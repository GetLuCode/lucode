package cdp

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// resolveLocked turns a ref into a remote object id of its DOM element.
func (b *Browser) resolveLocked(ref string) (string, error) {
	id, err := parseRef(ref)
	if err != nil {
		return "", err
	}
	var out struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := b.pageCall("DOM.resolveNode", map[string]any{"backendNodeId": id}, &out); err != nil {
		return "", staleRef(err)
	}
	if out.Object.ObjectID == "" {
		return "", staleRef(errors.New("No node found"))
	}
	return out.Object.ObjectID, nil
}

// centerLocked scrolls the element into view and returns the middle of its box (viewport coordinates).
func (b *Browser) centerLocked(objectID string) (x, y float64, err error) {
	_ = b.pageCall("DOM.scrollIntoViewIfNeeded", map[string]any{"objectId": objectID}, nil)
	var q struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := b.pageCall("DOM.getContentQuads", map[string]any{"objectId": objectID}, &q); err != nil {
		return 0, 0, err
	}
	for _, quad := range q.Quads {
		if len(quad) < 8 {
			continue
		}
		w, h := quad[2]-quad[0], quad[7]-quad[1]
		if w <= 0 || h <= 0 {
			continue
		}
		return (quad[0] + quad[2] + quad[4] + quad[6]) / 4, (quad[1] + quad[3] + quad[5] + quad[7]) / 4, nil
	}
	return 0, 0, errors.New("the element has no visible box (hidden, collapsed or off-screen)")
}

// ActionResult is what an action reports back so the caller sees what happened.
type ActionResult struct {
	Message string   `json:"message"`
	Page    PageInfo `json:"page"`
	Dialog  string   `json:"dialog,omitempty"` // a JavaScript dialog opened and blocks the page
}

func (r ActionResult) String() string {
	s := r.Message
	if r.Page.URL != "" {
		s += fmt.Sprintf("\nPage: %q %s", r.Page.Title, r.Page.URL)
	}
	if r.Dialog != "" {
		s += "\nA JavaScript dialog is open (" + r.Dialog + "): answer it with browser_dialog."
	}
	return s
}

// settleLocked gives the page a moment to react to an action and waits if it started loading something.
func (b *Browser) settleLocked(before PageInfo) PageInfo {
	time.Sleep(200 * time.Millisecond)
	after := b.infoLocked()
	if after.URL != before.URL {
		b.waitLoadedLocked()
		after = b.infoLocked()
	}
	return after
}

func (b *Browser) resultLocked(msg string, before PageInfo) ActionResult {
	r := ActionResult{Message: msg, Page: b.settleLocked(before)}
	if b.current != "" {
		r.Dialog = b.dialogOf(b.sessions[b.current])
	}
	return r
}

func buttonName(s string) string {
	switch strings.ToLower(s) {
	case "right":
		return "right"
	case "middle":
		return "middle"
	}
	return "left"
}

func buttonsMask(button string) int {
	switch button {
	case "right":
		return 2
	case "middle":
		return 4
	}
	return 1
}

// mouseLocked sends the mouse events for a press at (x, y). A click that opens an alert() blocks
// the release event until the dialog is answered, so a timeout with a dialog open counts as done.
func (b *Browser) mouseLocked(x, y float64, button string, clicks int) error {
	sess, err := b.sessionLocked()
	if err != nil {
		return err
	}
	send := func(typ string, count int, timeout time.Duration) error {
		p := map[string]any{"type": typ, "x": x, "y": y, "button": button, "clickCount": count, "buttons": buttonsMask(button)}
		if typ == "mouseMoved" || typ == "mouseReleased" {
			p["buttons"] = 0
		}
		if typ == "mouseMoved" {
			p["button"] = "none"
		}
		return b.c.call(sess, "Input.dispatchMouseEvent", p, nil, timeout)
	}
	b.mouseX, b.mouseY = x, y
	if err := send("mouseMoved", 0, 0); err != nil {
		return err
	}
	for i := 1; i <= clicks; i++ {
		if err := send("mousePressed", i, 3*time.Second); err != nil && b.dialogOf(sess) == "" {
			return err
		}
		if err := send("mouseReleased", i, 3*time.Second); err != nil && b.dialogOf(sess) == "" {
			return err
		}
	}
	return nil
}

