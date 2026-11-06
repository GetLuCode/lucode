package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-orchestrator/internal/cdp"
	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
)

// TestPushLive is the whole way for real: the built web in a real Edge with a throwaway profile,
// a real subscription, a published flow, and the browser's real push service. It sends one
// encrypted test message over the internet and costs no tokens, so it only runs on request:
//
//	cd web && npx parcel build index.html --dist-dir <dir> --cache-dir <dir>/.cache
//	LUCODE_PUSH_LIVE=1 LUCODE_PUSH_DIST=<dir> go test ./internal/api/ -run TestPushLive -v
//
// Edge goes through Windows Push. LUCODE_EDGE_BIN=<chrome.exe> tries Google's service instead,
// the one Android uses. A window opens for a few seconds, and a notification may show.
func TestPushLive(t *testing.T) {
	dist := os.Getenv("LUCODE_PUSH_DIST")
	if os.Getenv("LUCODE_PUSH_LIVE") != "1" || dist == "" {
		t.Skip("set LUCODE_PUSH_LIVE=1 and LUCODE_PUSH_DIST to talk to a real push service")
	}
	h, pid, _, _ := flowHarness(t)
	keyFile := filepath.Join(t.TempDir(), "flows-push.json")
	prev := flowPushPath
	flowPushPath = func() string { return keyFile }
	t.Cleanup(func() { flowPushPath = prev })
	h.pushIcon = findPushIcon(os.DirFS(dist))

	mux := http.NewServeMux()
	h.registerFlows(mux)
	mux.Handle("/", spaHandler(os.DirFS(dist)))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	origin := "http://" + ln.Addr().String()

	// allowed beforehand: nobody is there to click the browser's question
	profile, err := os.MkdirTemp("", "lucode-push-live-*")
	if err != nil {
		t.Fatal(err)
	}
	prefs, _ := json.Marshal(map[string]any{"profile": map[string]any{"content_settings": map[string]any{"exceptions": map[string]any{
		"notifications": map[string]any{origin + ",*": map[string]any{"setting": 1}},
	}}}})
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Default", "Preferences"), prefs, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(profile, "state.json"))

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	browser := cdp.New("push-live", port)
	t.Cleanup(func() {
		t.Log(browser.Close())
		time.Sleep(time.Second) // Edge lets go of its profile
		os.RemoveAll(profile)
	})
	// With a window: a headless browser subscribes, but its messages never arrive.
	if _, err := browser.Launch(cdp.LaunchOptions{Port: port, ProfileDir: profile, URL: origin + "/"}); err != nil {
		t.Fatal(err)
	}

	if _, err := browser.Navigate(origin + "/"); err != nil {
		t.Fatal(err)
	}

	// A promise's result, read by polling: the page keeps it until it is ready.
	await := func(name, script string, limit time.Duration) string {
		t.Helper()
		start := fmt.Sprintf(`window.__r=undefined;(async()=>{%s})().then(v=>{window.__r='ok:'+v},e=>{window.__r='err:'+e})&&'started'`, script)
		if _, err := browser.Eval(start); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			v, err := browser.Eval(`String(window.__r)`)
			if err == nil && strings.HasPrefix(v, "ok:") {
				return strings.TrimPrefix(v, "ok:")
			}
			if err == nil && strings.HasPrefix(v, "err:") {
				t.Fatalf("%s: %s", name, v)
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: no answer in %s", name, limit)
		return ""
	}

	t.Log("page:", await("page", `return location.href + ' secure=' + isSecureContext`, 10*time.Second))
	t.Log("permission:", await("permission", `return Notification.permission`, 10*time.Second))
	t.Log("worker:", await("worker", `return (await navigator.serviceWorker.ready).active.scriptURL`, 30*time.Second))
	key, err := flowPushKeys()
	if err != nil {
		t.Fatal(err)
	}
	// the same steps as web/src/lib/push.ts, with the hub's real API
	host := await("subscribe", fmt.Sprintf(`
		const reg = await navigator.serviceWorker.ready
		const raw = %q.replace(/-/g,'+').replace(/_/g,'/')
		const bytes = Uint8Array.from(atob(raw.padEnd(Math.ceil(raw.length/4)*4,'=')), c => c.charCodeAt(0))
		const sub = await reg.pushManager.subscribe({userVisibleOnly:true, applicationServerKey:bytes})
		const res = await fetch('/api/flow-push/subscription',{method:'PUT',body:JSON.stringify(sub.toJSON())})
		if (!res.ok) throw new Error(await res.text())
		return new URL(sub.endpoint).host`, key.Public), time.Minute)
	t.Log("push service:", host)

	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeManual},
			{ID: "p", Type: flows.TypePushNotification, Config: map[string]any{"title": "Live título", "body": "Run {{run.depth}} ñandú 🚀"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortNext, To: "p"}},
	})
	if f, err = dbpkg.GetFlow(h.db, f.ID); err != nil {
		t.Fatal(err)
	}
	run, err := h.flows.StartManual(f, "")
	if err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	h.flows.WaitForEvaluations()
	h.flows.Drain()
	if run, err = dbpkg.GetFlowRun(h.db, run.ID); err != nil {
		t.Fatal(err)
	}
	if run.Status != dbpkg.RunSucceeded {
		t.Fatalf("the push service refused it: %s %s", run.Status, run.Error)
	}
	t.Log("the push service accepted it")

	shown := await("notification", `
		const reg = await navigator.serviceWorker.ready
		for (let i = 0; i < 300; i++) {
			const list = await reg.getNotifications()
			if (list.length) return JSON.stringify({title:list[0].title, body:list[0].body, tag:list[0].tag, icon:list[0].icon, url:list[0].data && list[0].data.url})
			await new Promise(r => setTimeout(r, 200))
		}
		throw new Error('no notification was shown')`, 70*time.Second)
	t.Log("shown:", shown)
	var got map[string]string
	if err := json.Unmarshal([]byte(shown), &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Live título" || got["body"] != "Run 0 ñandú 🚀" || !strings.HasPrefix(got["tag"], "flow-step-") {
		t.Fatalf("shown: %+v", got)
	}
	if want := "/projects/" + pid + "/flows?flow=" + f.ID + "&run=" + run.ID; got["url"] != want {
		t.Fatalf("url %q, want %q", got["url"], want)
	}
	if !strings.Contains(got["icon"], "icon-192") {
		t.Fatalf("icon %q", got["icon"])
	}
}
