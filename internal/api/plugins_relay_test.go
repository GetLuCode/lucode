package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	"claude-orchestrator/internal/plugins"
	pluginhub "claude-orchestrator/internal/plugins/hub"
)

// Webhooks through the relay, against the real host, real plugin code and a fake relay client: no
// network. The relay client itself is tested in internal/relayclient.

type fakeRelay struct {
	mu        sync.Mutex
	handler   func(context.Context, plugins.RelayEvent)
	next      int
	created   []plugins.RelayEndpoint
	deleted   []string
	connected bool
	failWith  error
}

func newFakeRelay() *fakeRelay { return &fakeRelay{connected: true} }

func (f *fakeRelay) Status() plugins.RelayStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return plugins.RelayStatus{Configured: true, Connected: f.connected, URL: "wss://relay.test/ws"}
}

func (f *fakeRelay) CreateEndpoint(_ context.Context, provider, label string) (plugins.RelayEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return plugins.RelayEndpoint{}, f.failWith
	}
	f.next++
	id := "ep" + string(rune('a'+f.next))
	ep := plugins.RelayEndpoint{ID: id, Provider: provider, Label: label, URL: "https://relay.test/wh/" + provider + "/" + id, Secret: "relay-secret-" + id}
	f.created = append(f.created, ep)
	return ep, nil
}

func (f *fakeRelay) DeleteEndpoint(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeRelay) SetHandler(fn func(context.Context, plugins.RelayEvent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = fn
}

// deliver is the relay client handing a webhook to the host, as it does when one comes down the websocket.
func (f *fakeRelay) deliver(ev plugins.RelayEvent) {
	f.mu.Lock()
	fn := f.handler
	f.mu.Unlock()
	fn(context.Background(), ev)
}

func (f *fakeRelay) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.deleted...)
}

func hookManifest(id string, providers string) string {
	return `{"id":"` + id + `","name":"Hook","version":"1.0.0","sdk":1,"description":"d",
  "targets":{"server":{"main":"main.js","permissions":{"storage":true,"flows":true,"relay":{"providers":` + providers + `}}}},
  "contributes":{}}`
}

const hookCode = `
module.exports = {
  rpc: {
    connect(a) { var ep = lucode.relay.createEndpoint({ provider: a.provider || "telegram", label: a.label || "bot" }); lucode.storage.set("ep", ep.id); return ep },
    status() { return lucode.relay.status() },
    list() { return { endpoints: lucode.relay.listEndpoints() } },
    del(a) { lucode.relay.deleteEndpoint(a.id); return { ok: true } },
    last() { return { last: lucode.storage.get("last") } },
    leak(a) { var ep = lucode.relay.createEndpoint({ provider: "telegram" }); lucode.log.info("secret is", ep.secret); return { secret: ep.secret } },
  },
  events: {
    webhook(e) { lucode.storage.set("last", e); var u = JSON.parse(e.body); lucode.flows.emit("message", { text: u.text }) },
  },
};`

func hookFiles(id string) map[string]string {
	return map[string]string{"plugin.json": hookManifest(id, `["telegram"]`), "main.js": hookCode}
}

func relayRig(t *testing.T) (*pluginRig, *fakeRelay) {
	t.Helper()
	fake := newFakeRelay()
	r := newPluginRig(t, func(d *plugins.HubDeps) { d.Relay = fake })
	return r, fake
}

func allowHook(r *pluginRig, id string, names ...string) {
	for _, n := range names {
		pluginhub.AllowRPCForTest(r.h.plugins, id, n)
	}
}

func hookRPC(r *pluginRig, id, name string, args any) (int, string) {
	r.t.Helper()
	code, raw, _ := r.do("user", "POST", "/api/plugins/"+id+"/rpc/"+name, map[string]any{"projectId": r.pid, "args": args})
	return code, string(raw)
}

func telegramUpdate(text string) plugins.RelayEvent {
	return plugins.RelayEvent{Seq: 1, Provider: "telegram", ReceivedAt: 1700000000000, ContentType: "application/json",
		Body: `{"update_id":1,"text":"` + text + `"}`}
}

