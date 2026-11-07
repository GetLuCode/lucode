package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	pluginhub "claude-orchestrator/internal/plugins/hub"
	sessionpkg "claude-orchestrator/internal/session"
)

// The plugin routes against the real host, a temp database and the real auth middleware. Plugin
// code really runs. Nothing here reaches claude, Google or the network.

const helloManifest = `{
  "id":"acme.hello","name":"Hello","version":"1.0.0","sdk":1,
  "description":"d","icon":"assets/icon.svg",
  "main":"main.js",
  "permissions":{"storage":true,"conversations":true,"sessions":"read","board":"write","flows":true},
  "contributes":{
    "models":[{"id":"echo","label":"Echo","accent":"#4285f4","images":true,"timeoutSeconds":30}],
    "conversations":[{"id":"chats","model":"echo","title":"Chats"}],
    "pages":[{"id":"main","label":"Main","render":"renderMain"}],
    "settings":[{"key":"token","label":"Token","type":"secret"},{"key":"color","label":"Color","type":"select","options":["red","blue"],"default":"red"},{"key":"limit","label":"Limit","type":"number"}],
    "blueprints":[{"key":"bp","name":"Hello flow","file":"blueprints/bp.json"}],
    "flowNodes":[{"id":"ask","label":"Ask","fields":[{"name":"q","label":"Q","kind":"template","required":true}],"ports":["next","failed"],"timeoutSeconds":20}],
    "chatActions":[{"id":"sum","label":"Sum","rpc":"summarize"}]
  }
}`

const helloCode = `
module.exports = {
  models: { echo: { turn(req) {
    lucode.context.progress("thinking");
    if (req.message === "boom") throw new Error("it broke");
    if (req.message === "slow") { lucode.time.sleep(60000); return "late" }
    if (req.message === "hang") { for(;;){} }
    lucode.conversations.save({sessionId: req.sessionId, title: req.message, turns: 1});
    return "echo: " + req.message + (req.image ? " +image" : "");
  } } },
  nodes: { ask(input) {
    if (input.config.q === "fail") return { port: "failed", output: { error: "asked to fail" } };
    if (input.config.q === "throw") throw new Error("thrown");
    return { port: "next", output: { answer: "got " + input.config.q + " on " + input.card.title } };
  } },
  rpc: {
    renderMain(a) { return { type: "stack", children: [{ type: "button", label: "x", action: { rpc: "fromView", args: {} } }], projectId: a.projectId } },
    fromView() { return { toast: "clicked" } },
    summarize() { return { ok: true } },
    hidden() { return { secret: lucode.settings.get("token") } },
    leak() { lucode.log.info("token is", lucode.settings.get("token")); return { value: lucode.settings.get("token") } },
    storageRoundTrip(a) { lucode.storage.set(a.key, a.value); return { value: lucode.storage.get(a.key), keys: lucode.storage.list("") } },
    bigStorage() { var s = "x".repeat(4 << 20); for (var i = 0; i < 5; i++) lucode.storage.set("k" + i, s); return "fit" },
    board(a) {
      var lists = lucode.board.listLists();
      var c = lucode.board.createCard({ title: "from plugin" });
      lucode.board.comment(c.id, "hi");
      lucode.board.addTag(c.id, "plugin");
      var mv = lucode.board.moveCard(c.id, { toKind: "review" });
      return { lists: lists.length, card: lucode.board.getCard(c.id).title, moved: mv.moved, id: c.id };
    },
    otherProject(a) { return lucode.board.getCard(a.cardId) },
    otherSession(a) { return lucode.sessions.get(a.sessionId) },
    emit(a) { lucode.flows.emit("done", { x: a.x }); return "sent" },
  },
  events: {
    installed(e) { lucode.storage.set("installed", e.version) },
    upgraded(e) { lucode.storage.set("upgraded", e.version) },
    cardMoved(e) { lucode.storage.set("moved", e.to_kind) },
    sessionDeleted(e) { lucode.storage.set("deleted", e.sessionId) },
  },
};`

const helloBlueprint = `{"nodes":[
  {"id":"t","type":"trigger.manual","x":0,"y":0,"config":{}},
  {"id":"a","type":"plugin.acme.hello.ask","x":100,"y":0,"config":{"q":"bp"}}
],"edges":[{"from":"t","port":"next","to":"a"}]}`

func pluginZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		out, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(out, body)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func helloFiles() map[string]string {
	return map[string]string{
		"plugin.json": helloManifest, "main.js": helloCode, "blueprints/bp.json": helloBlueprint,
		"assets/icon.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`, "assets/pic.png": "not really a png", "data/a.json": `{"a":1}`,
	}
}

type pluginRig struct {
	t      *testing.T
	h      *Handler
	srv    *httptest.Server
	pid    string
	lists  map[string]dbpkg.KanbanList
	browse *fakeBrowser
}

// fakeBrowser stands in for Edge.
type fakeBrowser struct {
	url    string
	evals  []string
	closed int
}

func (f *fakeBrowser) Launch(bool) (string, error)   { return "started", nil }
func (f *fakeBrowser) Close() string                 { f.closed++; return "closed" }
func (f *fakeBrowser) Status() string                { return "ok" }
func (f *fakeBrowser) Tabs() (any, error)            { return []any{}, nil }
func (f *fakeBrowser) NewTab(u string) (any, error)  { f.url = u; return map[string]any{"url": u}, nil }
func (f *fakeBrowser) SwitchTab(string) (any, error) { return nil, nil }
func (f *fakeBrowser) CloseTab(string) error         { return nil }
func (f *fakeBrowser) URL() (string, error)          { return f.url, nil }
func (f *fakeBrowser) Info() (any, error)            { return map[string]any{"url": f.url}, nil }
func (f *fakeBrowser) Navigate(u string) (any, error) {
	f.url = u
	return map[string]any{"url": u}, nil
}
func (f *fakeBrowser) Find(string, string, string, bool, int) (any, error) {
	return []any{}, nil
}
func (f *fakeBrowser) Click(string) (any, error)                    { return "clicked", nil }
func (f *fakeBrowser) Type(string, string, bool, bool) (any, error) { return "typed", nil }
func (f *fakeBrowser) Press(string) (any, error)                    { return "pressed", nil }
func (f *fakeBrowser) Eval(js string) (string, error) {
	f.evals = append(f.evals, js)
	return "evaluated", nil
}
func (f *fakeBrowser) Text(string, int) (string, error) { return "text", nil }

