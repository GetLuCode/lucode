package toolengine

import (
	"encoding/json"
	"strings"
	"time"

	"claude-orchestrator/internal/cdp"

	"github.com/dop251/goja"
)

// browserTimeout replaces the default limit for tools that drive the browser (starting
// Edge and loading pages takes longer than file or HTTP work).
const browserTimeout = 60 * time.Second

// plain converts a Go value to what JavaScript sees: maps, arrays and scalars (via its JSON form).
func plain(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func optStr(o map[string]any, k string) string {
	s, _ := o[k].(string)
	return s
}

func optBool(o map[string]any, k string) bool {
	b, _ := o[k].(bool)
	return b
}

func optNum(o map[string]any, k string) (float64, bool) {
	switch n := o[k].(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func optInt(o map[string]any, k string) int {
	n, _ := optNum(o, k)
	return int(n)
}

func optStrings(o map[string]any, k string) []string {
	var out []string
	if arr, ok := o[k].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func opts(o []map[string]any) map[string]any {
	if len(o) > 0 && o[0] != nil {
		return o[0]
	}
	return map[string]any{}
}

// injectCDP gives user tools control of Microsoft Edge through the same browser the MCP
// browser_* tools use. Elements are found by role and accessible name first.
//
//	cdp.launch({mode: "headless"|"window", url, port, profileDir})   cdp.close()
//	cdp.tabs()  cdp.newTab(url)  cdp.switchTab(id)  cdp.closeTab(id?)
//	cdp.navigate(url)  cdp.back()  cdp.forward()  cdp.reload()  cdp.info()
//	cdp.list({roles, query, all, limit}) -> {page, total, elements}
//	cdp.find({role, name, text, selector, exact, limit}) -> elements ({ref, role, name, ...})
//	cdp.click(ref, {button, double})  cdp.type(ref|null, text, {clear, submit})  cdp.key("Control+a")
//	cdp.mouse({action, ref, x, y, deltaX, deltaY, button})  cdp.text(ref?, maxChars?)
//	cdp.screenshot({fullPage}) -> base64 PNG  cdp.dialog(accept?, promptText?)
func injectCDP(vm *goja.Runtime) {
	br := cdp.Default
	result := func(r cdp.ActionResult, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return plain(r), nil
	}
	pageInfo := func(p cdp.PageInfo, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return plain(p), nil
	}
	vm.Set("cdp", map[string]any{
		"launch": func(o ...map[string]any) (string, error) {
			m := opts(o)
			return br.Launch(cdp.LaunchOptions{
				Headless:   optStr(m, "mode") == "headless" || optBool(m, "headless"),
				Port:       optInt(m, "port"),
				URL:        optStr(m, "url"),
				ProfileDir: optStr(m, "profileDir"),
			})
		},
		"close": func() string { return br.Close() },
		"tabs": func() (any, error) {
			t, err := br.Tabs()
			return plain(t), err
		},
		"newTab":    func(url string) (any, error) { return pageInfo(br.NewTab(url)) },
		"switchTab": func(id string) (any, error) { return pageInfo(br.SwitchTab(id)) },
		"closeTab": func(id ...string) error {
			if len(id) > 0 {
				return br.CloseTab(id[0])
			}
			return br.CloseTab("")
		},
		"navigate": func(url string) (any, error) { return pageInfo(br.Navigate(url)) },
		"back":     func() (any, error) { return pageInfo(br.History("back")) },
		"forward":  func() (any, error) { return pageInfo(br.History("forward")) },
		"reload":   func() (any, error) { return pageInfo(br.History("reload")) },
		"info":     func() (any, error) { return pageInfo(br.Info()) },
		"list": func(o ...map[string]any) (any, error) {
			m := opts(o)
			els, total, page, err := br.List(cdp.ListOptions{Roles: optStrings(m, "roles"), Query: optStr(m, "query"), All: optBool(m, "all"), Limit: optInt(m, "limit")})
			if err != nil {
				return nil, err
			}
			return map[string]any{"page": plain(page), "total": total, "elements": plain(els)}, nil
		},
		"find": func(o map[string]any) (any, error) {
			els, err := br.Find(cdp.FindOptions{
				Role: optStr(o, "role"), Name: optStr(o, "name"), Text: optStr(o, "text"),
				Selector: optStr(o, "selector"), Exact: optBool(o, "exact"), Limit: optInt(o, "limit"),
			})
			if err != nil {
				return nil, err
			}
			return plain(els), nil
		},
		"click": func(ref string, o ...map[string]any) (any, error) {
			m := opts(o)
			return result(br.Click(cdp.ClickOptions{Ref: ref, Button: optStr(m, "button"), Double: optBool(m, "double")}))
		},
		"type": func(ref any, text string, o ...map[string]any) (any, error) {
			m := opts(o)
			r, _ := ref.(string)
			return result(br.Type(cdp.TypeOptions{Ref: r, Text: text, Clear: optBool(m, "clear"), Submit: optBool(m, "submit")}))
		},
		"key": func(combo string) (any, error) { return result(br.Press(strings.TrimSpace(combo))) },
		"mouse": func(o map[string]any) (any, error) {
			mo := cdp.MouseOptions{Action: optStr(o, "action"), Ref: optStr(o, "ref"), Button: optStr(o, "button")}
			if x, ok := optNum(o, "x"); ok {
				mo.X = &x
			}
			if y, ok := optNum(o, "y"); ok {
				mo.Y = &y
			}
			mo.DeltaX, _ = optNum(o, "deltaX")
			mo.DeltaY, _ = optNum(o, "deltaY")
			return result(br.Mouse(mo))
		},
		"text": func(args ...any) (string, error) {
			ref, max := "", 0
			if len(args) > 0 {
				ref, _ = args[0].(string)
			}
			if len(args) > 1 {
				if n, ok := args[1].(int64); ok {
					max = int(n)
				} else if f, ok := args[1].(float64); ok {
					max = int(f)
				}
			}
			return br.Text(ref, max)
		},
		"screenshot": func(o ...map[string]any) (string, error) { return br.Screenshot(optBool(opts(o), "fullPage")) },
		"dialog": func(args ...any) (string, error) {
			accept, text := true, ""
			if len(args) > 0 {
				if b, ok := args[0].(bool); ok {
					accept = b
				}
			}
			if len(args) > 1 {
				text, _ = args[1].(string)
			}
			return br.Dialog(accept, text)
		},
	})
}