func TestAPluginGetsAnAddressAndItsWebhooks(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect", "status", "list", "last", "del")

	// the relay's state, as the plugin sees it (never the key)
	if code, raw := hookRPC(r, "acme.hook", "status", nil); code != 200 || !strings.Contains(raw, `"connected":true`) || !strings.Contains(raw, "wss://relay.test/ws") {
		t.Fatalf("status: %d %s", code, raw)
	}

	// it asks for an address: the secret comes back once, the address is kept
	code, raw := hookRPC(r, "acme.hook", "connect", map[string]any{"label": "LuuCodeBot"})
	if code != 200 {
		t.Fatalf("connect: %d %s", code, raw)
	}
	var ep struct{ ID, URL, Secret, ProjectID, Provider, Label string }
	if err := json.Unmarshal([]byte(raw), &ep); err != nil {
		t.Fatal(err)
	}
	if ep.ID == "" || ep.Secret == "" || !strings.HasSuffix(ep.URL, "/wh/telegram/"+ep.ID) || ep.ProjectID != r.pid || ep.Provider != "telegram" || ep.Label != "LuuCodeBot" {
		t.Fatalf("endpoint %+v", ep)
	}
	row, err := dbpkg.GetRelayEndpoint(r.h.db, ep.ID)
	if err != nil || row.PluginID != "acme.hook" || row.ProjectID != r.pid {
		t.Fatalf("row %+v %v", row, err)
	}
	if code, raw := hookRPC(r, "acme.hook", "list", nil); code != 200 || !strings.Contains(raw, ep.ID) || strings.Contains(raw, ep.Secret) {
		t.Fatalf("a listing has the address and never its secret: %d %s", code, raw)
	}

	// a webhook for it runs the plugin's events.webhook, in the endpoint's project
	f := publishFlow(t, r.h, r.pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "e", Type: flows.TypePluginEvent, Config: map[string]any{"plugin": "acme.hook", "event": "message"}},
			{ID: "m", Type: flows.TypeCreateTask, Config: map[string]any{"title": "from telegram: {{event.payload.text}}"}},
		},
		Edges: []flows.Edge{{From: "e", Port: "next", To: "m"}},
	})
	ev := telegramUpdate("hola")
	ev.Endpoint = ep.ID
	fake.deliver(ev)
	if code, raw := hookRPC(r, "acme.hook", "last", nil); code != 200 {
		t.Fatalf("last: %d %s", code, raw)
	} else {
		var got struct{ Last map[string]any }
		_ = json.Unmarshal([]byte(raw), &got)
		if got.Last["endpoint"] != ep.ID || got.Last["provider"] != "telegram" || got.Last["projectId"] != r.pid || got.Last["contentType"] != "application/json" || got.Last["label"] != "LuuCodeBot" {
			t.Fatalf("what the handler got: %v", got.Last)
		}
		if body, _ := got.Last["body"].(string); body != ev.Body {
			t.Fatalf("body %q", body)
		}
	}
	// and the flow it fires, the blueprint way
	settleFlows(r)
	if run := lastRun(t, r.h, f); run.Status != dbpkg.RunSucceeded {
		t.Fatalf("run %s %q", run.Status, run.Error)
	}
	cards, _ := dbpkg.ListKanbanCards(r.h.db, r.pid, false)
	if len(cards) != 1 || cards[0].Title != "from telegram: hola" {
		t.Fatalf("%+v", cards)
	}

	// giving the address back
	if code, raw := hookRPC(r, "acme.hook", "del", map[string]any{"id": ep.ID}); code != 200 {
		t.Fatalf("del: %d %s", code, raw)
	}
	if got := fake.deletedIDs(); len(got) != 1 || got[0] != ep.ID {
		t.Fatalf("the relay was asked to delete %v", got)
	}
	if _, err := dbpkg.GetRelayEndpoint(r.h.db, ep.ID); err == nil {
		t.Fatal("the row should be gone")
	}
}

// The browser hears of a webhook over the websocket the moment the plugin has handled it, without the
// body: that is what makes a plugin's page show a message as it arrives.
func TestAWebhookIsAnnouncedToTheBrowsers(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect")
	var mu sync.Mutex
	var heard []map[string]any
	r.h.bus.OnBroadcast = func(data []byte) {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "plugin_webhook" {
			mu.Lock()
			heard = append(heard, m)
			mu.Unlock()
		}
	}
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)

	ev := telegramUpdate("a secret message")
	ev.Endpoint = ep.ID
	fake.deliver(ev)

	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 1 {
		t.Fatalf("the browsers should hear of it once: %v", heard)
	}
	got := heard[0]
	if got["pluginId"] != "acme.hook" || got["provider"] != "telegram" || got["endpoint"] != ep.ID || got["projectId"] != r.pid || got["receivedAt"].(float64) != 1700000000000 {
		t.Fatalf("what the browsers heard: %v", got)
	}
	if _, has := got["body"]; has {
		t.Fatal("the body must not go to every browser")
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "a secret message") {
		t.Fatal("what was written must not go to every browser")
	}
}