func fakeAuth(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token  string `json:"token"`
			APIKey string `json:"api_key"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case body.Token == "admin":
			json.NewEncoder(w).Encode(map[string]any{"UserID": "a", "IsAdmin": true})
		case body.Token == "user":
			json.NewEncoder(w).Encode(map[string]any{"UserID": "u", "IsAdmin": false})
		case body.APIKey == "adminkey":
			json.NewEncoder(w).Encode(map[string]any{"UserID": "a", "IsAdmin": true})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newPluginRig(t *testing.T, opts ...func(*plugins.HubDeps)) *pluginRig {
	t.Helper()
	h, pid, lists := newTaskTestHandler(t)
	bus := natspkg.NewBus(nil)
	h.bus = bus
	h.manager = sessionpkg.NewManager(h.db, bus, "", "")
	h.flows = flows.NewEngine(h.db, flowHost{h})
	h.flows.TurnEndGrace = 0
	h.flows.EventTap = h.pluginEventTap
	h.SetAuthURL(fakeAuth(t))
	r := &pluginRig{t: t, h: h, pid: pid, lists: lists, browse: &fakeBrowser{}}
	deps := plugins.HubDeps{
		DataDir: t.TempDir(),
		Browser: func(string) plugins.BrowserDriver { return r.browse },
		Bundled: fstest.MapFS{
			"bundled/plugin.json": {Data: []byte(`{"id":"lucode.sample","name":"Sample","version":"1.0.0","sdk":1,"description":"s","main":"main.js"}`)},
			"bundled/main.js":     {Data: []byte(`module.exports={}`)},
		},
		AutoInstall: nil,
	}
	for _, o := range opts {
		o(&deps)
	}
	if err := h.startPluginHost(context.Background(), deps, pluginhub.New); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flows.SetPluginSource(nil) })
	r.srv = httptest.NewServer(Wrap(h))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *pluginRig) do(token, method, path string, body any) (int, []byte, http.Header) {
	r.t.Helper()
	var rd io.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if token == "key" {
		req.Header.Set("X-API-Key", "adminkey")
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

func (r *pluginRig) ok(token, method, path string, body any, out any) {
	r.t.Helper()
	code, raw, _ := r.do(token, method, path, body)
	if code >= 300 {
		r.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			r.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
}

func (r *pluginRig) inspect(token string, zipBytes []byte) (int, []byte) {
	r.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "p.zip")
	part.Write(zipBytes)
	w.Close()
	req, _ := http.NewRequest("POST", r.srv.URL+"/api/plugins/inspect", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token == "key" {
		req.Header.Set("X-API-Key", "adminkey")
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (r *pluginRig) install(files map[string]string) plugins.InspectResult {
	r.t.Helper()
	code, raw := r.inspect("admin", pluginZip(r.t, files))
	if code != 200 {
		r.t.Fatalf("inspect: %d %s", code, raw)
	}
	var res plugins.InspectResult
	json.Unmarshal(raw, &res)
	keys := []string{}
	for _, p := range res.Permissions {
		keys = append(keys, p.Key)
	}
	r.ok("admin", "POST", "/api/plugins/install", map[string]any{"token": res.Token, "granted": keys}, nil)
	return res
}

func (r *pluginRig) rpc(id, name string, args any) (int, map[string]any, string) {
	r.t.Helper()
	code, raw, _ := r.do("user", "POST", "/api/plugins/"+id+"/rpc/"+name, map[string]any{"projectId": r.pid, "args": args})
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return code, out, string(raw)
}

// allowRPC makes an rpc callable the way a page does: a view tree offers it.
func (r *pluginRig) allowRPC(names ...string) {
	r.t.Helper()
	for _, n := range names {
		pluginhub.AllowRPCForTest(r.h.plugins, "acme.hello", n)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Every admin route refuses a plain user, an API key and a request over NATS.
func TestAdminRoutes(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	routes := []struct{ method, path string }{
		{"POST", "/api/plugins/inspect"},
		{"POST", "/api/plugins/install"},
		{"POST", "/api/plugins/catalog/lucode.sample/install"},
		{"PUT", "/api/plugins/acme.hello/enabled"},
		{"DELETE", "/api/plugins/acme.hello"},
		{"PUT", "/api/plugins/acme.hello/settings"},
		{"GET", "/api/plugins/acme.hello/logs"},
		{"POST", "/api/plugins/runners/some-runner/pair"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if code, _, _ := r.do("", rt.method, rt.path, map[string]any{}); code != 401 {
				t.Errorf("no token: %d", code)
			}
			if code, _, _ := r.do("user", rt.method, rt.path, map[string]any{}); code != 403 {
				t.Errorf("a plain user: %d", code)
			}
			if code, _, _ := r.do("key", rt.method, rt.path, map[string]any{}); code != 403 {
				t.Errorf("an API key, even an admin's: %d", code)
			}
			// over NATS auth lets it through: the route itself has to refuse
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
			req = req.WithContext(natspkg.WithRPC(context.Background()))
			rec := httptest.NewRecorder()
			Wrap(r.h).ServeHTTP(rec, req)
			if rec.Code != 403 {
				t.Errorf("over NATS: %d %s", rec.Code, rec.Body)
			}
		})
	}
	// and a browser call over NATS runs no plugin code either
	req := httptest.NewRequest("POST", "/api/plugins/acme.hello/rpc/renderMain", strings.NewReader("{}"))
	req = req.WithContext(natspkg.WithRPC(context.Background()))
	rec := httptest.NewRecorder()
	Wrap(r.h).ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("rpc over NATS: %d", rec.Code)
	}
	// the routes every signed-in user has
	for _, p := range []string{"/api/plugins", "/api/plugins/ui", "/api/plugins/catalog", "/api/plugins/acme.hello/settings"} {
		if code, _, _ := r.do("user", "GET", p, nil); code != 200 {
			t.Errorf("GET %s as a user: %d", p, code)
		}
	}
	// other routes behave as before, with an API key too
	if code, _, _ := r.do("key", "GET", "/api/flow-node-types", nil); code != 200 {
		t.Errorf("an API key on another route: %d", code)
	}
}

func TestInstallFlow(t *testing.T) {
	r := newPluginRig(t)
	code, raw := r.inspect("admin", pluginZip(t, helloFiles()))
	if code != 200 {
		t.Fatalf("inspect: %d %s", code, raw)
	}
	var res plugins.InspectResult
	json.Unmarshal(raw, &res)
	if res.Token == "" || res.UpgradeFrom != "" || len(res.Permissions) == 0 {
		t.Fatalf("%+v", res)
	}
	for _, p := range res.Permissions {
		if p.Target != plugins.TargetServer {
			t.Fatalf("permission target: %+v", p)
		}
	}
	// inspecting installs nothing
	var list []plugins.PluginView
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 0 {
		t.Fatalf("inspect installed something: %+v", list)
	}
	// the grant has to cover everything, and the token dies with the attempt
	code, raw, _ = r.do("admin", "POST", "/api/plugins/install", map[string]any{"token": res.Token, "granted": []string{"server.storage"}})
	if code != 400 || !strings.Contains(string(raw), "was not granted") {
		t.Fatalf("a partial grant: %d %s", code, raw)
	}
	keys := []string{}
	for _, p := range res.Permissions {
		keys = append(keys, p.Key)
	}
	code, raw, _ = r.do("admin", "POST", "/api/plugins/install", map[string]any{"token": res.Token, "granted": keys})
	if code != 400 || !strings.Contains(string(raw), "already used") {
		t.Fatalf("a token used twice: %d %s", code, raw)
	}
	code, raw, _ = r.do("admin", "POST", "/api/plugins/install", map[string]any{"token": "nope", "granted": keys})
	if code != 400 {
		t.Fatalf("an invented token: %d %s", code, raw)
	}
	r.install(helloFiles())
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 1 || list[0].ID != "acme.hello" || !list[0].Enabled || list[0].Status != "enabled" || list[0].Source != "upload" || list[0].Targets["server"] == nil {
		t.Fatalf("%+v", list)
	}
	waitFor(t, "the installed event", func() bool {
		v, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.hello", "installed")
		return ok && v == `"1.0.0"`
	})
	// the web's merged list
	var ui plugins.UIContributions
	r.ok("user", "GET", "/api/plugins/ui", nil, &ui)
	if len(ui.Models) != 1 || ui.Models[0]["pluginId"] != "acme.hello" || ui.Models[0]["id"] != "echo" || len(ui.Pages) != 1 || len(ui.ChatActions) != 1 {
		t.Fatalf("%+v", ui)
	}

	// the reserved prefix belongs to the binary
	bad := helloFiles()
	bad["plugin.json"] = strings.Replace(helloManifest, "acme.hello", "lucode.hello", 1)
	if code, raw := r.inspect("admin", pluginZip(t, bad)); code != 400 || !strings.Contains(string(raw), "reserved") {
		t.Fatalf("a reserved id: %d %s", code, raw)
	}
	// a bad zip, as far as the API goes
	if code, _ := r.inspect("admin", []byte("not a zip")); code != 400 {
		t.Fatalf("garbage: %d", code)
	}
	// the catalog installs what ships in the binary, through the same pipeline
	var cat []plugins.CatalogEntry
	r.ok("user", "GET", "/api/plugins/catalog", nil, &cat)
	if len(cat) != 1 || cat[0].ID != "lucode.sample" || cat[0].Installed {
		t.Fatalf("catalog: %+v", cat)
	}
	r.ok("admin", "POST", "/api/plugins/catalog/lucode.sample/install", nil, nil)
	r.ok("user", "GET", "/api/plugins/catalog", nil, &cat)
	if !cat[0].Installed {
		t.Fatalf("catalog: %+v", cat)
	}
	if code, _, _ := r.do("admin", "POST", "/api/plugins/catalog/lucode.nope/install", nil); code != 404 {
		t.Fatalf("an unknown one: %d", code)
	}
}

func TestBundledIsInstalledOnce(t *testing.T) {
	r := newPluginRig(t, func(d *plugins.HubDeps) { d.AutoInstall = []string{"lucode.sample"} })
	var list []plugins.PluginView
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 1 || list[0].Source != "bundled" {
		t.Fatalf("%+v", list)
	}
	r.ok("admin", "DELETE", "/api/plugins/lucode.sample", nil, nil)
	if first, err := dbpkg.ClaimPluginSeed(r.h.db, "lucode.sample"); err != nil || first {
		t.Fatalf("a removed one stays removed: first=%v %v", first, err)
	}
}

func TestUpgradeAskingForMoreNeedsAGrant(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	next := helloFiles()
	next["plugin.json"] = strings.Replace(strings.Replace(helloManifest, `"version":"1.0.0"`, `"version":"1.1.0"`, 1),
		`"flows":true}`, `"flows":true,"http":{"hosts":["api.example.com"]}}`, 1)
	code, raw := r.inspect("admin", pluginZip(t, next))
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var res plugins.InspectResult
	json.Unmarshal(raw, &res)
	if res.UpgradeFrom != "1.0.0" || len(res.NewPermissions) != 1 || res.NewPermissions[0] != "server.http:api.example.com" {
		t.Fatalf("%+v", res)
	}
	old := []string{}
	for _, p := range res.Permissions {
		if p.Key != "server.http:api.example.com" {
			old = append(old, p.Key)
		}
	}
	code, raw2, _ := r.do("admin", "POST", "/api/plugins/install", map[string]any{"token": res.Token, "granted": old})
	if code != 400 || !strings.Contains(string(raw2), "server.http:api.example.com") {
		t.Fatalf("the old grant must not cover the new permission: %d %s", code, raw2)
	}
	var list []plugins.PluginView
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if list[0].Version != "1.0.0" {
		t.Fatalf("a refused upgrade changed the plugin: %+v", list[0])
	}
	r.install(next)
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if list[0].Version != "1.1.0" {
		t.Fatalf("%+v", list[0])
	}
	waitFor(t, "the upgraded event", func() bool {
		v, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.hello", "upgraded")
		return ok && v == `"1.1.0"`
	})
}

func TestRPCAllowlist(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	if code, _, raw := r.rpc("acme.hello", "hidden", nil); code != 403 {
		t.Fatalf("an rpc nothing offers: %d %s", code, raw)
	}
	if code, _, raw := r.rpc("acme.hello", "fromView", nil); code != 403 {
		t.Fatalf("before a view offered it: %d %s", code, raw)
	}
	code, tree, raw := r.rpc("acme.hello", "renderMain", nil)
	if code != 200 || tree["type"] != "stack" || tree["projectId"] != r.pid {
		t.Fatalf("a page's render: %d %s", code, raw)
	}
	if code, out, raw := r.rpc("acme.hello", "fromView", nil); code != 200 || out["toast"] != "clicked" {
		t.Fatalf("once a view offered it: %d %s", code, raw)
	}
	if code, _, raw := r.rpc("acme.hello", "summarize", nil); code != 200 {
		t.Fatalf("a chat action: %d %s", code, raw)
	}
	if code, _, _ := r.rpc("acme.nope", "x", nil); code == 200 {
		t.Fatal("a plugin that does not exist")
	}
	if code, _, _ := r.do("user", "POST", "/api/plugins/acme.hello/rpc/renderMain", map[string]any{"projectId": "nope"}); code != 404 {
		t.Fatalf("a project that does not exist: %d", code)
	}
}

func TestSecretsStayOnTheHub(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	const secret = "hunter2-very-secret"
	r.ok("admin", "PUT", "/api/plugins/acme.hello/settings", map[string]any{"token": secret, "color": "blue", "limit": 3}, nil)
	code, raw, _ := r.do("user", "GET", "/api/plugins/acme.hello/settings", nil)
	if code != 200 || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"token":{"set":true}`) || !strings.Contains(string(raw), `"color":"blue"`) {
		t.Fatalf("settings as the browser sees them: %s", raw)
	}
	// the plugin reads it, and what it tries to hand to the browser or the log is cleaned
	r.allowRPC("leak")
	code, out, rawRPC := r.rpc("acme.hello", "leak", nil)
	if code != 200 || strings.Contains(rawRPC, secret) || out["value"] != "***" {
		t.Fatalf("a secret on its way to the browser: %d %s", code, rawRPC)
	}
	code, logs, _ := r.do("admin", "GET", "/api/plugins/acme.hello/logs", nil)
	if code != 200 || strings.Contains(string(logs), secret) || !strings.Contains(string(logs), "token is") {
		t.Fatalf("logs: %d %s", code, logs)
	}
	// sent back as the browser got it, it stays; empty, it goes
	r.ok("admin", "PUT", "/api/plugins/acme.hello/settings", map[string]any{"token": map[string]any{"set": true}}, nil)
	stored, _ := dbpkg.PluginSettings(r.h.db, "acme.hello")
	if !strings.Contains(stored["token"], secret) {
		t.Fatalf("stored: %v", stored)
	}
	r.ok("admin", "PUT", "/api/plugins/acme.hello/settings", map[string]any{"token": ""}, nil)
	_, raw, _ = r.do("user", "GET", "/api/plugins/acme.hello/settings", nil)
	if !strings.Contains(string(raw), `"token":{"set":false}`) {
		t.Fatalf("cleared: %s", raw)
	}
	for name, body := range map[string]map[string]any{
		"unknown key": {"nope": 1}, "wrong type": {"limit": "x"}, "not an option": {"color": "green"},
	} {
		if code, _, _ := r.do("admin", "PUT", "/api/plugins/acme.hello/settings", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
}

func TestAssetsAreImagesOnly(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	code, raw, hdr := r.do("user", "GET", "/api/plugins/acme.hello/assets/assets/icon.svg", nil)
	if code != 200 || !strings.Contains(string(raw), "svg") {
		t.Fatalf("%d %s", code, raw)
	}
	if hdr.Get("Content-Type") != "image/svg+xml" || hdr.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("headers: %+v", hdr)
	}
	for _, p := range []string{"main.js", "plugin.json", "data/a.json", "assets/../main.js", "../../plugin.json", "assets/nope.png"} {
		if code, _, _ := r.do("user", "GET", "/api/plugins/acme.hello/assets/"+p, nil); code == 200 {
			t.Errorf("%s was served", p)
		}
	}
	if code, _, _ := r.do("", "GET", "/api/plugins/acme.hello/assets/assets/icon.svg", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

// A second plugin cannot reach the first one's data, whatever it names.
func TestPluginsAreIsolated(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	other := map[string]string{
		"plugin.json": `{"id":"acme.other","name":"O","version":"1.0.0","sdk":1,"main":"main.js","permissions":{"storage":true},
			"contributes":{"chatActions":[{"id":"x","label":"x","rpc":"read"}]}}`,
		"main.js": `module.exports={rpc:{read(){ return {
			own: lucode.storage.get("shared"),
			settings: lucode.settings.all(),
			keys: lucode.storage.list(""),
			plugin: lucode.plugin.id,
		} }}}`,
	}
	r.install(other)
	r.allowRPC("storageRoundTrip")
	if code, _, raw := r.rpc("acme.hello", "storageRoundTrip", map[string]any{"key": "shared", "value": "first's"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	r.ok("admin", "PUT", "/api/plugins/acme.hello/settings", map[string]any{"token": "the-first-ones-secret"}, nil)
	code, out, raw := r.rpc("acme.other", "read", nil)
	if code != 200 || out["own"] != nil || out["plugin"] != "acme.other" || strings.Contains(raw, "secret") || strings.Contains(raw, "shared") {
		t.Fatalf("%d %s", code, raw)
	}
	// without the permission the function throws
	plain := map[string]string{
		"plugin.json": `{"id":"acme.plain","name":"P","version":"1.0.0","sdk":1,"main":"main.js",
			"contributes":{"chatActions":[{"id":"x","label":"x","rpc":"go"}]}}`,
		"main.js": `module.exports={rpc:{go(a){ return lucode[a.mod][a.fn](a.arg) }}}`,
	}
	r.install(plain)
	for _, c := range []struct{ mod, fn string }{
		{"storage", "get"}, {"conversations", "list"}, {"sessions", "get"}, {"board", "listLists"},
		{"flows", "emit"}, {"browser", "launch"}, {"http", "fetch"},
	} {
		code, _, raw := r.rpc("acme.plain", "go", map[string]any{"mod": c.mod, "fn": c.fn, "arg": "x"})
		if code != 422 || !strings.Contains(raw, "permission") {
			t.Errorf("%s.%s without its permission: %d %s", c.mod, c.fn, code, raw)
		}
	}
}

func TestStorageQuota(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	r.allowRPC("bigStorage")
	code, _, raw := r.rpc("acme.hello", "bigStorage", nil)
	if code != 422 || !strings.Contains(raw, "full") {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestBoardGoesThroughTheBoard(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	r.allowRPC("board", "otherProject", "otherSession")
	code, out, raw := r.rpc("acme.hello", "board", nil)
	if code != 200 || out["moved"] != true || out["card"] != "from plugin" {
		t.Fatalf("%d %s", code, raw)
	}
	id := out["id"].(string)
	card, _ := dbpkg.GetKanbanCard(r.h.db, id)
	if card.ListID != r.lists["review"].ID || len(card.Tags) != 1 {
		t.Fatalf("%+v", card)
	}
	comments, _ := dbpkg.ListCardComments(r.h.db, id)
	if len(comments) != 1 || comments[0].Author != "Hello" {
		t.Fatalf("%+v", comments)
	}
	// the move is logged like any other, and says who did it
	var src string
	if err := r.h.db.QueryRow(`SELECT source FROM flow_events WHERE type=? AND card_id=?`, dbpkg.EventCardMoved, id).Scan(&src); err != nil || src != "plugin:acme.hello" {
		t.Fatalf("event source %q %v", src, err)
	}

	// another project's card or session does not exist as far as the plugin knows
	foreign := dbpkg.Project{ID: "p-other", Name: "other", WorkDirs: []string{"."}, DefaultModel: "haiku"}
	dbpkg.InsertProject(r.h.db, foreign)
	dbpkg.EnsureDefaultKanbanLists(r.h.db, foreign.ID)
	lists, _ := dbpkg.ListKanbanLists(r.h.db, foreign.ID)
	dbpkg.InsertKanbanCard(r.h.db, dbpkg.KanbanCard{ID: "foreign", ProjectID: foreign.ID, ListID: lists[0].ID, Title: "x", CardType: "task", Tags: []string{}, DependsOn: []string{}})
	dbpkg.InsertSession(r.h.db, dbpkg.Session{ID: "fs", ProjectID: foreign.ID, Name: "n", Model: "m", Status: "idle"})
	if code, _, raw := r.rpc("acme.hello", "otherProject", map[string]any{"cardId": "foreign"}); code != 422 || !strings.Contains(raw, "no card") {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, raw := r.rpc("acme.hello", "otherSession", map[string]any{"sessionId": "fs"}); code != 422 || !strings.Contains(raw, "no session") {
		t.Fatalf("%d %s", code, raw)
	}
}

// A page can go anywhere by itself, so what it shows is only for pages on an allowed origin.
func TestBrowserPageOperationsNeedAnAllowedOrigin(t *testing.T) {
	r := newPluginRig(t)
	files := map[string]string{
		"plugin.json": `{"id":"acme.page","name":"P","version":"1.0.0","sdk":1,"main":"main.js",
			"permissions":{"browser":{"origins":["https://www.example.com"]}},
			"contributes":{"chatActions":[{"id":"x","label":"x","rpc":"go"}]}}`,
		"main.js": `module.exports={rpc:{go(a){
			var b=lucode.browser;
			switch(a.op){
				case "find": return b.find({role:"button"});
				case "click": return b.click("e1");
				case "type": return b.type("e1","x");
				case "press": return b.press("Enter");
				case "text": return b.text();
				case "eval": return b.evalPage("1");
				case "info": return b.info();
				case "tabs": return b.tabs();
				case "nav": return b.navigate(a.url);
				case "newTab": return b.newTab(a.url);
			}
		}}}`,
	}
	res := r.install(files)
	for _, p := range res.Permissions {
		if p.Key == "server.browser:https://www.example.com" && p.Risk != "high" {
			t.Fatalf("a browser permission is high risk: %+v", p)
		}
	}
	ops := []string{"find", "click", "type", "press", "text", "eval"}
	for _, page := range []string{"https://evil.example.org/x", "http://192.168.1.1/admin", "about:blank", "https://www.example.com.evil.org/", "file:///c:/secret"} {
		r.browse.url = page
		for _, op := range ops {
			code, _, raw := r.rpc("acme.page", "go", map[string]any{"op": op})
			if code == 200 || !strings.Contains(raw, "not an origin") {
				t.Errorf("%s on %s: %d %s", op, page, code, raw)
			}
			if strings.Contains(raw, "/admin") || strings.Contains(raw, "/x") {
				t.Errorf("%s: the page's address leaked: %s", op, raw)
			}
		}
	}
	if len(r.browse.evals) != 0 {
		t.Fatalf("code ran: %v", r.browse.evals)
	}
	// where the browser is can still be asked, and an empty tab can be opened and left
	for _, c := range []map[string]any{{"op": "info"}, {"op": "tabs"}, {"op": "nav", "url": "about:blank"}, {"op": "newTab", "url": "about:blank"}} {
		if code, _, raw := r.rpc("acme.page", "go", c); code != 200 {
			t.Errorf("%v: %d %s", c, code, raw)
		}
	}
	r.browse.url = "https://www.example.com/page"
	for _, op := range ops {
		if code, _, raw := r.rpc("acme.page", "go", map[string]any{"op": op}); code != 200 {
			t.Errorf("%s on the allowed page: %d %s", op, code, raw)
		}
	}
}

// A plugin's model and a plugin's flows run plugin code, and NATS proves nothing about who asks.
func TestRPCCannotRunPluginCode(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	over := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(natspkg.WithRPC(context.Background()))
		rec := httptest.NewRecorder()
		Wrap(r.h).ServeHTTP(rec, req)
		return rec
	}
	sess := dbpkg.Session{ID: "plain", ProjectID: r.pid, Name: "n", Model: "plugin:acme.hello/echo", Status: "idle"}
	orch := dbpkg.Session{ID: "orch", ProjectID: r.pid, Name: "n", Model: "local:aimode", Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
	dbpkg.InsertSession(r.h.db, sess)
	dbpkg.InsertSession(r.h.db, orch)
	for name, c := range map[string]struct{ id, body string }{
		"a plugin model named":        {"plain", `{"message":"x","model":"plugin:acme.hello/echo"}`},
		"a session's plugin model":    {"plain", `{"message":"x"}`},
		"the old id":                  {"plain", `{"message":"x","model":"local:aimode"}`},
		"an orchestrator chat":        {"orch", `{"message":"x","model":"plugin:acme.hello/echo"}`},
		"an orchestrator's saved one": {"orch", `{"message":"x"}`},
	} {
		if rec := over("POST", "/api/sessions/"+c.id+"/turn", c.body); rec.Code != 403 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	msgs, _ := dbpkg.ListMessages(r.h.db, "plain")
	if len(msgs) != 0 {
		t.Fatalf("a refused turn left messages: %+v", msgs)
	}

	var list []dbpkg.Flow
	r.ok("user", "GET", "/api/projects/"+r.pid+"/flows", nil, &list)
	var locked dbpkg.Flow
	for _, f := range list {
		if f.Locked {
			locked = f
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/flows/" + locked.ID + "/enabled", `{"enabled":true}`},
		{"POST", "/api/flows/" + locked.ID + "/run", `{}`},
		{"POST", "/api/flows/" + locked.ID + "/fork", ``},
	} {
		if rec := over(c.method, c.path, c.body); rec.Code != 403 {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if got, _ := dbpkg.GetFlow(r.h.db, locked.ID); got.Enabled {
		t.Fatal("a locked flow was enabled over NATS")
	}
	// the ordinary road stays open: an ordinary flow over NATS is as it was
	mine := publishFlow(t, r.h, r.pid, flows.Graph{Nodes: []flows.Node{{ID: "m", Type: flows.TypeManual}}})
	if rec := over("PUT", "/api/flows/"+mine.ID+"/enabled", `{"enabled":false}`); rec.Code != 200 {
		t.Errorf("an ordinary flow over NATS: %d %s", rec.Code, rec.Body)
	}
}

// What happens on the board only reaches plugins allowed to read it.
func TestCardMovedNeedsBoardRead(t *testing.T) {
	r := newPluginRig(t)
	code := `module.exports={events:{cardMoved(e){lucode.storage.set("moved",e.to_kind)}},rpc:{go(){return 1}}}`
	manifest := func(id, perms string) string {
		return `{"id":"` + id + `","name":"N","version":"1.0.0","sdk":1,"main":"main.js","permissions":{"storage":true` + perms + `},
			"contributes":{"chatActions":[{"id":"a","label":"A","rpc":"go"}]}}`
	}
	r.install(map[string]string{"plugin.json": manifest("acme.blind", ""), "main.js": code})
	r.install(map[string]string{"plugin.json": manifest("acme.reader", `,"board":"read"`), "main.js": code})
	card := newCard(t, r.h, r.pid, r.lists["todo"], "the task")
	if _, err := r.h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	settleFlows(r)
	waitFor(t, "the reader to hear", func() bool {
		_, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.reader", "moved")
		return ok
	})
	time.Sleep(200 * time.Millisecond)
	if _, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.blind", "moved"); ok {
		t.Fatal("a plugin with no board permission heard about a card")
	}
}

func TestBrowserOrigins(t *testing.T) {
	r := newPluginRig(t)
	files := map[string]string{
		"plugin.json": `{"id":"acme.web","name":"W","version":"1.0.0","sdk":1,"main":"main.js",
			"permissions":{"browser":{"origins":["https://www.example.com"]}},
			"contributes":{"chatActions":[{"id":"x","label":"x","rpc":"go"}]}}`,
		"main.js": `module.exports={rpc:{go(a){
			if(a.op==="nav") return lucode.browser.navigate(a.url);
			if(a.op==="tab") return lucode.browser.newTab(a.url);
			if(a.op==="eval") return lucode.browser.evalPage(a.js);
		}}}`,
	}
	r.install(files)
	call := func(args map[string]any) (int, string) {
		code, _, raw := r.rpc("acme.web", "go", args)
		return code, raw
	}
	if code, raw := call(map[string]any{"op": "nav", "url": "https://www.example.com/page"}); code != 200 {
		t.Fatalf("an allowed origin: %d %s", code, raw)
	}
	for _, bad := range []string{
		"https://evil.example.org/", "http://www.example.com/", "https://www.example.com.evil.org/",
		"file:///etc/passwd", "javascript:alert(1)", "about:srcdoc", "ABOUT:BLANK", "https://www.example.com@evil.org/",
	} {
		if code, raw := call(map[string]any{"op": "nav", "url": bad}); code == 200 {
			t.Errorf("navigate %s: %s", bad, raw)
		}
		if code, raw := call(map[string]any{"op": "tab", "url": bad}); code == 200 {
			t.Errorf("newTab %s: %s", bad, raw)
		}
	}
	// an empty page to start from is fine, and no code runs on it
	for _, op := range []string{"nav", "tab"} {
		if code, raw := call(map[string]any{"op": op, "url": "about:blank"}); code != 200 {
			t.Fatalf("%s about:blank: %d %s", op, code, raw)
		}
	}
	if code, raw := call(map[string]any{"op": "eval", "js": "0"}); code == 200 || !strings.Contains(raw, "not an origin") || len(r.browse.evals) != 0 {
		t.Fatalf("eval on about:blank: %d %s", code, raw)
	}
	if code, raw := call(map[string]any{"op": "nav", "url": "https://www.example.com/page"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if code, raw := call(map[string]any{"op": "eval", "js": "1"}); code != 200 || len(r.browse.evals) != 1 {
		t.Fatalf("eval on the allowed page: %d %s", code, raw)
	}
	// the page went somewhere else on its own: the origin is checked again before any code runs
	r.browse.url = "https://evil.example.org/stolen"
	if code, raw := call(map[string]any{"op": "eval", "js": "2"}); code == 200 || !strings.Contains(raw, "not an origin") {
		t.Fatalf("eval after a redirect: %d %s", code, raw)
	}
	if len(r.browse.evals) != 1 {
		t.Fatalf("code ran on another origin: %v", r.browse.evals)
	}
	// disabling closes the plugin's browser
	r.ok("admin", "PUT", "/api/plugins/acme.web/enabled", map[string]any{"enabled": false}, nil)
	waitFor(t, "the browser to close", func() bool { return r.browse.closed > 0 })
}

// A turn on a plugin model, the whole way: API, manager, plugin code, messages, status.
func TestTurnOnAPluginModel(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	wire := "plugin:acme.hello/echo"
	var sess dbpkg.Session
	r.ok("user", "POST", "/api/plugins/acme.hello/conversations", map[string]any{"projectId": r.pid, "model": "echo"}, &sess)
	if sess.Kind != dbpkg.SessionKindPlugin || sess.Model != wire {
		t.Fatalf("%+v", sess)
	}
	listed, _ := dbpkg.ListSessions(r.h.db, r.pid)
	if len(listed) != 0 {
		t.Fatalf("a page's chat is not in the session list: %+v", listed)
	}
	status := func() string {
		s, _ := dbpkg.GetSession(r.h.db, sess.ID)
		return s.Status
	}
	send := func(msg string) int {
		code, _, _ := r.do("user", "POST", "/api/sessions/"+sess.ID+"/turn", map[string]any{"message": msg, "model": wire, "image": map[string]any{"base64": "", "mediaType": ""}})
		return code
	}
	answered := func(want string) {
		t.Helper()
		waitFor(t, "the answer", func() bool {
			msgs, _ := dbpkg.ListMessages(r.h.db, sess.ID)
			for _, m := range msgs {
				if m.Role == "assistant" && m.Content == want {
					return !r.h.manager.TurnActive(sess.ID)
				}
			}
			return false
		})
	}
	if code := send("hi"); code != 202 {
		t.Fatalf("turn: %d", code)
	}
	answered("echo: hi")
	waitFor(t, "idle", func() bool { return status() == "idle" })
	conv, err := dbpkg.GetPluginConversationBySession(r.h.db, "acme.hello", sess.ID)
	if err != nil || conv.Title != "hi" {
		t.Fatalf("conversation: %+v %v", conv, err)
	}
	var rows []pluginConversationRow
	r.ok("user", "GET", "/api/plugins/acme.hello/conversations?project="+r.pid, nil, &rows)
	if len(rows) != 1 || !rows[0].SessionExists || rows[0].SessionKind != dbpkg.SessionKindPlugin || rows[0].MessageCount < 2 {
		t.Fatalf("%+v", rows)
	}

	// a failure: an error status and a message, and the session is free again
	if code := send("boom"); code != 202 {
		t.Fatalf("turn: %d", code)
	}
	waitFor(t, "the error status", func() bool { return status() == "error" && !r.h.manager.TurnActive(sess.ID) })

	// cancelling a slow one and a spinning one both end in idle
	for _, msg := range []string{"slow", "hang"} {
		if code := send(msg); code != 202 {
			t.Fatalf("turn: %d", code)
		}
		waitFor(t, "the turn to start", func() bool { return r.h.manager.TurnActive(sess.ID) })
		time.Sleep(50 * time.Millisecond)
		if code, _, _ := r.do("user", "DELETE", "/api/sessions/"+sess.ID+"/turn", nil); code != 204 {
			t.Fatalf("cancel: %d", code)
		}
		waitFor(t, "idle after cancel", func() bool { return status() == "idle" && !r.h.manager.TurnActive(sess.ID) })
	}

	// the conversation comes back, and with its chat gone a new one takes it over
	var back dbpkg.Session
	r.ok("user", "POST", "/api/plugins/acme.hello/conversations/"+rows[0].ID+"/continue", nil, &back)
	if back.ID != sess.ID {
		t.Fatalf("%+v", back)
	}
	dbpkg.DeleteSession(r.h.db, sess.ID)
	var taken dbpkg.Session
	r.ok("user", "POST", "/api/plugins/acme.hello/conversations/"+rows[0].ID+"/continue?surface=page", nil, &taken)
	if taken.ID == sess.ID || taken.Model != wire || taken.Kind != dbpkg.SessionKindPlugin {
		t.Fatalf("%+v", taken)
	}
}

func TestModelIDsAreChecked(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	sess := dbpkg.Session{ID: "s1", ProjectID: r.pid, Name: "n", Model: "haiku", Status: "idle"}
	dbpkg.InsertSession(r.h.db, sess)
	for name, model := range map[string]string{
		"another plugin":   "plugin:acme.other/echo",
		"a model it lacks": "plugin:acme.hello/nope",
		"malformed":        "plugin:acme.hello",
		"bad characters":   "plugin:../x/echo",
	} {
		t.Run(name, func(t *testing.T) {
			code, raw, _ := r.do("user", "POST", "/api/sessions/s1/turn", map[string]any{"message": "x", "model": model})
			if code != 409 {
				t.Fatalf("%d %s", code, raw)
			}
		})
	}
	// an orchestrator chat is refused the same way
	orch := dbpkg.Session{ID: "o1", ProjectID: r.pid, Name: "n", Model: "sonnet", Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
	dbpkg.InsertSession(r.h.db, orch)
	if code, raw, _ := r.do("user", "POST", "/api/sessions/o1/turn", map[string]any{"message": "x", "model": "plugin:acme.other/echo"}); code != 400 {
		t.Fatalf("%d %s", code, raw)
	}
	// and takes a plugin's model through the same road
	proj, _ := dbpkg.GetProject(r.h.db, r.pid)
	opts, err := r.h.orchestratorTurnOpts(orch, proj, "hi", "plugin:acme.hello/echo", "", "", "", "")
	if err != nil || opts.Executor != pluginExecutor || opts.Model != "plugin:acme.hello/echo" || opts.Persistent || opts.RunnerID != "" {
		t.Fatalf("%+v %v", opts, err)
	}
	// the old id means its plugin, and without it the turn is refused, saying why
	if _, err := r.h.orchestratorTurnOpts(orch, proj, "hi", plugins.LegacyAIMode, "", "", "", ""); err == nil || !strings.Contains(err.Error(), "lucode.gemini is not installed") {
		t.Fatalf("%v", err)
	}
	if code, raw, _ := r.do("user", "POST", "/api/sessions/s1/turn", map[string]any{"message": "x", "model": plugins.LegacyAIMode}); code != 409 || !strings.Contains(string(raw), "not installed") {
		t.Fatalf("%d %s", code, raw)
	}
	r.ok("admin", "PUT", "/api/plugins/acme.hello/enabled", map[string]any{"enabled": false}, nil)
	code, raw, _ := r.do("user", "POST", "/api/sessions/s1/turn", map[string]any{"message": "x", "model": "plugin:acme.hello/echo"})
	if code != 409 || !strings.Contains(string(raw), "disabled") {
		t.Fatalf("a disabled plugin's model: %d %s", code, raw)
	}
	if code, _, _ := r.do("user", "POST", "/api/plugins/acme.hello/conversations", map[string]any{"projectId": r.pid, "model": "echo"}); code != 409 {
		t.Fatalf("a chat with a disabled plugin: %d", code)
	}
}

func TestLegacyModelMapsToItsPlugin(t *testing.T) {
	dir := t.TempDir()
	r := newPluginRig(t, func(d *plugins.HubDeps) {
		d.DataDir = dir
		d.Bundled = fstest.MapFS{
			"gemini/plugin.json": {Data: []byte(`{"id":"lucode.gemini","name":"G","version":"1.0.0","sdk":1,"main":"main.js","contributes":{"models":[{"id":"aimode","label":"AI"}]}}`)},
			"gemini/main.js":     {Data: []byte(`module.exports={models:{aimode:{turn(){return "from the plugin"}}}}`)},
		}
		d.AutoInstall = []string{"lucode.gemini"}
	})
	sess := dbpkg.Session{ID: "o2", ProjectID: r.pid, Name: "n", Model: plugins.LegacyAIMode, Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
	dbpkg.InsertSession(r.h.db, sess)
	proj, _ := dbpkg.GetProject(r.h.db, r.pid)
	opts, err := r.h.orchestratorTurnOpts(sess, proj, "hi", plugins.LegacyAIMode, "", "", "", "")
	if err != nil || opts.Executor != pluginExecutor || opts.Model != plugins.MappedAIMode {
		t.Fatalf("%+v %v", opts, err)
	}
	if code, raw, _ := r.do("user", "POST", "/api/sessions/o2/turn", map[string]any{"message": "x", "model": plugins.LegacyAIMode}); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	waitFor(t, "the answer", func() bool {
		msgs, _ := dbpkg.ListMessages(r.h.db, "o2")
		for _, m := range msgs {
			if m.Content == "from the plugin" {
				return true
			}
		}
		return false
	})
}

func publishPluginFlow(t *testing.T, r *pluginRig, q string, extra ...flows.Node) dbpkg.Flow {
	t.Helper()
	g := flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "p", Type: "plugin.acme.hello.ask", Config: map[string]any{"q": q}},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "said {{nodes.p.answer}}"}},
		},
		Edges: []flows.Edge{{From: "t", Port: "next", To: "p"}, {From: "p", Port: "next", To: "c"}},
	}
	return publishFlow(t, r.h, r.pid, g)
}

func settleFlows(r *pluginRig) {
	r.h.flows.Drain()
	r.h.flows.WaitForEvaluations()
	r.h.flows.Drain()
}

// A flow with a plugin's node, on the real board and the real plugin code.
func TestFlowRunsAPluginNode(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	f := publishPluginFlow(t, r, "{{card.title}}?")
	card := newCard(t, r.h, r.pid, r.lists["todo"], "the task")
	if _, err := r.h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	settleFlows(r)
	run := lastRun(t, r.h, f)
	if run.Status != dbpkg.RunSucceeded {
		steps, _ := dbpkg.ListFlowSteps(r.h.db, run.ID)
		t.Fatalf("run %s %q: %+v", run.Status, run.Error, steps)
	}
	comments, _ := dbpkg.ListCardComments(r.h.db, card.ID)
	if len(comments) != 1 || comments[0].Body != "said got the task? on the task" {
		t.Fatalf("%+v", comments)
	}
	waitFor(t, "cardMoved to reach the plugin", func() bool {
		v, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.hello", "moved")
		return ok && v == `"review"`
	})

	// the node is in the list the editor reads, with its plugin
	var types []flows.NodeType
	r.ok("user", "GET", "/api/flow-node-types", nil, &types)
	found := false
	for _, n := range types {
		if n.Type == "plugin.acme.hello.ask" {
			found = n.PluginID == "acme.hello" && n.PluginName == "Hello" && n.Category == flows.CategoryAction
		}
	}
	if !found {
		t.Fatal("the plugin's node is not listed")
	}

	// disabled: it stays listed, and a run fails with a clear message
	r.ok("admin", "PUT", "/api/plugins/acme.hello/enabled", map[string]any{"enabled": false}, nil)
	r.ok("user", "GET", "/api/flow-node-types", nil, &types)
	flagged := false
	for _, n := range types {
		if n.Type == "plugin.acme.hello.ask" {
			flagged = n.Disabled
		}
	}
	if !flagged {
		t.Fatal("a disabled plugin's node should stay listed, flagged")
	}
	second := newCard(t, r.h, r.pid, r.lists["todo"], "second")
	r.h.moveCardToKind(second, dbpkg.KanbanReview)
	settleFlows(r)
	run = lastRun(t, r.h, f)
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "disabled") {
		t.Fatalf("run %s %q", run.Status, run.Error)
	}
}

func TestPluginEventsFireFlows(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	r.allowRPC("emit")
	f := publishFlow(t, r.h, r.pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "e", Type: flows.TypePluginEvent, Config: map[string]any{"plugin": "acme.hello", "event": "done"}},
			{ID: "m", Type: flows.TypeCreateTask, Config: map[string]any{"title": "made from {{event.payload.x}}"}},
		},
		Edges: []flows.Edge{{From: "e", Port: "next", To: "m"}},
	})
	if code, _, raw := r.rpc("acme.hello", "emit", map[string]any{"x": "y"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	settleFlows(r)
	if run := lastRun(t, r.h, f); run.Status != dbpkg.RunSucceeded {
		t.Fatalf("run %s %q", run.Status, run.Error)
	}
	cards, _ := dbpkg.ListKanbanCards(r.h.db, r.pid, false)
	if len(cards) != 1 || cards[0].Title != "made from y" {
		t.Fatalf("%+v", cards)
	}
}

// A plugin's blueprint is a locked flow in each project, off, until someone forks or enables it.
func TestBlueprintsAreLockedFlows(t *testing.T) {
	r := newPluginRig(t)
	r.install(helloFiles())
	var list []dbpkg.Flow
	r.ok("user", "GET", "/api/projects/"+r.pid+"/flows", nil, &list)
	var bp dbpkg.Flow
	for _, f := range list {
		if f.PluginID == "acme.hello" {
			bp = f
		}
	}
	if !bp.Locked || bp.Enabled || bp.PublishedVersion != 1 || bp.PluginBlueprint != "bp" || bp.Name != "Hello flow" {
		t.Fatalf("%+v in %+v", bp, list)
	}
	// a new project gets it too
	other := dbpkg.Project{ID: "p2", Name: "two", WorkDirs: []string{"."}, DefaultModel: "haiku"}
	dbpkg.InsertProject(r.h.db, other)
	r.h.seedProjectFlows(other.ID)
	var inOther []dbpkg.Flow
	r.ok("user", "GET", "/api/projects/p2/flows", nil, &inOther)
	has := false
	for _, f := range inOther {
		has = has || f.PluginID == "acme.hello"
	}
	if !has {
		t.Fatalf("%+v", inOther)
	}

	// no road changes it
	if code, raw, _ := r.do("user", "PUT", "/api/flows/"+bp.ID, map[string]any{"name": "x"}); code != 409 {
		t.Fatalf("save: %d %s", code, raw)
	}
	if code, raw, _ := r.do("user", "POST", "/api/flows/"+bp.ID+"/publish", nil); code != 409 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	if code, raw, _ := r.do("user", "DELETE", "/api/flows/"+bp.ID, nil); code != 409 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if _, err := r.h.flowUpdateTool(r.pid, "Bot", json.RawMessage(`{"flow":"`+bp.ID+`","name":"hacked"}`)); err == nil || !strings.Contains(err.Error(), "belongs to a plugin") {
		t.Fatalf("flow_update: %v", err)
	}
	// but it can be enabled and run
	r.ok("user", "PUT", "/api/flows/"+bp.ID+"/enabled", map[string]any{"enabled": true}, nil)
	var run dbpkg.FlowRun
	r.ok("user", "POST", "/api/flows/"+bp.ID+"/run", map[string]any{}, &run)
	settleFlows(r)
	got, _ := dbpkg.GetFlowRun(r.h.db, run.ID)
	// the node reads card.title, and this run has no card: the plugin's own error is what shows
	if got.Status == dbpkg.RunRunning {
		t.Fatalf("still running: %+v", got)
	}

	// a fork is an editable draft
	var fork dbpkg.Flow
	r.ok("user", "POST", "/api/flows/"+bp.ID+"/fork", nil, &fork)
	if fork.Locked || fork.Enabled || fork.PublishedVersion != 0 || fork.ForkedFrom != bp.ID || fork.Name != "Hello flow (copy)" {
		t.Fatalf("%+v", fork)
	}
	if code, _, _ := r.do("user", "POST", "/api/flows/"+fork.ID+"/fork", nil); code != 400 {
		t.Fatalf("forking a fork: %d", code)
	}
	r.ok("user", "PUT", "/api/flows/"+fork.ID, map[string]any{"name": "mine"}, nil)

	// an upgrade publishes a new version, and it stays on
	next := helloFiles()
	next["plugin.json"] = strings.Replace(helloManifest, `"version":"1.0.0"`, `"version":"2.0.0"`, 1)
	next["blueprints/bp.json"] = strings.Replace(helloBlueprint, `"bp"`, `"changed"`, 1)
	r.install(next)
	up, _ := dbpkg.GetFlow(r.h.db, bp.ID)
	if up.PublishedVersion != 2 || !up.Enabled || !up.Locked {
		t.Fatalf("%+v", up)
	}

	// the flow tools see plugin nodes as valid while it is there
	if p := flows.Validate(mustGraph(t, fork.Draft)); len(p) != 0 {
		t.Fatalf("%+v", p)
	}
	// uninstall: its flows go, the fork stays and says what is missing
	r.ok("admin", "DELETE", "/api/plugins/acme.hello", nil, nil)
	if _, err := dbpkg.GetFlow(r.h.db, bp.ID); err == nil {
		t.Fatal("the locked flow should be gone")
	}
	left, err := dbpkg.GetFlow(r.h.db, fork.ID)
	if err != nil {
		t.Fatalf("the fork should stay: %v", err)
	}
	problems := flows.Validate(mustGraph(t, left.Draft))
	named := false
	for _, p := range problems {
		named = named || strings.Contains(p.Message, "the plugin acme.hello, which is not installed")
	}
	if !named {
		t.Fatalf("%+v", problems)
	}
}

func mustGraph(t *testing.T, raw json.RawMessage) flows.Graph {
	t.Helper()
	g, err := flows.ParseGraph(raw)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A chat that is over reaches the plugins that had a conversation in it, and nobody else. A handler
// that throws or hangs does not touch the request.
func TestSessionClosed(t *testing.T) {
	r := newPluginRig(t)
	code := `module.exports={
		models:{m:{turn(){return "x"}}},
		rpc:{save(a){ lucode.conversations.save({sessionId:a.sessionId,title:"t"}); return "ok" }},
		events:{
			sessionClosed(e){ lucode.storage.set("closed", e); if(lucode.storage.get("hang")) for(;;){} ; throw new Error("handler failed") },
			sessionDeleted(e){ lucode.storage.set("deleted", e.sessionId) },
		},
	}`
	manifest := func(id string) string {
		return `{"id":"` + id + `","name":"N","version":"1.0.0","sdk":1,"main":"main.js","permissions":{"storage":true,"conversations":true},
			"contributes":{"models":[{"id":"m","label":"M"}],"chatActions":[{"id":"a","label":"A","rpc":"save"}]}}`
	}
	r.install(map[string]string{"plugin.json": manifest("acme.has"), "main.js": code})
	r.install(map[string]string{"plugin.json": manifest("acme.hasnt"), "main.js": code})
	if err := dbpkg.PluginStorageSet(r.h.db, "acme.has", "hang", "true"); err != nil {
		t.Fatal(err)
	}
	closedOf := func(id string) (string, bool) {
		v, ok, _ := dbpkg.PluginStorageGet(r.h.db, id, "closed")
		return v, ok
	}

	// replacing the Supervisor conversation closes it
	var old dbpkg.Session
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &old)
	if status, _, raw := r.rpc("acme.has", "save", map[string]any{"sessionId": old.ID}); status != 200 {
		t.Fatalf("%d %s", status, raw)
	}
	start := time.Now()
	if status, raw, _ := r.do("user", "POST", "/api/projects/"+r.pid+"/orchestrator/new", nil); status != 200 {
		t.Fatalf("a hanging handler must not fail the request: %d %s", status, raw)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hanging handler held the request for %s", d)
	}
	waitFor(t, "sessionClosed", func() bool {
		v, ok := closedOf("acme.has")
		return ok && strings.Contains(v, old.ID) && strings.Contains(v, r.pid)
	})
	if _, ok := closedOf("acme.hasnt"); ok {
		t.Fatal("a plugin with no conversation in that session should not hear about it")
	}
	if _, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.has", "deleted"); ok {
		t.Fatal("replacing a conversation deletes nothing")
	}

	// deleting fires both, and a failing handler changes nothing for the caller
	dbpkg.PluginStorageDelete(r.h.db, "acme.has", "hang")
	dbpkg.PluginStorageDelete(r.h.db, "acme.has", "closed")
	sess := dbpkg.Session{ID: "gone", ProjectID: r.pid, Name: "n", Model: "haiku", Status: "idle"}
	dbpkg.InsertSession(r.h.db, sess)
	r.rpc("acme.has", "save", map[string]any{"sessionId": sess.ID})
	if status, raw, _ := r.do("user", "DELETE", "/api/sessions/gone", nil); status != 204 {
		t.Fatalf("%d %s", status, raw)
	}
	waitFor(t, "both events", func() bool {
		_, closed := closedOf("acme.has")
		v, deleted, _ := dbpkg.PluginStorageGet(r.h.db, "acme.has", "deleted")
		return closed && deleted && strings.Contains(v, "gone")
	})
	if _, ok := closedOf("acme.hasnt"); ok {
		t.Fatal("a plugin with no conversation in that session should not hear about it")
	}
}

func TestUninstallKeepsOrRemovesData(t *testing.T) {
	for _, keep := range []bool{true, false} {
		r := newPluginRig(t)
		r.install(helloFiles())
		r.allowRPC("storageRoundTrip")
		r.rpc("acme.hello", "storageRoundTrip", map[string]any{"key": "k", "value": 1})
		path := "/api/plugins/acme.hello"
		if keep {
			path += "?keepData=1"
		}
		r.ok("admin", "DELETE", path, nil, nil)
		_, found, _ := dbpkg.PluginStorageGet(r.h.db, "acme.hello", "k")
		if found != keep {
			t.Fatalf("keep=%v: found=%v", keep, found)
		}
		var list []plugins.PluginView
		r.ok("user", "GET", "/api/plugins", nil, &list)
		if len(list) != 0 {
			t.Fatalf("%+v", list)
		}
	}
}

// A runner's binary does not contain the hub's host.
func TestRunnerDoesNotLinkTheHost(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out, err := goCommand(root, "list", "-deps", "./cmd/lucode-runner")
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	if strings.Contains(out, "internal/plugins/hub") || strings.Contains(out, "internal/plugins/bundled") {
		t.Fatal("cmd/lucode-runner links the hub's plugin host")
	}
	if !strings.Contains(out, "internal/plugins/runner") {
		t.Fatal("cmd/lucode-runner should link its own host")
	}
}

// The engine's name reaches neither the API nor a message.
func TestRuntimeNameNeverLeaks(t *testing.T) {
	r := newPluginRig(t)
	files := helloFiles()
	files["main.js"] = `module.exports={rpc:{renderMain(){ null.x }, summarize(){ return lucode.nope.x() }}}`
	r.install(files)
	for _, name := range []string{"renderMain", "summarize"} {
		code, _, raw := r.rpc("acme.hello", name, nil)
		if code != 422 || strings.Contains(strings.ToLower(raw), "g"+"oja") {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	_, logs, _ := r.do("admin", "GET", "/api/plugins/acme.hello/logs", nil)
	if strings.Contains(strings.ToLower(string(logs)), "g"+"oja") {
		t.Fatalf("logs: %s", logs)
	}
}

func goCommand(dir string, args ...string) (string, error) {
	return runIn(dir, "go", args...)
}
