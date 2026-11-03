package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"claude-orchestrator/internal/cdp"
	"claude-orchestrator/internal/taskmcp"
)

// browserPrompt describes the browser tools to a session that has them.
const browserPrompt = `

# Controlling the browser (Microsoft Edge)
Tools (mcp__lucode__<name>) drive an Edge that runs on the machine where lucode runs:
- browser_launch(mode: window|headless, url?): start it first. Use window when the user wants to watch or sign in, headless for background work. browser_close when you are done.
- browser_list_elements / browser_find_element: see and locate things through the ACCESSIBILITY tree (role + accessible name). Always start here: it is cheaper and more reliable than a screenshot. Use selector (CSS) only for elements with no accessible name.
- browser_click(ref), browser_type(ref, text, clear?, submit?), browser_key(key): act on the refs they return. A ref is stale after the page changes: list again.
- browser_mouse: pointer by coordinates or ref (hover, drag, wheel scroll). browser_get_text reads content. browser_screenshot only when the tree is not enough.
- browser_navigate, browser_tabs, browser_dialog.
Never enter passwords or payment data on the user's behalf unless they asked; ask them to do it in the window.`

// handleBrowserTool runs a browser tool. The browser lives in this process, so it is shared by
// every session that has the tools.
func (h *Handler) handleBrowserTool(tool string, args json.RawMessage) (string, error) {
	name := strings.TrimPrefix(tool, "mcp__lucode__")
	br := cdp.Default

	var a struct {
		Mode       string   `json:"mode"`
		URL        string   `json:"url"`
		Port       int      `json:"port"`
		ProfileDir string   `json:"profile_dir"`
		Action     string   `json:"action"`
		ID         string   `json:"id"`
		Roles      []string `json:"roles"`
		Query      string   `json:"query"`
		All        bool     `json:"all"`
		Limit      int      `json:"limit"`
		Role       string   `json:"role"`
		Name       string   `json:"name"`
		Text       string   `json:"text"`
		Selector   string   `json:"selector"`
		Exact      bool     `json:"exact"`
		Ref        string   `json:"ref"`
		Button     string   `json:"button"`
		Double     bool     `json:"double"`
		X          *float64 `json:"x"`
		Y          *float64 `json:"y"`
		DeltaX     float64  `json:"delta_x"`
		DeltaY     float64  `json:"delta_y"`
		Clear      bool     `json:"clear"`
		Submit     bool     `json:"submit"`
		Key        string   `json:"key"`
		FullPage   bool     `json:"full_page"`
		MaxChars   int      `json:"max_chars"`
		Accept     *bool    `json:"accept"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	page := func(p cdp.PageInfo) string { return fmt.Sprintf("%q %s", p.Title, p.URL) }

	switch name {
	case taskmcp.ToolBrowserLaunch:
		return br.Launch(cdp.LaunchOptions{Headless: a.Mode == "headless", Port: a.Port, URL: a.URL, ProfileDir: a.ProfileDir})

	case taskmcp.ToolBrowserClose:
		return br.Close(), nil

	case taskmcp.ToolBrowserTabs:
		switch a.Action {
		case "", "list":
			tabs, err := br.Tabs()
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, t := range tabs {
				mark := " "
				if t.Current {
					mark = "*"
				}
				fmt.Fprintf(&sb, "%s %s  %q  %s\n", mark, t.ID, t.Title, t.URL)
			}
			if sb.Len() == 0 {
				return "No tabs are open.", nil
			}
			return sb.String() + "(* = current tab)", nil
		case "new":
			p, err := br.NewTab(a.URL)
			if err != nil {
				return "", err
			}
			return "Opened a new tab: " + page(p), nil
		case "switch":
			p, err := br.SwitchTab(a.ID)
			if err != nil {
				return "", err
			}
			return "Switched to " + page(p), nil
		case "close":
			if err := br.CloseTab(a.ID); err != nil {
				return "", err
			}
			return "Tab closed.", nil
		}
		return "", fmt.Errorf("unknown action %q: use list, new, switch or close", a.Action)

	case taskmcp.ToolBrowserNavigate:
		var p cdp.PageInfo
		var err error
		switch {
		case a.Action != "":
			if a.Action != "back" && a.Action != "forward" && a.Action != "reload" {
				return "", fmt.Errorf("unknown action %q: use back, forward or reload", a.Action)
			}
			p, err = br.History(a.Action)
		case strings.TrimSpace(a.URL) != "":
			p, err = br.Navigate(strings.TrimSpace(a.URL))
		default:
			return "", errors.New("give a url, or an action (back, forward, reload)")
		}
		if err != nil {
			return "", err
		}
		return "Now at " + page(p), nil

	case taskmcp.ToolBrowserList:
		els, total, p, err := br.List(cdp.ListOptions{Roles: a.Roles, Query: a.Query, All: a.All, Limit: a.Limit})
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("Page: %s\n", page(p))
		if len(els) == 0 {
			return out + "No matching elements.", nil
		}
		out += cdp.FormatElements(els, a.All)
		if total > len(els) {
			out += fmt.Sprintf("… %d more not shown: narrow it with roles or query, or raise limit.", total-len(els))
		}
		return out, nil

	case taskmcp.ToolBrowserFind:
		els, err := br.Find(cdp.FindOptions{Role: a.Role, Name: a.Name, Text: a.Text, Selector: a.Selector, Exact: a.Exact, Limit: a.Limit})
		if err != nil {
			return "", err
		}
		if len(els) == 0 {
			return "No element matches. Try browser_list_elements to see what the page offers, or a shorter name.", nil
		}
		return cdp.FormatElements(els, false), nil

	case taskmcp.ToolBrowserClick:
		r, err := br.Click(cdp.ClickOptions{Ref: a.Ref, Button: a.Button, Double: a.Double})
		return r.String(), err

	case taskmcp.ToolBrowserMouse:
		r, err := br.Mouse(cdp.MouseOptions{Action: a.Action, Ref: a.Ref, X: a.X, Y: a.Y, DeltaX: a.DeltaX, DeltaY: a.DeltaY, Button: a.Button})
		return r.String(), err

	case taskmcp.ToolBrowserType:
		r, err := br.Type(cdp.TypeOptions{Ref: a.Ref, Text: a.Text, Clear: a.Clear, Submit: a.Submit})
		return r.String(), err

	case taskmcp.ToolBrowserKey:
		if strings.TrimSpace(a.Key) == "" {
			return "", errors.New("key is required")
		}
		r, err := br.Press(strings.TrimSpace(a.Key))
		return r.String(), err

	case taskmcp.ToolBrowserShot:
		b64, err := br.Screenshot(a.FullPage)
		if err != nil {
			return "", err
		}
		return taskmcp.ImageResult("image/png", b64), nil

	case taskmcp.ToolBrowserText:
		return br.Text(a.Ref, a.MaxChars)

	case taskmcp.ToolBrowserDialog:
		return br.Dialog(a.Accept == nil || *a.Accept, a.Text)
	}
	return "", fmt.Errorf("unknown browser tool %q", name)
}