// A webhook nobody owns, or for a plugin that is off, tells the browsers nothing.
func TestADroppedWebhookIsNotAnnounced(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect")
	var mu sync.Mutex
	n := 0
	r.h.bus.OnBroadcast = func(data []byte) {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "plugin_webhook" {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}
	unknown := telegramUpdate("x")
	unknown.Endpoint = "nobody-owns-this"
	fake.deliver(unknown)
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)
	r.ok("admin", "PUT", "/api/plugins/acme.hook/enabled", map[string]any{"enabled": false}, nil)
	off := telegramUpdate("x")
	off.Endpoint = ep.ID
	fake.deliver(off)
	mu.Lock()
	defer mu.Unlock()
	if n != 0 {
		t.Fatalf("%d announcements for webhooks that went nowhere", n)
	}
}

func TestRelayPermissionIsPerProvider(t *testing.T) {
	r, _ := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect")
	code, raw := hookRPC(r, "acme.hook", "connect", map[string]any{"provider": "github"})
	if code == 200 || !strings.Contains(raw, "not granted the relay permission for github") {
		t.Fatalf("%d %s", code, raw)
	}
}

// The permission is on the review dialog, and a plugin is installed with all it asks for or not at all.
func TestTheRelayPermissionIsReviewedAndRequired(t *testing.T) {
	r, fake := relayRig(t)
	code, raw := r.inspect("admin", pluginZip(t, hookFiles("acme.hook")))
	if code != 200 {
		t.Fatalf("inspect: %d %s", code, raw)
	}
	var res plugins.InspectResult
	_ = json.Unmarshal(raw, &res)
	var keys []string
	sawRelay := false
	for _, p := range res.Permissions {
		if strings.Contains(p.Key, "relay") {
			sawRelay = true
			if p.Key != "server.relay:telegram" || p.Target != "server" || p.Risk != "medium" || !strings.Contains(p.Label, "telegram") {
				t.Fatalf("the review line: %+v", p)
			}
			continue
		}
		keys = append(keys, p.Key)
	}
	if !sawRelay {
		t.Fatalf("the review dialog must list the relay permission: %+v", res.Permissions)
	}
	code, raw, _ = r.do("admin", "POST", "/api/plugins/install", map[string]any{"token": res.Token, "granted": keys})
	if code != 400 || !strings.Contains(string(raw), "server.relay:telegram") {
		t.Fatalf("without the relay grant it must not install: %d %s", code, raw)
	}
	if len(fake.created) != 0 {
		t.Fatal("nothing may reach the relay without the grant")
	}
}

func TestOnePluginCannotTouchAnothersAddress(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	r.install(hookFiles("acme.other"))
	allowHook(r, "acme.hook", "connect")
	allowHook(r, "acme.other", "del", "list")
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)

	code, raw := hookRPC(r, "acme.other", "del", map[string]any{"id": ep.ID})
	if code == 200 || !strings.Contains(raw, "no such webhook endpoint") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(fake.deletedIDs()) != 0 {
		t.Fatal("the relay must not be asked to delete it")
	}
	if code, raw := hookRPC(r, "acme.other", "list", nil); code != 200 || strings.Contains(raw, ep.ID) {
		t.Fatalf("another plugin's listing: %d %s", code, raw)
	}
}

func TestWebhooksThatHaveNowhereToGoAreDropped(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect", "last")
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)
	last := func() string { _, raw := hookRPC(r, "acme.hook", "last", nil); return raw }

	// an address nobody owns
	unknown := telegramUpdate("x")
	unknown.Endpoint = "not-an-endpoint"
	fake.deliver(unknown)
	// the wrong provider for the address
	wrong := telegramUpdate("x")
	wrong.Endpoint, wrong.Provider = ep.ID, "github"
	fake.deliver(wrong)
	if !strings.Contains(last(), `"last":null`) {
		t.Fatalf("nothing should have reached the plugin: %s", last())
	}

	// a plugin that is off gets nothing
	r.ok("admin", "PUT", "/api/plugins/acme.hook/enabled", map[string]any{"enabled": false}, nil)
	off := telegramUpdate("while off")
	off.Endpoint = ep.ID
	fake.deliver(off)
	r.ok("admin", "PUT", "/api/plugins/acme.hook/enabled", map[string]any{"enabled": true}, nil)
	allowHook(r, "acme.hook", "last") // a reload forgets which calls a page offered
	if !strings.Contains(last(), `"last":null`) {
		t.Fatalf("a disabled plugin must get nothing: %s", last())
	}

	// and on again it does
	on := telegramUpdate("back on")
	on.Endpoint = ep.ID
	fake.deliver(on)
	if !strings.Contains(last(), "back on") {
		t.Fatalf("%s", last())
	}
}

