package cdp

import (
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

const testPage = `<!doctype html><title>Form test</title>
<h1>Account</h1>
<label>Your name <input id="n"></label>
<input id="nolabel" placeholder="hidden hint">
<label><input type="checkbox" id="agree"> Agree to terms</label>
<button id="save" onclick="document.getElementById('out').textContent='Saved '+document.getElementById('n').value">Save</button>
<button aria-label="Close dialog" onclick="alert('hello')">X</button>
<a href="#" onclick="document.title='Docs opened';return false">Docs</a>
<div id="out" role="status"></div>`

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func find1(t *testing.T, b *Browser, o FindOptions) Element {
	t.Helper()
	els, err := b.Find(o)
	if err != nil || len(els) == 0 {
		t.Fatalf("find %+v: %v %v", o, els, err)
	}
	return els[0]
}

func TestEdgeRoundTrip(t *testing.T) {
	if _, err := FindEdge(); err != nil {
		t.Skip("Edge is not installed")
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json"))
	b := &Browser{}
	port := freePort(t)
	msg, err := b.Launch(LaunchOptions{Headless: true, Port: port, ProfileDir: t.TempDir(), URL: "data:text/html;charset=utf-8," + url.PathEscape(testPage)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	t.Log(msg)

	// the accessibility tree names things the way a user would
	els, total, page, err := b.List(ListOptions{})
	if err != nil || total == 0 {
		t.Fatalf("list: %v %v", els, err)
	}
	t.Logf("page %+v\n%s", page, FormatElements(els, false))
	if page.Title != "Form test" {
		t.Errorf("title = %q", page.Title)
	}
	for _, want := range []string{`textbox "Your name"`, `button "Save"`, `link "Docs"`, `checkbox "Agree to terms"`, `button "Close dialog"`} {
		if !strings.Contains(FormatElements(els, false), want) {
			t.Errorf("list is missing %s", want)
		}
	}

	// type by role + accessible name, then click a button found by its name
	name := find1(t, b, FindOptions{Role: "textbox", Name: "your name"})
	if _, err := b.Type(TypeOptions{Ref: name.Ref, Text: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Click(ClickOptions{Ref: find1(t, b, FindOptions{Role: "button", Name: "Save"}).Ref}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Text("", 0); !strings.Contains(got, "Saved Ada") {
		t.Errorf("page text after clicking Save: %q", got)
	}
	// replacing the content
	if _, err := b.Type(TypeOptions{Ref: name.Ref, Text: "Grace", Clear: true}); err != nil {
		t.Fatal(err)
	}
	if v := find1(t, b, FindOptions{Role: "textbox", Name: "your name"}).Value; v != "Grace" {
		t.Errorf("value after clear+type = %q", v)
	}

	// checkbox state shows in the tree
	if _, err := b.Click(ClickOptions{Ref: find1(t, b, FindOptions{Role: "checkbox"}).Ref}); err != nil {
		t.Fatal(err)
	}
	if e := find1(t, b, FindOptions{Role: "checkbox"}); !strings.Contains(strings.Join(e.States, ","), "checked") {
		t.Errorf("checkbox states = %v", e.States)
	}

	// text search and the CSS fallback for an element with no accessible name
	if e := find1(t, b, FindOptions{Text: "agree"}); e.Role != "checkbox" {
		t.Errorf("text search should land on the interactive element, got %+v", e)
	}
	css := find1(t, b, FindOptions{Selector: "#nolabel"})
	t.Logf("css fallback: %+v", css)
	if css.Ref == "" {
		t.Error("css fallback returned no ref")
	}

	// keyboard and raw mouse
	if _, err := b.Press("Tab"); err != nil {
		t.Fatal(err)
	}
	x, y := 5.0, 5.0
	if _, err := b.Mouse(MouseOptions{Action: "move", X: &x, Y: &y}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Mouse(MouseOptions{Action: "click", Ref: find1(t, b, FindOptions{Role: "link", Name: "Docs"}).Ref}); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.Info(); p.Title != "Docs opened" {
		t.Errorf("clicking the link should run its handler, title = %q", p.Title)
	}

	// a JavaScript dialog must not hang the tool
	res, err := b.Click(ClickOptions{Ref: find1(t, b, FindOptions{Role: "button", Name: "Close dialog"}).Ref})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dialog == "" {
		t.Errorf("expected the alert to be reported: %+v", res)
	}
	if _, err := b.Dialog(true, ""); err != nil {
		t.Fatal(err)
	}

	// tabs
	first, _ := b.Tabs()
	if _, err := b.NewTab("data:text/html,<title>second</title>second"); err != nil {
		t.Fatal(err)
	}
	tabs, _ := b.Tabs()
	if len(tabs) != len(first)+1 {
		t.Fatalf("tabs = %+v", tabs)
	}
	var second string
	for _, tb := range tabs {
		if tb.Title == "second" {
			second = tb.ID
		}
	}
	if _, err := b.SwitchTab(first[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := b.CloseTab(second); err != nil {
		t.Fatal(err)
	}

	if png, err := b.Screenshot(false); err != nil || len(png) < 100 {
		t.Errorf("screenshot: %d bytes, %v", len(png), err)
	}
	if _, err := b.Navigate("data:text/html,<title>other</title>"); err != nil {
		t.Fatal(err)
	}
	if p, err := b.History("back"); err != nil || p.Title != "Form test" {
		t.Errorf("back: %+v %v", p, err)
	}

	b.Close()
	if _, err := wsEndpoint(port); err == nil {
		t.Error("the debugging port should be closed after Close")
	}
}

// A restart of lucode loses the Browser in memory; the state file brings the Edge back,
// even on a custom port, and lets browser_close end it.
func TestReconnectAfterARestart(t *testing.T) {
	if _, err := FindEdge(); err != nil {
		t.Skip("Edge is not installed")
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json"))
	port := freePort(t)
	first := &Browser{}
	if _, err := first.Launch(LaunchOptions{Headless: true, Port: port, ProfileDir: t.TempDir(), URL: "data:text/html,<title>kept</title>"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	first.resetLocked() // what a restart does: the connection and the handle are gone, Edge keeps running

	second := &Browser{} // a new process knows nothing, not even the port
	msg, err := second.Launch(LaunchOptions{Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Reconnected") {
		t.Errorf("want a reconnection, got %q", msg)
	}
	if tabs, err := second.Tabs(); err != nil || len(tabs) == 0 || tabs[0].Title != "kept" {
		t.Errorf("tabs after reconnecting: %+v %v", tabs, err)
	}
	// browser tools work without calling launch again
	third := &Browser{}
	if _, err := third.Info(); err != nil {
		t.Errorf("a tool should find the Edge by itself: %v", err)
	}
	third.resetLocked()

	if got := second.Close(); got != "Edge closed." {
		t.Errorf("close = %q", got)
	}
	if _, err := wsEndpoint(port); err == nil {
		t.Error("the Edge from the earlier run should be gone")
	}
	if _, ok := loadState(); ok {
		t.Error("the state file should be removed")
	}
}
