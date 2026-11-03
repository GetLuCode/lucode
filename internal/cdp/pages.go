package cdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tab is an open page of the browser.
type Tab struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Current bool   `json:"current"`
}

// PageInfo is where the current tab is.
type PageInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (b *Browser) tabsLocked() ([]Tab, error) {
	var out struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := b.c.call("", "Target.getTargets", nil, &out, 0); err != nil {
		return nil, err
	}
	var tabs []Tab
	for _, t := range out.TargetInfos {
		if t.Type == "page" {
			tabs = append(tabs, Tab{ID: t.TargetID, Title: t.Title, URL: t.URL, Current: t.TargetID == b.current})
		}
	}
	return tabs, nil
}

// Tabs lists the open pages.
func (b *Browser) Tabs() ([]Tab, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureLocked(); err != nil {
		return nil, err
	}
	return b.tabsLocked()
}

// sessionLocked returns the session of the current tab, picking and attaching one if needed.
func (b *Browser) sessionLocked() (string, error) {
	if err := b.ensureLocked(); err != nil {
		return "", err
	}
	if b.current != "" {
		if s, ok := b.sessions[b.current]; ok {
			return s, nil
		}
	}
	if b.current == "" {
		tabs, err := b.tabsLocked()
		if err != nil {
			return "", err
		}
		if len(tabs) == 0 {
			return "", errors.New("the browser has no open tab: open one with browser_tabs action=new")
		}
		b.current = tabs[0].ID
	}
	return b.attachLocked(b.current)
}

func (b *Browser) attachLocked(target string) (string, error) {
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.c.call("", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &out, 0); err != nil {
		return "", err
	}
	for _, m := range []string{"Page.enable", "DOM.enable", "Accessibility.enable"} {
		if err := b.c.call(out.SessionID, m, nil, nil, 0); err != nil {
			return "", fmt.Errorf("%s: %w", m, err)
		}
	}
	b.sessions[target] = out.SessionID
	return out.SessionID, nil
}

// pageCall runs a command on the current tab. If the tab was closed meanwhile the next call picks another.
func (b *Browser) pageCall(method string, params, out any) error {
	return b.pageCallT(method, params, out, 0)
}

func (b *Browser) pageCallT(method string, params, out any, timeout time.Duration) error {
	sess, err := b.sessionLocked()
	if err != nil {
		return err
	}
	err = b.c.call(sess, method, params, out, timeout)
	if isGoneTarget(err) {
		delete(b.sessions, b.current)
		b.current = ""
		return errors.New("the tab was closed: list the tabs again (browser_tabs)")
	}
	return err
}

