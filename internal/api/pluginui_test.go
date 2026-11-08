package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const frontManifest = `{
  "id": "acme.front", "name": "Front", "version": "1.0.0", "sdk": 1,
  "targets": { "server": { "main": "dist/server.js", "permissions": { "storage": true } } },
  "contributes": {
    "pages": [
      { "id": "main", "label": "Main", "ui": "dist/main.js", "rpc": ["add"] },
      { "id": "tree", "label": "Tree", "render": "renderTree" }
    ]
  }
}`

func frontFiles() map[string]string {
	return map[string]string{
		"plugin.json":    frontManifest,
		"dist/server.js": `module.exports = { rpc: { add: a => ({ sum: a.x + 1, project: a.projectId }), secret: () => "nope", renderTree: () => ({ type: "text", text: "hi" }) } };`,
		"dist/main.js":   `document.body.appendChild(document.createTextNode("PAGE-BUNDLE"));`,
		"dist/main.css":  `.a { color: red }`,
	}
}

type pageUI struct {
	Script string   `json:"script"`
	Style  string   `json:"style"`
	RPC    []string `json:"rpc"`
}

func TestPluginFrontendPage(t *testing.T) {
	r := newPluginRig(t)
	res := r.install(frontFiles())
	if !strings.Contains(strings.Join(res.Warnings, "\n"), `Its page "Main" draws itself`) || !strings.Contains(strings.Join(res.Warnings, "\n"), "only these calls: add") {
		t.Fatalf("the review dialog says a page runs in the browser: %v", res.Warnings)
	}

	if code, _, _ := r.do("", "POST", "/api/plugins/acme.front/pages/main/ui", map[string]any{"projectId": r.pid}); code != http.StatusUnauthorized {
		t.Fatalf("a ticket needs the token: %d", code)
	}
	var ui pageUI
	r.ok("user", "POST", "/api/plugins/acme.front/pages/main/ui", map[string]any{"projectId": r.pid}, &ui)
	if !strings.HasPrefix(ui.Script, "/api/plugin-ui/") || ui.Style != ".a { color: red }" || len(ui.RPC) != 1 || ui.RPC[0] != "add" {
		t.Fatalf("page ui: %+v", ui)
	}

	// the Worker loads it with the ticket alone, and only once
	code, body, hdr := r.do("", "GET", ui.Script, nil)
	if code != http.StatusOK {
		t.Fatalf("script: %d %s", code, body)
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": "default-src 'none'",
		"Content-Type":            "text/javascript; charset=utf-8",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
	} {
		if hdr.Get(k) != want {
			t.Errorf("%s: %q, want %q", k, hdr.Get(k), want)
		}
	}
	text := string(body)
	ctx, _ := json.Marshal(r.pid)
	if !strings.Contains(text, `"projectId":`+string(ctx)) || !strings.Contains(text, `"pluginId":"acme.front"`) || !strings.Contains(text, "PAGE-BUNDLE") {
		t.Fatalf("the script has the runtime, the context and the bundle:\n%.400s", text)
	}
	if strings.Index(text, "PAGE-BUNDLE") < strings.Index(text, `"pageId":"main"`) {
		t.Fatal("the runtime runs before the plugin's code")
	}
	if code, _, _ := r.do("", "GET", ui.Script, nil); code != http.StatusForbidden {
		t.Fatalf("a ticket works once: %d", code)
	}

	// a ticket is not a key to anything else
	var again pageUI
	r.ok("user", "POST", "/api/plugins/acme.front/pages/main/ui", map[string]any{"projectId": r.pid}, &again)
	tampered := again.Script[:len(again.Script)-2] + "xx"
	if code, _, _ := r.do("", "GET", tampered, nil); code != http.StatusForbidden {
		t.Fatalf("a tampered ticket: %d", code)
	}
	for _, path := range []string{
		"/api/plugin-ui/" + strings.TrimPrefix(again.Script, "/api/plugin-ui/") + "/../../plugins",
		"/api/plugin-ui/x%2F..%2F..%2Fplugins",
		"/api/plugin-ui/",
	} {
		if code, _, _ := r.do("", "GET", path, nil); code != http.StatusUnauthorized && code != http.StatusNotFound && code != http.StatusMovedPermanently {
			t.Errorf("%s: %d, want it refused without the token", path, code)
		}
	}
	req, _ := http.NewRequest("GET", r.srv.URL+again.Script, nil)
	req.Header.Set("X-Lucode-Remote", "other-instance")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a request for another instance needs the token: %d", resp.StatusCode)
	}

	// the page's rpcs, and nothing else
	code, out, raw := r.rpc("acme.front", "add", map[string]any{"x": 41})
	if code != 200 || out["sum"] != float64(42) || out["project"] != r.pid {
		t.Fatalf("add: %d %s", code, raw)
	}
	if code, _, raw := r.rpc("acme.front", "secret", nil); code != http.StatusForbidden {
		t.Fatalf("an rpc the page does not list: %d %s", code, raw)
	}

	// a view-tree page has no frontend, and a missing page is missing
	if code, _, _ := r.do("user", "POST", "/api/plugins/acme.front/pages/tree/ui", map[string]any{"projectId": r.pid}); code != http.StatusBadRequest {
		t.Fatalf("a view-tree page: %d", code)
	}
	if code, _, _ := r.do("user", "POST", "/api/plugins/acme.front/pages/nope/ui", map[string]any{"projectId": r.pid}); code != http.StatusNotFound {
		t.Fatalf("a missing page: %d", code)
	}

	// off means off, for a ticket minted before too
	var late pageUI
	r.ok("user", "POST", "/api/plugins/acme.front/pages/main/ui", map[string]any{"projectId": r.pid}, &late)
	r.ok("admin", "PUT", "/api/plugins/acme.front/enabled", map[string]any{"enabled": false}, nil)
	if code, _, _ := r.do("", "GET", late.Script, nil); code < 400 {
		t.Fatalf("the script of a plugin that is off: %d", code)
	}
}

func TestPluginUIListsFrontendPages(t *testing.T) {
	r := newPluginRig(t)
	r.install(frontFiles())
	var ui struct {
		Pages []map[string]any `json:"pages"`
	}
	r.ok("user", "GET", "/api/plugins/ui", nil, &ui)
	found := false
	for _, p := range ui.Pages {
		if p["pluginId"] == "acme.front" && p["id"] == "main" {
			found = true
			if p["ui"] != "dist/main.js" {
				t.Fatalf("the web needs to know the page has a frontend: %v", p)
			}
		}
	}
	if !found {
		t.Fatalf("no page: %v", ui.Pages)
	}
}
