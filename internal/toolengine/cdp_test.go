package toolengine

import (
	"encoding/json"
	"net"
	"net/url"
	"path/filepath"
	"testing"

	"claude-orchestrator/internal/cdp"
)

func TestCDPFromAUserTool(t *testing.T) {
	if _, err := cdp.FindEdge(); err != nil {
		t.Skip("Edge is not installed")
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json")) // never touch the real one
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Cleanup(func() { cdp.Default.Close() })

	page := `<title>t</title><label>City <input></label><button onclick="document.title='Hi '+document.querySelector('input').value">Go</button>`
	args, _ := json.Marshal(map[string]any{
		"port": port, "profile": t.TempDir(),
		"url": "data:text/html;charset=utf-8," + url.PathEscape(page),
	})
	code := `
		cdp.launch({mode: "headless", port: args.port, profileDir: args.profile, url: args.url});
		var box = cdp.find({role: "textbox", name: "city"})[0];
		cdp.type(box.ref, "Lima");
		cdp.click(cdp.find({role: "button", name: "go"})[0].ref);
		var title = cdp.info().title;
		cdp.close();
		return {title: title, found: box.name};
	`
	got, err := Execute(code, args, "")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := got.(map[string]any)
	if m["title"] != "Hi Lima" || m["found"] != "City" {
		t.Errorf("result = %v", got)
	}
}