func (b *Browser) infoLocked() PageInfo {
	var out struct {
		TargetInfo struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"targetInfo"`
	}
	if b.current == "" {
		return PageInfo{}
	}
	if err := b.c.call("", "Target.getTargetInfo", map[string]any{"targetId": b.current}, &out, 0); err != nil {
		return PageInfo{}
	}
	return PageInfo{URL: out.TargetInfo.URL, Title: out.TargetInfo.Title}
}

// Info says where the current tab is.
func (b *Browser) Info() (PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.sessionLocked(); err != nil {
		return PageInfo{}, err
	}
	return b.infoLocked(), nil
}

// matchTab resolves a full tab id, a unique prefix of one, or a 1-based index in the tab list.
func matchTab(tabs []Tab, id string) (string, error) {
	id = strings.TrimSpace(id)
	for _, t := range tabs {
		if t.ID == id {
			return t.ID, nil
		}
	}
	var found []string
	for _, t := range tabs {
		if id != "" && strings.HasPrefix(t.ID, id) {
			found = append(found, t.ID)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return "", fmt.Errorf("tab %q not found (or ambiguous): list the tabs with browser_tabs", id)
}

// NewTab opens a page and makes it the current tab.
func (b *Browser) NewTab(url string) (PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureLocked(); err != nil {
		return PageInfo{}, err
	}
	if url == "" {
		url = "about:blank"
	}
	var out struct {
		TargetID string `json:"targetId"`
	}
	if err := b.c.call("", "Target.createTarget", map[string]any{"url": url}, &out, 0); err != nil {
		return PageInfo{}, err
	}
	b.current = out.TargetID
	if _, err := b.attachLocked(out.TargetID); err != nil {
		return PageInfo{}, err
	}
	b.waitLoadedLocked()
	return b.infoLocked(), nil
}

// SwitchTab makes a tab the current one and brings it to the front.
func (b *Browser) SwitchTab(id string) (PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureLocked(); err != nil {
		return PageInfo{}, err
	}
	tabs, err := b.tabsLocked()
	if err != nil {
		return PageInfo{}, err
	}
	target, err := matchTab(tabs, id)
	if err != nil {
		return PageInfo{}, err
	}
	if err := b.c.call("", "Target.activateTarget", map[string]any{"targetId": target}, nil, 0); err != nil {
		return PageInfo{}, err
	}
	b.current = target
	if _, ok := b.sessions[target]; !ok {
		if _, err := b.attachLocked(target); err != nil {
			return PageInfo{}, err
		}
	}
	return b.infoLocked(), nil
}

// CloseTab closes a tab (the current one when id is empty).
func (b *Browser) CloseTab(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureLocked(); err != nil {
		return err
	}
	target := b.current
	if id != "" {
		tabs, err := b.tabsLocked()
		if err != nil {
			return err
		}
		if target, err = matchTab(tabs, id); err != nil {
			return err
		}
	}
	if target == "" {
		return errors.New("no current tab")
	}
	if err := b.c.call("", "Target.closeTarget", map[string]any{"targetId": target}, nil, 0); err != nil {
		return err
	}
	delete(b.sessions, target)
	if b.current == target {
		b.current = ""
	}
	return nil
}

// waitLoadedLocked waits (up to 15 s) for the current document to finish loading.
func (b *Browser) waitLoadedLocked() {
	deadline := time.Now().Add(15 * time.Second)
	time.Sleep(120 * time.Millisecond) // let the navigation start replacing the old document
	for time.Now().Before(deadline) {
		var out struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		err := b.pageCallT("Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true}, &out, 3*time.Second)
		if err == nil && out.Result.Value == "complete" {
			return
		}
		if err != nil && !strings.Contains(err.Error(), "no answer") && !strings.Contains(err.Error(), "Cannot find context") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (b *Browser) navigateLocked(url string) (PageInfo, error) {
	if !strings.Contains(url, ":") {
		url = "https://" + url
	}
	var out struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.pageCall("Page.navigate", map[string]any{"url": url}, &out); err != nil {
		return PageInfo{}, err
	}
	if out.ErrorText != "" {
		return PageInfo{}, fmt.Errorf("could not open %s: %s", url, out.ErrorText)
	}
	b.waitLoadedLocked()
	return b.infoLocked(), nil
}

// Navigate opens a URL in the current tab and waits for it to load.
func (b *Browser) Navigate(url string) (PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.navigateLocked(url)
}

// History goes "back", "forward" or "reload" in the current tab.
func (b *Browser) History(action string) (PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if action == "reload" {
		if err := b.pageCall("Page.reload", nil, nil); err != nil {
			return PageInfo{}, err
		}
		b.waitLoadedLocked()
		return b.infoLocked(), nil
	}
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := b.pageCall("Page.getNavigationHistory", nil, &h); err != nil {
		return PageInfo{}, err
	}
	i := h.CurrentIndex + 1
	if action == "back" {
		i = h.CurrentIndex - 1
	}
	if i < 0 || i >= len(h.Entries) {
		return PageInfo{}, fmt.Errorf("there is nothing to go %s to", action)
	}
	if err := b.pageCall("Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil); err != nil {
		return PageInfo{}, err
	}
	b.waitLoadedLocked()
	return b.infoLocked(), nil
}

// Screenshot captures the current tab as a base64 PNG.
func (b *Browser) Screenshot(fullPage bool) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out struct {
		Data string `json:"data"`
	}
	params := map[string]any{"format": "png", "captureBeyondViewport": fullPage}
	if err := b.pageCallT("Page.captureScreenshot", params, &out, 30*time.Second); err != nil {
		return "", err
	}
	return out.Data, nil
}

// Dialog answers the open JavaScript dialog (alert, confirm, prompt, beforeunload).
func (b *Browser) Dialog(accept bool, text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, err := b.sessionLocked()
	if err != nil {
		return "", err
	}
	msg := b.dialogOf(sess)
	if msg == "" {
		return "", errors.New("no JavaScript dialog is open")
	}
	params := map[string]any{"accept": accept}
	if text != "" {
		params["promptText"] = text
	}
	if err := b.c.call(sess, "Page.handleJavaScriptDialog", params, nil, 0); err != nil {
		return "", err
	}
	return "Dialog (" + msg + ") " + map[bool]string{true: "accepted", false: "dismissed"}[accept] + ".", nil
}

// jsonString is a small helper to read an arbitrary JSON scalar as text.
func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// Eval runs a JavaScript expression in the current tab and returns its result as text
// (strings as they are, anything else as JSON). It is for Go callers that know the page
// they drive; it is deliberately not offered as a tool.
func (b *Browser) Eval(expr string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	params := map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}
	if err := b.pageCall("Runtime.evaluate", params, &out); err != nil {
		return "", err
	}
	if out.Exception != nil {
		msg := out.Exception.Exception.Description
		if msg == "" {
			msg = out.Exception.Text
		}
		return "", errors.New("javascript: " + msg)
	}
	if out.Result.Type == "undefined" {
		return "", nil
	}
	return jsonString(out.Result.Value), nil
}
