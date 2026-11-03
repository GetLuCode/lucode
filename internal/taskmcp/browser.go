package taskmcp

import "strings"

// Browser tools: control Microsoft Edge through the DevTools protocol (see internal/cdp).
// They are not in Names(): no agent gets them by default. The orchestrator has them, and an
// agent only when they are listed in its lucode_tools. Unlike the task tools they are not
// auto-approved: acting on web pages needs the user's approval like any other powerful tool.
const (
	ToolBrowserLaunch   = "browser_launch"
	ToolBrowserClose    = "browser_close"
	ToolBrowserTabs     = "browser_tabs"
	ToolBrowserNavigate = "browser_navigate"
	ToolBrowserList     = "browser_list_elements"
	ToolBrowserFind     = "browser_find_element"
	ToolBrowserClick    = "browser_click"
	ToolBrowserMouse    = "browser_mouse"
	ToolBrowserType     = "browser_type"
	ToolBrowserKey      = "browser_key"
	ToolBrowserShot     = "browser_screenshot"
	ToolBrowserText     = "browser_get_text"
	ToolBrowserDialog   = "browser_dialog"
)

// BrowserNames lists the browser tools, in display order.
func BrowserNames() []string {
	return []string{ToolBrowserLaunch, ToolBrowserClose, ToolBrowserTabs, ToolBrowserNavigate, ToolBrowserList, ToolBrowserFind,
		ToolBrowserClick, ToolBrowserMouse, ToolBrowserType, ToolBrowserKey, ToolBrowserShot, ToolBrowserText, ToolBrowserDialog}
}

// IsBrowserTool reports whether name is a browser tool, with or without the mcp__lucode__ prefix.
func IsBrowserTool(name string) bool {
	n := strings.TrimPrefix(name, "mcp__lucode__")
	for _, b := range BrowserNames() {
		if b == n {
			return true
		}
	}
	return false
}