func TestAPluginThatThrowsOnAWebhookDoesNotBreakTheHost(t *testing.T) {
	r, fake := relayRig(t)
	files := hookFiles("acme.hook")
	files["main.js"] = strings.Replace(hookCode, `webhook(e) {`, `webhook(e) { throw new Error("handler failed");`, 1)
	r.install(files)
	allowHook(r, "acme.hook", "connect")
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)
	ev := telegramUpdate("x")
	ev.Endpoint = ep.ID
	fake.deliver(ev) // returns: the client would acknowledge it
	if code, _ := hookRPC(r, "acme.hook", "connect", nil); code != 200 {
		t.Fatal("the plugin still works afterwards")
	}
}

func TestTheRelaySecretNeverLeavesThroughALogOrAResult(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "leak")
	code, raw := hookRPC(r, "acme.hook", "leak", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	secret := fake.created[0].Secret
	if strings.Contains(raw, secret) {
		t.Fatalf("a result must not show the relay's secret: %s", raw)
	}
	code, logs, _ := r.do("admin", "GET", "/api/plugins/acme.hook/logs", nil)
	if code != 200 || strings.Contains(string(logs), secret) {
		t.Fatalf("a log must not show it: %d %s", code, logs)
	}
}

func TestAPluginHoldsAtMostTenAddresses(t *testing.T) {
	r, _ := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect")
	for i := 0; i < 10; i++ {
		if code, raw := hookRPC(r, "acme.hook", "connect", nil); code != 200 {
			t.Fatalf("%d: %d %s", i, code, raw)
		}
	}
	if code, raw := hookRPC(r, "acme.hook", "connect", nil); code == 200 || !strings.Contains(raw, "at most 10") {
		t.Fatalf("the eleventh: %d %s", code, raw)
	}
}

func TestRelayFailuresReachThePluginInPlainWords(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect", "list")
	fake.failWith = errString("the webhook relay is not connected")
	code, raw := hookRPC(r, "acme.hook", "connect", nil)
	if code == 200 || !strings.Contains(raw, "the webhook relay is not connected") {
		t.Fatalf("%d %s", code, raw)
	}
	if code, raw := hookRPC(r, "acme.hook", "list", nil); code != 200 || strings.Contains(raw, `"id"`) {
		t.Fatalf("no row for an address that was never made: %d %s", code, raw)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestUninstallGivesTheAddressesBack(t *testing.T) {
	r, fake := relayRig(t)
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "connect")
	_, raw := hookRPC(r, "acme.hook", "connect", nil)
	var ep struct{ ID string }
	_ = json.Unmarshal([]byte(raw), &ep)
	if code, raw, _ := r.do("admin", "DELETE", "/api/plugins/acme.hook", nil); code != 204 {
		t.Fatalf("%d %s", code, raw)
	}
	waitFor(t, "the relay to be told", func() bool { return len(fake.deletedIDs()) == 1 })
	if fake.deletedIDs()[0] != ep.ID {
		t.Fatalf("%v", fake.deletedIDs())
	}
	if _, err := dbpkg.GetRelayEndpoint(r.h.db, ep.ID); err == nil {
		t.Fatal("the row should be gone with the plugin")
	}
}

func TestAHubWithoutARelaySaysSo(t *testing.T) {
	r := newPluginRig(t) // no relay client at all
	r.install(hookFiles("acme.hook"))
	allowHook(r, "acme.hook", "status", "connect")
	if code, raw := hookRPC(r, "acme.hook", "status", nil); code != 200 || !strings.Contains(raw, `"configured":false`) {
		t.Fatalf("%d %s", code, raw)
	}
	if code, raw := hookRPC(r, "acme.hook", "connect", nil); code == 200 || !strings.Contains(raw, "no webhook relay") {
		t.Fatalf("%d %s", code, raw)
	}
}
