package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/plugins/bundled"
	pluginhub "claude-orchestrator/internal/plugins/hub"
)

// The real bundled Gemini plugin through the real sandbox, with a browser that plays Google's page.
// Nothing reaches Google.

// fakeGoogle behaves like the page as far as the plugin can see: a home with an AI Mode button, a
// message box, and turns that come back with an answer.
type fakeGoogle struct {
	mu      sync.Mutex
	tabs    []*googleTab
	current *googleTab
	pages   map[string]*googlePage
	nextTab int
	typed   []string
	newTabs []string
	visited []string
	closed  []string
	consent bool // Google sends the first visit to its consent page
}

type googleTab struct {
	id  string
	url string
}

type googlePage struct {
	turns  int
	answer string
}

func newFakeGoogle() *fakeGoogle { return &fakeGoogle{pages: map[string]*googlePage{}} }

func (g *fakeGoogle) addTab(url string) *googleTab {
	g.nextTab++
	t := &googleTab{id: fmt.Sprintf("tab%d", g.nextTab), url: url}
	g.tabs = append(g.tabs, t)
	g.current = t
	return t
}

func (g *fakeGoogle) page() *googlePage {
	if g.current == nil {
		return &googlePage{}
	}
	p, ok := g.pages[g.current.url]
	if !ok {
		p = &googlePage{}
		g.pages[g.current.url] = p
	}
	return p
}

func (g *fakeGoogle) inAIMode() bool {
	return g.current != nil && strings.Contains(g.current.url, "udm=50")
}

func (g *fakeGoogle) Launch(bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.tabs) == 0 {
		g.addTab("about:blank")
	}
	return "started", nil
}
func (g *fakeGoogle) Close() string  { return "closed" }
func (g *fakeGoogle) Status() string { return "ok" }
func (g *fakeGoogle) Tabs() (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []any{}
	for _, t := range g.tabs {
		out = append(out, map[string]any{"id": t.id, "url": t.url, "title": "", "current": t == g.current})
	}
	return out, nil
}
func (g *fakeGoogle) NewTab(url string) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.newTabs = append(g.newTabs, url)
	t := g.addTab(url)
	return map[string]any{"url": t.url}, nil
}
func (g *fakeGoogle) SwitchTab(id string) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.tabs {
		if t.id == id {
			g.current = t
			return map[string]any{"url": t.url}, nil
		}
	}
	return nil, fmt.Errorf("no tab %s", id)
}
func (g *fakeGoogle) CloseTab(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = append(g.closed, id)
	for i, t := range g.tabs {
		if t.id == id {
			g.tabs = append(g.tabs[:i], g.tabs[i+1:]...)
			if g.current == t {
				g.current = nil
				if len(g.tabs) > 0 {
					g.current = g.tabs[0]
				}
			}
		}
	}
	return nil
}
func (g *fakeGoogle) URL() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current == nil {
		return "", fmt.Errorf("no tab")
	}
	return g.current.url, nil
}
func (g *fakeGoogle) Info() (any, error) {
	url, err := g.URL()
	return map[string]any{"url": url, "title": "Google"}, err
}
func (g *fakeGoogle) Navigate(url string) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.visited = append(g.visited, url)
	g.current.url = url
	if g.consent && url == "https://www.google.com/" {
		g.current.url = "https://consent.google.com/m?continue=https://www.google.com/"
	}
	return map[string]any{"url": url}, nil
}
func (g *fakeGoogle) Find(role, name, _ string, _ bool, _ int) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case name == "Accept all" && role == "button" && g.current != nil && strings.HasPrefix(g.current.url, "https://consent.google.com/"):
		return []any{map[string]any{"ref": "accept", "name": name}}, nil
	case name == "AI Mode" && role == "link" && g.current != nil && g.current.url == "https://www.google.com/":
		return []any{map[string]any{"ref": "enter", "name": name}}, nil
	case name == "" && role == "textbox" && g.inAIMode():
		return []any{map[string]any{"ref": "box", "name": "Ask"}}, nil
	}
	return []any{}, nil
}
func (g *fakeGoogle) Click(ref string) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ref == "accept" {
		g.consent = false
		g.current.url = "https://www.google.com/"
	}
	if ref == "enter" {
		g.current.url = fmt.Sprintf("https://www.google.com/search?udm=50&conv=%s", g.current.id)
	}
	return "clicked", nil
}
func (g *fakeGoogle) Type(_, text string, _, _ bool) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.typed = append(g.typed, text)
	p := g.page()
	p.turns++
	p.answer = "answer to " + text + "\nLa IA puede cometer errores, así que verifica las respuestas"
	return "typed", nil
}
func (g *fakeGoogle) Press(string) (any, error) { return "pressed", nil }
func (g *fakeGoogle) Eval(js string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.Contains(js, `document.querySelectorAll('[data-scope-id="turn"]').length`) && len(js) < 100 {
		return fmt.Sprint(g.page().turns), nil
	}
	return g.page().answer, nil
}
func (g *fakeGoogle) Text(string, int) (string, error) { return "", nil }