func props(m map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": m}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

const refHelp = "A ref such as e42, returned by browser_list_elements or browser_find_element. It stays valid until the page navigates or re-renders that element."

var browserTools = []tool{
	{
		Name: ToolBrowserLaunch,
		Description: "Start Microsoft Edge under lucode's control, or reuse the one already running. mode=window opens a visible window (use it when the user wants to watch or help); mode=headless runs without a window (faster, for background work). " +
			"It uses its own profile (not the user's daily one), so logins made in it persist between runs. Asking for the other mode restarts a browser lucode started. Call it before any other browser tool.",
		InputSchema: props(map[string]any{
			"mode":        map[string]any{"type": "string", "enum": []string{"window", "headless"}, "description": "window (default) or headless."},
			"url":         str("Page to open first. Default about:blank."),
			"port":        num("Remote debugging port. Default 9222."),
			"profile_dir": str("Browser profile folder. Default ~/.lucode/edge-profile."),
		}),
	},
	{
		Name:        ToolBrowserClose,
		Description: "Close the Edge that browser_launch started (or just detach from one lucode did not start). Close it when you are done.",
		InputSchema: props(map[string]any{}),
	},
	{
		Name:        ToolBrowserTabs,
		Description: "List, open, switch to or close tabs. Every other browser tool acts on the current tab.",
		InputSchema: props(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "new", "switch", "close"}, "description": "list (default), new, switch or close."},
			"id":     str("Tab id (or a unique prefix of it) for switch / close. close without id closes the current tab."),
			"url":    str("URL for new."),
		}),
	},
	{
		Name:        ToolBrowserNavigate,
		Description: "Open a URL in the current tab and wait for it to load, or go back / forward / reload. Returns the page title and URL.",
		InputSchema: props(map[string]any{
			"url":    str("Address to open (https:// is assumed when there is no scheme)."),
			"action": map[string]any{"type": "string", "enum": []string{"back", "forward", "reload"}, "description": "Instead of a url."},
		}),
	},
	{
		Name: ToolBrowserList,
		Description: "List the elements of the current page as the accessibility tree describes them: [ref] role \"accessible name\" {states}. " +
			"This is how you see what can be done on a page, and it is far cheaper and more reliable than a screenshot: prefer it. By default only interactive elements (buttons, links, fields, checkboxes, tabs, menu items...); all=true adds headings, landmarks, dialogs and images. " +
			"An interactive element shown as \"(no accessible name)\" comes with its HTML tag so you can reach it with browser_find_element selector=…",
		InputSchema: props(map[string]any{
			"roles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only these roles, e.g. [\"button\",\"link\"] or [\"textbox\"]."},
			"query": str("Only elements whose name, value or description contains this text."),
			"all":   flag("Include non-interactive structure (headings, landmarks, dialogs, images)."),
			"limit": num("Maximum elements (default 150)."),
		}),
	},
	{
		Name: ToolBrowserFind,
		Description: "Find elements by what a user perceives, using the accessibility tree: role (button, link, textbox, checkbox, combobox, tab, menuitem, heading...) and accessible name (its label, aria-label or text). Best match first. " +
			"Use text to match any visible text of the element. Use selector (CSS) only for elements that have no accessible name. Returns refs for browser_click / browser_type / browser_mouse.",
		InputSchema: props(map[string]any{
			"role":     str("ARIA role, e.g. button, link, textbox."),
			"name":     str("Accessible name; matches if it contains this (case-insensitive) unless exact."),
			"text":     str("Matches the name, value, description or surrounding text; lands on the interactive element holding it."),
			"selector": str("CSS selector, the fallback when there is no usable role/name."),
			"exact":    flag("name must match exactly."),
			"limit":    num("Maximum results (default 10)."),
		}),
	},
	{
		Name:        ToolBrowserClick,
		Description: "Scroll an element into view and click its center. Returns what changed (page title / URL, or an open JavaScript dialog to answer with browser_dialog).",
		InputSchema: props(map[string]any{
			"ref":    str(refHelp),
			"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}, "description": "Default left."},
			"double": flag("Double click."),
		}, "ref"),
	},
	{
		Name: ToolBrowserMouse,
		Description: "Low-level pointer control: move (hover), click, double_click, right_click, down, up, or wheel (scroll). Aim with a ref (the middle of that element) or with x,y in viewport pixels (from a screenshot). " +
			"Use it for hover menus, drag (down, move, up), canvases and scrolling; for ordinary buttons and links use browser_click.",
		InputSchema: props(map[string]any{
			"action":  map[string]any{"type": "string", "enum": []string{"move", "click", "double_click", "right_click", "down", "up", "wheel"}},
			"ref":     str(refHelp),
			"x":       num("Viewport x in CSS pixels."),
			"y":       num("Viewport y in CSS pixels."),
			"delta_y": num("wheel: vertical scroll in pixels (positive = down)."),
			"delta_x": num("wheel: horizontal scroll in pixels."),
			"button":  map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
		}, "action"),
	},
	{
		Name:        ToolBrowserType,
		Description: "Type text into a field (focusing it first when you give a ref), or into whatever is focused. clear=true replaces the existing content; submit=true presses Enter afterwards.",
		InputSchema: props(map[string]any{
			"ref":    str(refHelp),
			"text":   str("What to type."),
			"clear":  flag("Replace the current content instead of appending."),
			"submit": flag("Press Enter after typing."),
		}, "text"),
	},
	{
		Name:        ToolBrowserKey,
		Description: "Press a key or shortcut on the focused element: Enter, Tab, Escape, Backspace, Delete, Space, ArrowDown/Up/Left/Right, Home, End, PageUp, PageDown, F1-F12, a character, or combinations like Control+a, Shift+Tab, Alt+ArrowLeft.",
		InputSchema: props(map[string]any{"key": str("Key or combination, e.g. Enter or Control+Shift+k.")}, "key"),
	},
	{
		Name:        ToolBrowserShot,
		Description: "Take a screenshot of the current tab. Use it when the accessibility tree is not enough (canvas, layout, something visual); otherwise prefer browser_list_elements and browser_get_text.",
		InputSchema: props(map[string]any{"full_page": flag("Capture the whole scrollable page, not only the viewport.")}),
	},
	{
		Name:        ToolBrowserText,
		Description: "Read the visible text of the page (or of one element when you give a ref), cut to max_chars. Use it to read content; use browser_list_elements to see what you can click.",
		InputSchema: props(map[string]any{
			"ref":       str(refHelp),
			"max_chars": num("Default 20000."),
		}),
	},
	{
		Name:        ToolBrowserDialog,
		Description: "Answer the JavaScript dialog (alert, confirm, prompt, beforeunload) that is blocking the page; a click that opens one reports it.",
		InputSchema: props(map[string]any{
			"accept": flag("true = OK / Yes, false = Cancel (default true)."),
			"text":   str("Text to enter for a prompt."),
		}),
	},
}

func init() { tools = append(tools, browserTools...) }