// ClickOptions say what to click.
type ClickOptions struct {
	Ref    string
	Button string // left (default), right, middle
	Double bool
}

// Click scrolls an element into view and clicks the middle of it.
func (b *Browser) Click(o ClickOptions) (ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o.Ref == "" {
		return ActionResult{}, errors.New("ref is required (see browser_find_element); to click coordinates use browser_mouse")
	}
	obj, err := b.resolveLocked(o.Ref)
	if err != nil {
		return ActionResult{}, err
	}
	desc := o.Ref
	if id, err := parseRef(o.Ref); err == nil {
		if e, err := b.elementOfLocked(id); err == nil {
			desc = strings.TrimSpace(FormatElements([]Element{e}, false))
		}
	}
	before := b.infoLocked()
	x, y, err := b.centerLocked(obj)
	if err != nil {
		// no box to click: let the page's own click handler run
		if cerr := b.pageCall("Runtime.callFunctionOn", map[string]any{"objectId": obj, "functionDeclaration": "function(){ this.click() }"}, nil); cerr != nil {
			return ActionResult{}, err
		}
		return b.resultLocked("Clicked "+desc+" (through the DOM: it has no visible box).", before), nil
	}
	clicks := 1
	if o.Double {
		clicks = 2
	}
	if err := b.mouseLocked(x, y, buttonName(o.Button), clicks); err != nil {
		return ActionResult{}, err
	}
	return b.resultLocked(fmt.Sprintf("Clicked %s at (%.0f, %.0f).", desc, x, y), before), nil
}

// MouseOptions drive the pointer directly. Coordinates are CSS pixels of the viewport; a Ref
// aims at the middle of that element instead.
type MouseOptions struct {
	Action string // move, click, double_click, right_click, down, up, wheel
	Ref    string
	X, Y   *float64
	DeltaX float64
	DeltaY float64 // wheel
	Button string
}