func (g *fakeGoogle) primersTyped() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, t := range g.typed {
		if !strings.HasPrefix(t, "msg ") {
			n++
		}
	}
	return n
}

func geminiRig(t *testing.T, g *fakeGoogle) *pluginRig {
	t.Helper()
	return newPluginRig(t, func(d *plugins.HubDeps) {
		d.Bundled = bundled.FS
		d.AutoInstall = []string{plugins.GeminiID}
		d.Browser = func(string) plugins.BrowserDriver { return g }
	})
}

func insertOldConversation(t *testing.T, r *pluginRig, id, sessID, url, title string, turns int) {
	t.Helper()
	_, err := r.h.db.Exec(`INSERT INTO aimode_conversations (id, session_id, project_id, url, title, turns) VALUES (?,?,?,?,?,?)`, id, sessID, r.pid, url, title, turns)
	if err != nil {
		t.Fatal(err)
	}
}

func (r *pluginRig) lastAnswer(sessID, want string) {
	r.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := dbpkg.ListMessages(r.h.db, sessID)
		for _, m := range msgs {
			if m.Role == "assistant" && m.Content == want && !r.h.manager.TurnActive(sessID) {
				return
			}
		}
		s, _ := dbpkg.GetSession(r.h.db, sessID)
		if s.Status == "error" {
			logs, _ := r.h.plugins.Logs(plugins.GeminiID)
			r.t.Fatalf("the turn failed: %+v", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	msgs, _ := dbpkg.ListMessages(r.h.db, sessID)
	r.t.Fatalf("no answer %q: %+v", want, msgs)
}

func (r *pluginRig) turn(sessID, model, msg string) {
	r.t.Helper()
	code, raw, _ := r.do("user", "POST", "/api/sessions/"+sessID+"/turn", map[string]any{"message": msg, "model": model})
	if code != 202 {
		r.t.Fatalf("turn: %d %s", code, raw)
	}
}

func TestGeminiEndToEnd(t *testing.T) {
	g := newFakeGoogle()
	r := geminiRig(t, g)
	first := dbpkg.Session{ID: "first", ProjectID: r.pid, Name: "Orchestrator", Model: plugins.LegacyAIMode, Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
	second := dbpkg.Session{ID: "second", ProjectID: r.pid, Name: "chat", Model: plugins.LegacyAIMode, Status: "idle"}
	for _, s := range []dbpkg.Session{first, second} {
		if err := dbpkg.InsertSession(r.h.db, s); err != nil {
			t.Fatal(err)
		}
	}

	// the old id on an old session, then the new id: the same conversation, instructed once
	r.turn("first", plugins.LegacyAIMode, "msg one")
	r.lastAnswer("first", "answer to msg one") // the page's notice is cleaned off
	if n := g.primersTyped(); n != 1 {
		t.Fatalf("instructions typed %d times: %v", n, g.typed)
	}
	r.turn("first", plugins.MappedAIMode, "msg two")
	r.lastAnswer("first", "answer to msg two")
	if n := g.primersTyped(); n != 1 || len(g.tabs) != 1 || len(g.newTabs) != 0 {
		t.Fatalf("the second turn must stay in its tab: primers %d tabs %d new %v", n, len(g.tabs), g.newTabs)
	}
	conv, err := dbpkg.GetPluginConversationBySession(r.h.db, plugins.GeminiID, "first")
	if err != nil || conv.Title != "msg one" || conv.Source != "aimode" || conv.Turns != 3 || !strings.Contains(conv.URL, "udm=50") {
		t.Fatalf("%+v %v", conv, err)
	}

	// another session's first turn gets a blank tab of its own and its own instructions
	r.turn("second", "", "msg three")
	r.lastAnswer("second", "answer to msg three")
	if len(g.newTabs) != 1 || g.newTabs[0] != "about:blank" || len(g.tabs) != 2 || g.primersTyped() != 2 {
		t.Fatalf("new tabs %v tabs %d primers %d", g.newTabs, len(g.tabs), g.primersTyped())
	}

	// a conversation from the old table reopens the same Google page, with no new instructions
	const old = "https://www.google.com/search?udm=50&mtid=old"
	g.mu.Lock()
	g.pages[old] = &googlePage{turns: 4}
	g.mu.Unlock()
	third := dbpkg.Session{ID: "third", ProjectID: r.pid, Name: "chat", Model: plugins.LegacyAIMode, Status: "idle"}
	if err := dbpkg.InsertSession(r.h.db, third); err != nil {
		t.Fatal(err)
	}
	insertOldConversation(t, r, "old-row", "third", old, "the old question", 4)
	if n, err := dbpkg.CopyAIModeConversations(r.h.db, plugins.GeminiID, "aimode"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	r.turn("third", plugins.LegacyAIMode, "msg four")
	r.lastAnswer("third", "answer to msg four")
	if g.primersTyped() != 2 {
		t.Fatalf("a reopened conversation needs no instructions: %v", g.typed)
	}
	g.mu.Lock()
	visited := strings.Join(g.visited, " ")
	g.mu.Unlock()
	if !strings.Contains(visited, old) {
		t.Fatalf("visited: %s", visited)
	}
	conv, err = dbpkg.GetPluginConversationBySession(r.h.db, plugins.GeminiID, "third")
	if err != nil || conv.ID != "old-row" || conv.URL != old || conv.Title != "the old question" || conv.Turns != 5 {
		t.Fatalf("%+v %v", conv, err)
	}
}

// A bundled plugin follows the binary, even with the same version number.
func TestBundledPluginFollowsTheBinary(t *testing.T) {
	dir := t.TempDir()
	const id = "lucode.sample"
	manifest := func(version, origins string) string {
		return `{"id":"lucode.sample","name":"Sample","version":"` + version + `","sdk":1,"main":"main.js",
			"permissions":{"storage":true,"flows":true,"browser":{"origins":[` + origins + `]}},
			"contributes":{"settings":[{"key":"k","label":"K","type":"text"}],
				"blueprints":[{"key":"bp","name":"BP","file":"blueprints/bp.json"}],
				"flowNodes":[{"id":"n","label":"N","ports":["next"]}]}}`
	}
	const blueprint = `{"nodes":[{"id":"t","type":"trigger.manual","config":{}},{"id":"a","type":"plugin.lucode.sample.n","config":{}}],"edges":[{"from":"t","port":"next","to":"a"}]}`
	bundle := func(version, origins, code, bp string) fstest.MapFS {
		return fstest.MapFS{
			"sample/plugin.json":        {Data: []byte(manifest(version, origins))},
			"sample/main.js":            {Data: []byte(code)},
			"sample/blueprints/bp.json": {Data: []byte(bp)},
		}
	}
	const a, b = `"https://a.example.com"`, `"https://a.example.com","https://b.example.com"`
	const codeA = `module.exports={nodes:{n(){return {output:{from:"A"}}}},events:{upgraded(e){lucode.storage.set("upgraded",1)}}}`
	const codeB = `module.exports={nodes:{n(){return {output:{from:"B"}}}},events:{upgraded(e){lucode.storage.set("upgraded",1)}}}`
	first := bundle("1.0.0", a, codeA, blueprint)
	r := newPluginRig(t, func(d *plugins.HubDeps) { d.DataDir = dir; d.Bundled = first; d.AutoInstall = []string{id} })
	start := func(fsys fstest.MapFS) {
		t.Helper()
		if err := r.h.startPluginHost(context.Background(), plugins.HubDeps{DataDir: dir, Bundled: fsys, Browser: func(string) plugins.BrowserDriver { return r.browse }}, pluginhub.New); err != nil {
			t.Fatalf("a start must not fail over a plugin: %v", err)
		}
	}
	row := func() dbpkg.PluginRow {
		t.Helper()
		p, err := dbpkg.GetPlugin(r.h.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	locked := func() dbpkg.Flow {
		t.Helper()
		list, _ := dbpkg.LockedFlowsOf(r.h.db, id)
		if len(list) != 1 {
			t.Fatalf("%+v", list)
		}
		return list[0]
	}
	before := row()
	r.ok("admin", "PUT", "/api/plugins/"+id+"/settings", map[string]any{"k": "kept"}, nil)
	if err := dbpkg.PluginStorageSet(r.h.db, id, "mine", `"data"`); err != nil {
		t.Fatal(err)
	}
	r.ok("admin", "PUT", "/api/plugins/"+id+"/enabled", map[string]any{"enabled": false}, nil)
	if locked().Enabled {
		t.Fatal("a blueprint starts off")
	}
	second := locked()

	// same version, an origin more, the plugin off: all of that survives
	withB := bundle("1.0.0", b, codeB, blueprint)
	start(withB)
	now := row()
	if now.SHA256 == before.SHA256 || now.Dir == before.Dir || now.Version != "1.0.0" || now.Error != "" {
		t.Fatalf("not upgraded: %+v", now)
	}
	found := false
	for _, g := range now.Granted {
		found = found || g == "server.browser:https://b.example.com"
	}
	if !found {
		t.Fatalf("grants: %v", now.Granted)
	}
	if now.Enabled {
		t.Fatal("off must stay off")
	}
	if locked().Enabled || locked().ID != second.ID {
		t.Fatalf("flow: %+v", locked())
	}
	stored, _ := dbpkg.PluginSettings(r.h.db, id)
	if stored["k"] != `"kept"` {
		t.Fatalf("settings: %v", stored)
	}
	if v, ok, _ := dbpkg.PluginStorageGet(r.h.db, id, "mine"); !ok || v != `"data"` {
		t.Fatalf("data: %v", v)
	}
	// the version it replaced is still there for a hub running on it
	if _, err := os.Stat(before.Dir); err != nil {
		t.Fatalf("the old version: %v", err)
	}

	// a second start does nothing
	start(withB)
	if again := row(); again.SHA256 != now.SHA256 || again.Dir != now.Dir || again.UpdatedAt != now.UpdatedAt {
		t.Fatalf("a second start changed it: %+v", again)
	}

	// enabled, and with a flow on: a new version of the flow, still on
	r.ok("admin", "PUT", "/api/plugins/"+id+"/enabled", map[string]any{"enabled": true}, nil)
	r.ok("user", "PUT", "/api/flows/"+locked().ID+"/enabled", map[string]any{"enabled": true}, nil)
	versionBefore := locked().PublishedVersion
	newGraph := strings.Replace(blueprint, `"x"`, `"x"`, 1) + " "
	start(bundle("1.0.0", b, codeB, newGraph))
	if !row().Enabled || !locked().Enabled || locked().PublishedVersion != versionBefore+1 {
		t.Fatalf("%+v %+v", row(), locked())
	}
	waitFor(t, "the upgraded event", func() bool {
		_, ok, _ := dbpkg.PluginStorageGet(r.h.db, id, "upgraded")
		return ok
	})
	good := row()

	// broken copies: the working one stays, the error shows, and it is still used
	for name, broken := range map[string]fstest.MapFS{
		"code that does not compile":  bundle("1.0.0", b, `module.exports={{{`, blueprint),
		"code that throws on load":    bundle("1.0.0", b, `throw new Error("no")`, blueprint),
		"an invalid manifest":         {"sample/plugin.json": {Data: []byte(`{"id":"lucode.sample","name":"S","version":"1.0.0","sdk":9,"main":"main.js"}`)}, "sample/main.js": {Data: []byte(codeB)}},
		"a blueprint that is invalid": bundle("1.0.0", b, codeB, `{"nodes":[{"id":"a","type":"nope"}],"edges":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			start(broken)
			got := row()
			if got.SHA256 != good.SHA256 || got.Dir != good.Dir || got.Error == "" || !strings.Contains(got.Error, "previous version") {
				t.Fatalf("%+v", got)
			}
			var list []plugins.PluginView
			r.ok("user", "GET", "/api/plugins", nil, &list)
			if len(list) != 1 || list[0].Status != "error" || !list[0].Enabled || list[0].Error == "" {
				t.Fatalf("%+v", list)
			}
			if _, _, err := r.h.plugins.RunNode(context.Background(), plugins.NodeCall{Type: "plugin.lucode.sample.n", ProjectID: r.pid}); err != nil {
				t.Fatalf("the old version must still work: %v", err)
			}
		})
	}
	// and a good copy clears it
	start(bundle("1.0.0", b, codeB, newGraph))
	if got := row(); got.Error != "" || got.SHA256 != good.SHA256 {
		t.Fatalf("%+v", got)
	}

	// an older binary leaves a newer version alone
	start(bundle("2.0.0", b, codeB, blueprint))
	newest := row()
	if newest.Version != "2.0.0" {
		t.Fatalf("%+v", newest)
	}
	start(bundle("1.5.0", a, codeA, blueprint))
	if got := row(); got.Version != "2.0.0" || got.SHA256 != newest.SHA256 || got.Error != "" {
		t.Fatalf("a downgrade: %+v", got)
	}
	if stored, _ := dbpkg.PluginSettings(r.h.db, id); stored["k"] != `"kept"` {
		t.Fatalf("settings: %v", stored)
	}

	// removed, it stays removed, whatever the binary has
	r.ok("admin", "DELETE", "/api/plugins/"+id, nil, nil)
	start(bundle("3.0.0", b, codeB, blueprint))
	if _, err := dbpkg.GetPlugin(r.h.db, id); err == nil {
		t.Fatal("a removed plugin came back")
	}
}

// Where Google sends a first visit to its consent page, the plugin answers it there and goes on.
func TestGeminiThroughTheConsentPage(t *testing.T) {
	g := newFakeGoogle()
	g.consent = true
	r := geminiRig(t, g)
	if err := dbpkg.InsertSession(r.h.db, dbpkg.Session{ID: "c", ProjectID: r.pid, Name: "n", Model: plugins.LegacyAIMode, Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	r.turn("c", plugins.LegacyAIMode, "msg one")
	r.lastAnswer("c", "answer to msg one")
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.consent {
		t.Fatal("the consent page was never answered")
	}
}

func TestGeminiInstallsOnceAndStaysRemoved(t *testing.T) {
	g := newFakeGoogle()
	dir := t.TempDir()
	deps := func() plugins.HubDeps {
		return plugins.HubDeps{
			DataDir: dir, Bundled: bundled.FS, AutoInstall: []string{plugins.GeminiID},
			Browser: func(string) plugins.BrowserDriver { return g },
		}
	}
	r := newPluginRig(t, func(d *plugins.HubDeps) { *d = deps() })
	var list []plugins.PluginView
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 1 || list[0].ID != plugins.GeminiID || list[0].Source != "bundled" || !list[0].Enabled || list[0].Error != "" {
		t.Fatalf("only Gemini installs by itself: %+v", list)
	}
	var cat []plugins.CatalogEntry
	r.ok("user", "GET", "/api/plugins/catalog", nil, &cat)
	if len(cat) != 3 {
		t.Fatalf("%+v", cat)
	}
	// its blueprint is there, and off
	var flowsList []dbpkg.Flow
	r.ok("user", "GET", "/api/projects/"+r.pid+"/flows", nil, &flowsList)
	found := false
	for _, f := range flowsList {
		if f.PluginID == plugins.GeminiID {
			found = true
			if f.Enabled || !f.Locked {
				t.Fatalf("installing never enables a blueprint: %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("the blueprint should be in the project")
	}

	// a restart installs nothing new, and nothing goes twice
	if err := r.h.startPluginHost(context.Background(), deps(), pluginhub.New); err != nil {
		t.Fatal(err)
	}
	r.ok("admin", "DELETE", "/api/plugins/"+plugins.GeminiID, nil, nil)
	if err := r.h.startPluginHost(context.Background(), deps(), pluginhub.New); err != nil {
		t.Fatal(err)
	}
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 0 {
		t.Fatalf("a removed plugin stays removed: %+v", list)
	}
	r.ok("user", "GET", "/api/plugins/catalog", nil, &cat)
	offered := false
	for _, c := range cat {
		offered = offered || (c.ID == plugins.GeminiID && !c.Installed)
	}
	if !offered {
		t.Fatalf("it stays in the catalog: %+v", cat)
	}
	// the model now says why it cannot answer
	sess := dbpkg.Session{ID: "s", ProjectID: r.pid, Name: "n", Model: plugins.LegacyAIMode, Status: "idle"}
	dbpkg.InsertSession(r.h.db, sess)
	code, raw, _ := r.do("user", "POST", "/api/sessions/s/turn", map[string]any{"message": "x"})
	if code != 409 || !strings.Contains(string(raw), "not installed") {
		t.Fatalf("%d %s", code, raw)
	}
	// from the catalog it comes back
	r.ok("admin", "POST", "/api/plugins/catalog/"+plugins.GeminiID+"/install", nil, nil)
	r.ok("user", "GET", "/api/plugins", nil, &list)
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
}

// The old rows are copied whole, twice is the same as once, and the old table is not touched.
func TestAIModeRowsAreCopied(t *testing.T) {
	r := newPluginRig(t)
	insertOldConversation(t, r, "a", "sess-a", "https://www.google.com/search?udm=50&x=a", "first question", 7)
	insertOldConversation(t, r, "b", "sess-b", "https://www.google.com/search?udm=50&x=b", "other", 2)
	count := func(table string) int {
		var n int
		if err := r.h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for round, want := range []int64{2, 0} {
		n, err := dbpkg.CopyAIModeConversations(r.h.db, plugins.GeminiID, "aimode")
		if err != nil || n != want {
			t.Fatalf("round %d: copied %d, %v", round, n, err)
		}
		if got := count("plugin_conversations"); got != 2 {
			t.Fatalf("round %d: %d rows", round, got)
		}
	}
	c, err := dbpkg.GetPluginConversationBySession(r.h.db, plugins.GeminiID, "sess-a")
	if err != nil || c.ID != "a" || c.URL != "https://www.google.com/search?udm=50&x=a" || c.Title != "first question" || c.Turns != 7 || c.ProjectID != r.pid || c.Source != "aimode" {
		t.Fatalf("%+v %v", c, err)
	}
	// a conversation that moved to another session is not copied back
	if err := dbpkg.BindPluginConversation(r.h.db, plugins.GeminiID, "a", "sess-new"); err != nil {
		t.Fatal(err)
	}
	if n, err := dbpkg.CopyAIModeConversations(r.h.db, plugins.GeminiID, "aimode"); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if count("aimode_conversations") != 2 {
		t.Fatal("the old table must stay as it was")
	}
	var oldSession string
	r.h.db.QueryRow(`SELECT session_id FROM aimode_conversations WHERE id='a'`).Scan(&oldSession)
	if oldSession != "sess-a" {
		t.Fatalf("the old row changed: %s", oldSession)
	}

	// the hub's way: once. Removed with its data, the history does not come back
	if err := dbpkg.DeletePlugin(r.h.db, plugins.GeminiID, false); err != nil {
		t.Fatal(err)
	}
	if n, err := dbpkg.CopyAIModeConversationsOnce(r.h.db, plugins.GeminiID, "aimode"); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if err := dbpkg.DeletePlugin(r.h.db, plugins.GeminiID, false); err != nil {
		t.Fatal(err)
	}
	if n, err := dbpkg.CopyAIModeConversationsOnce(r.h.db, plugins.GeminiID, "aimode"); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}

// What the history of the old AI Mode routes did, on the plugin's routes.
func TestConversationHistory(t *testing.T) {
	r := geminiRig(t, newFakeGoogle())
	base := "/api/plugins/" + plugins.GeminiID + "/conversations"
	var first dbpkg.Session
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &first)

	// where the conversation is follows it, and the title stays the first question
	save := func(url, title string, turns int) {
		t.Helper()
		if _, err := dbpkg.SavePluginConversation(r.h.db, dbpkg.PluginConversation{
			PluginID: plugins.GeminiID, SessionID: first.ID, ProjectID: r.pid, Source: "aimode", URL: url, Title: title, Turns: turns,
		}); err != nil {
			t.Fatal(err)
		}
	}
	const last = "https://www.google.com/search?q=a&x=2"
	save("https://www.google.com/search?q=a&x=1", "primera pregunta", 2)
	save(last, "otra", 4)

	// a new orchestrator chat takes over the panel, the old conversation stays in the history
	var second dbpkg.Session
	r.ok("user", "POST", "/api/projects/"+r.pid+"/orchestrator/new", nil, &second)
	var list []pluginConversationRow
	r.ok("user", "GET", base+"?project="+r.pid+"&source=aimode", nil, &list)
	if len(list) != 1 || list[0].Title != "primera pregunta" || list[0].URL != last || list[0].Turns != 4 || list[0].SessionID != first.ID || list[0].Current || !list[0].SessionExists || list[0].SessionKind != dbpkg.SessionKindOrchestrator {
		t.Fatalf("history = %+v", list)
	}

	// continuing it brings its chat back as the current one
	var back dbpkg.Session
	r.ok("user", "POST", base+"/"+list[0].ID+"/continue", nil, &back)
	if back.ID != first.ID {
		t.Fatalf("continue: %+v", back)
	}
	var cur dbpkg.Session
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &cur)
	if cur.ID != first.ID {
		t.Errorf("the continued chat must be the current one, got %s want %s", cur.ID, first.ID)
	}

	// if its chat was deleted, the side panel gets a new orchestrator chat, the one it finds again
	// after a reload, and the next message reopens the same page
	if err := dbpkg.DeleteSession(r.h.db, first.ID); err != nil {
		t.Fatal(err)
	}
	var taken dbpkg.Session
	r.ok("user", "POST", base+"/"+list[0].ID+"/continue", nil, &taken)
	if taken.ID == first.ID || taken.Kind != dbpkg.SessionKindOrchestrator {
		t.Fatalf("continue after delete: %+v", taken)
	}
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &cur)
	if cur.ID != taken.ID {
		t.Errorf("after a reload the panel must find it: got %s want %s", cur.ID, taken.ID)
	}
	got, err := dbpkg.GetPluginConversationBySession(r.h.db, plugins.GeminiID, taken.ID)
	if err != nil || got.URL != last || got.Turns != 4 {
		t.Errorf("the new chat must reopen the same url, got %+v %v", got, err)
	}

	// a plugin's page keeps its chats to itself: the panel's current chat does not change
	if err := dbpkg.DeleteSession(r.h.db, taken.ID); err != nil {
		t.Fatal(err)
	}
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &cur)
	var onPage dbpkg.Session
	r.ok("user", "POST", base+"/"+list[0].ID+"/continue?surface=page", nil, &onPage)
	if onPage.Kind != dbpkg.SessionKindPlugin || onPage.Model != plugins.MappedAIMode {
		t.Fatalf("continue on a page: %+v", onPage)
	}
	var after dbpkg.Session
	r.ok("user", "GET", "/api/projects/"+r.pid+"/orchestrator", nil, &after)
	if after.ID != cur.ID {
		t.Errorf("a page's chat changed the panel's: %s, was %s", after.ID, cur.ID)
	}
	got, err = dbpkg.GetPluginConversationBySession(r.h.db, plugins.GeminiID, onPage.ID)
	if err != nil || got.URL != last {
		t.Errorf("the page's chat must reopen the same url, got %+v %v", got, err)
	}
	// with its chat there, either surface opens that chat
	var again dbpkg.Session
	r.ok("user", "POST", base+"/"+list[0].ID+"/continue", nil, &again)
	if again.ID != onPage.ID {
		t.Errorf("got %+v", again)
	}
	if code, _, _ := r.do("user", "POST", base+"/nope/continue", nil); code != 404 {
		t.Errorf("unknown conversation: %d", code)
	}
	// the old routes are gone
	if code, _, _ := r.do("user", "POST", "/api/aimode/conversations/"+list[0].ID+"/continue", nil); code < 400 {
		t.Errorf("the old route still answers: %d", code)
	}
}