// Mouse moves the pointer, presses or releases buttons, or turns the wheel.
func (b *Browser) Mouse(o MouseOptions) (ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, err := b.sessionLocked()
	if err != nil {
		return ActionResult{}, err
	}
	x, y := b.mouseX, b.mouseY
	switch {
	case o.Ref != "":
		obj, err := b.resolveLocked(o.Ref)
		if err != nil {
			return ActionResult{}, err
		}
		if x, y, err = b.centerLocked(obj); err != nil {
			return ActionResult{}, err
		}
	case o.X != nil && o.Y != nil:
		x, y = *o.X, *o.Y
	case o.Action == "" || o.Action == "move" || o.Action == "click" || o.Action == "double_click" || o.Action == "right_click":
		return ActionResult{}, errors.New("give x and y (viewport pixels) or a ref")
	}
	before := b.infoLocked()
	button := buttonName(o.Button)
	where := fmt.Sprintf("(%.0f, %.0f)", x, y)
	switch o.Action {
	case "move", "":
		err = b.c.call(sess, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil, 0)
		b.mouseX, b.mouseY = x, y
		return b.resultLocked("Moved the pointer to "+where+".", before), err
	case "click":
		err = b.mouseLocked(x, y, button, 1)
	case "double_click":
		err = b.mouseLocked(x, y, button, 2)
	case "right_click":
		err = b.mouseLocked(x, y, "right", 1)
	case "down", "up":
		typ := map[string]string{"down": "mousePressed", "up": "mouseReleased"}[o.Action]
		buttons := buttonsMask(button)
		if o.Action == "up" {
			buttons = 0
		}
		err = b.c.call(sess, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": x, "y": y, "button": button, "buttons": buttons, "clickCount": 1}, nil, 3*time.Second)
		b.mouseX, b.mouseY = x, y
	case "wheel":
		err = b.c.call(sess, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": o.DeltaX, "deltaY": o.DeltaY}, nil, 0)
		b.mouseX, b.mouseY = x, y
		return b.resultLocked(fmt.Sprintf("Scrolled by (%.0f, %.0f) at %s.", o.DeltaX, o.DeltaY, where), before), err
	default:
		return ActionResult{}, fmt.Errorf("unknown mouse action %q: use move, click, double_click, right_click, down, up or wheel", o.Action)
	}
	if err != nil {
		return ActionResult{}, err
	}
	return b.resultLocked(fmt.Sprintf("Mouse %s at %s.", o.Action, where), before), nil
}

// TypeOptions say what to type and where.
type TypeOptions struct {
	Ref    string // element to focus first; empty = whatever is focused
	Text   string
	Clear  bool // replace the current content
	Submit bool // press Enter afterwards
}

// Type puts text into an element (or the focused one) as if pasted, optionally replacing the old text.
func (b *Browser) Type(o TypeOptions) (ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.sessionLocked(); err != nil {
		return ActionResult{}, err
	}
	before := b.infoLocked()
	desc := "the focused element"
	if o.Ref != "" {
		obj, err := b.resolveLocked(o.Ref)
		if err != nil {
			return ActionResult{}, err
		}
		_ = b.pageCall("DOM.scrollIntoViewIfNeeded", map[string]any{"objectId": obj}, nil)
		if err := b.pageCall("DOM.focus", map[string]any{"objectId": obj}, nil); err != nil {
			return ActionResult{}, fmt.Errorf("could not focus %s: %w", o.Ref, err)
		}
		desc = o.Ref
		if o.Clear {
			const sel = `function(){ if (this.select) { this.select() } else { var r = document.createRange(); r.selectNodeContents(this); var s = getSelection(); s.removeAllRanges(); s.addRange(r) } }`
			if err := b.pageCall("Runtime.callFunctionOn", map[string]any{"objectId": obj, "functionDeclaration": sel}, nil); err != nil {
				return ActionResult{}, err
			}
		}
	} else if o.Clear {
		if err := b.pressLocked("Control+a"); err != nil {
			return ActionResult{}, err
		}
	}
	if o.Text != "" {
		if err := b.pageCall("Input.insertText", map[string]any{"text": o.Text}, nil); err != nil {
			return ActionResult{}, err
		}
	} else if o.Clear {
		if err := b.pressLocked("Backspace"); err != nil {
			return ActionResult{}, err
		}
	}
	if o.Submit {
		if err := b.pressLocked("Enter"); err != nil {
			return ActionResult{}, err
		}
	}
	msg := fmt.Sprintf("Typed %d characters into %s", len([]rune(o.Text)), desc)
	if o.Clear {
		msg += " (replacing its content)"
	}
	if o.Submit {
		msg += " and pressed Enter"
	}
	return b.resultLocked(msg+".", before), nil
}

type keyDef struct {
	key, code string
	vk        int
	text      string
}

var namedKeys = map[string]keyDef{
	"enter":      {"Enter", "Enter", 13, "\r"},
	"return":     {"Enter", "Enter", 13, "\r"},
	"tab":        {"Tab", "Tab", 9, ""},
	"escape":     {"Escape", "Escape", 27, ""},
	"esc":        {"Escape", "Escape", 27, ""},
	"backspace":  {"Backspace", "Backspace", 8, ""},
	"delete":     {"Delete", "Delete", 46, ""},
	"space":      {" ", "Space", 32, " "},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowup":    {"ArrowUp", "ArrowUp", 38, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40, ""},
	"left":       {"ArrowLeft", "ArrowLeft", 37, ""},
	"up":         {"ArrowUp", "ArrowUp", 38, ""},
	"right":      {"ArrowRight", "ArrowRight", 39, ""},
	"down":       {"ArrowDown", "ArrowDown", 40, ""},
	"home":       {"Home", "Home", 36, ""},
	"end":        {"End", "End", 35, ""},
	"pageup":     {"PageUp", "PageUp", 33, ""},
	"pagedown":   {"PageDown", "PageDown", 34, ""},
}

var editCommands = map[string]string{"a": "selectAll", "c": "copy", "x": "cut", "v": "paste", "z": "undo", "y": "redo"}

func keyOf(name string) (keyDef, error) {
	if k, ok := namedKeys[strings.ToLower(name)]; ok {
		return k, nil
	}
	if r := []rune(name); len(r) == 1 {
		c := string(r)
		k := keyDef{key: c, text: c}
		switch {
		case r[0] >= 'a' && r[0] <= 'z':
			k.code, k.vk = "Key"+strings.ToUpper(c), int(r[0]-'a')+'A'
		case r[0] >= 'A' && r[0] <= 'Z':
			k.code, k.vk = "Key"+c, int(r[0])
		case r[0] >= '0' && r[0] <= '9':
			k.code, k.vk = "Digit"+c, int(r[0])
		}
		return k, nil
	}
	if len(name) >= 2 && (name[0] == 'F' || name[0] == 'f') {
		var n int
		if _, err := fmt.Sscanf(name[1:], "%d", &n); err == nil && n >= 1 && n <= 12 {
			return keyDef{key: fmt.Sprintf("F%d", n), code: fmt.Sprintf("F%d", n), vk: 111 + n}, nil
		}
	}
	return keyDef{}, fmt.Errorf("unknown key %q: use Enter, Tab, Escape, Backspace, Delete, Space, arrows, Home, End, PageUp, PageDown, F1-F12 or a single character; combine with Control+, Alt+, Shift+, Meta+", name)
}

// pressLocked presses a key or a combination such as "Control+Shift+k".
func (b *Browser) pressLocked(combo string) error {
	parts := strings.Split(combo, "+")
	if combo == "+" || strings.HasSuffix(combo, "++") {
		parts = []string{"+"}
	}
	mods := 0
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "alt":
			mods |= 1
		case "control", "ctrl":
			mods |= 2
		case "meta", "cmd", "command", "win":
			mods |= 4
		case "shift":
			mods |= 8
		default:
			return fmt.Errorf("unknown modifier %q", m)
		}
	}
	k, err := keyOf(parts[len(parts)-1])
	if err != nil {
		return err
	}
	ev := map[string]any{"key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": mods}
	down := map[string]any{"type": "rawKeyDown"}
	if k.text != "" && mods&^8 == 0 { // a character is produced unless Ctrl/Alt/Meta make it a shortcut
		text := k.text
		if mods&8 != 0 {
			text = strings.ToUpper(text)
		}
		down["type"], down["text"] = "keyDown", text
	}
	if mods&2 != 0 && len([]rune(k.key)) == 1 {
		if cmd, ok := editCommands[strings.ToLower(k.key)]; ok {
			down["commands"] = []string{cmd}
		}
	}
	for kk, v := range ev {
		down[kk] = v
	}
	up := map[string]any{"type": "keyUp"}
	for kk, v := range ev {
		up[kk] = v
	}
	if err := b.pageCall("Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return b.pageCall("Input.dispatchKeyEvent", up, nil)
}

// Press presses a key or combination on the focused element.
func (b *Browser) Press(combo string) (ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.sessionLocked(); err != nil {
		return ActionResult{}, err
	}
	before := b.infoLocked()
	if err := b.pressLocked(combo); err != nil {
		return ActionResult{}, err
	}
	return b.resultLocked("Pressed "+combo+".", before), nil
}

// Text returns the visible text of the page, or of one element, cut to max characters.
func (b *Browser) Text(ref string, max int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if max <= 0 {
		max = 20000
	}
	var out struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		Exception any `json:"exceptionDetails"`
	}
	if ref == "" {
		if err := b.pageCall("Runtime.evaluate", map[string]any{"expression": "document.body ? document.body.innerText : ''", "returnByValue": true}, &out); err != nil {
			return "", err
		}
	} else {
		obj, err := b.resolveLocked(ref)
		if err != nil {
			return "", err
		}
		if err := b.pageCall("Runtime.callFunctionOn", map[string]any{"objectId": obj, "functionDeclaration": "function(){ return this.innerText !== undefined ? this.innerText : this.textContent }", "returnByValue": true}, &out); err != nil {
			return "", err
		}
	}
	s := out.Result.Value
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + fmt.Sprintf("\n… (%d more characters)", len(r)-max)
	}
	return s, nil
}
