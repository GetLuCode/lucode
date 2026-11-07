package relayclient

// This client against the real relay: its binary, built from the lucode-webhook-relay repository and
// run on loopback, with this test as Telegram. It checks that the two sides speak the same protocol.
// It only runs when LUCODE_RELAY_REPO names that repository's folder; the process is killed when it ends.
//
//	LUCODE_RELAY_REPO=E:\dev\lucode-webhook-relay go test ./internal/relayclient/ -run Real -v

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"claude-orchestrator/internal/plugins"
)

func TestAgainstTheRealRelay(t *testing.T) {
	repo := os.Getenv("LUCODE_RELAY_REPO")
	if repo == "" {
		t.Skip("set LUCODE_RELAY_REPO to the lucode-webhook-relay folder to run this")
	}
	exe := filepath.Join(t.TempDir(), "webhook-relay")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, "./cmd/webhook-relay")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the relay: %v\n%s", err, out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	const admin = "admin-token-for-this-test"
	cmd := exec.Command(exe, "-listen", addr, "-public-url", "http://"+addr, "-data", t.TempDir())
	cmd.Env = append(os.Environ(), "WEBHOOK_RELAY_ADMIN_TOKEN="+admin)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	base := "http://" + addr
	for i := 0; ; i++ {
		if res, err := http.Get(base + "/healthz"); err == nil {
			res.Body.Close()
			break
		}
		if i > 100 {
			t.Fatal("the relay did not come up")
		}
		time.Sleep(50 * time.Millisecond)
	}

	adminCall := func(method, path string, body any) []byte {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+admin)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		if res.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, buf.String())
		}
		return buf.Bytes()
	}
	var tenant struct{ ID, Key string }
	if err := json.Unmarshal(adminCall("POST", "/admin/tenants", map[string]string{"name": "test hub"}), &tenant); err != nil || tenant.Key == "" {
		t.Fatalf("tenant: %+v %v", tenant, err)
	}

	got := make(chan plugins.RelayEvent, 4)
	cl := New(Config{URL: "ws://" + addr + "/ws", Key: tenant.Key, Tenant: tenant.ID}, true)
	cl.SetHandler(func(_ context.Context, ev plugins.RelayEvent) { got <- ev })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { cl.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "the hub to connect to the real relay", func() bool { return cl.Status().Connected })

	// the plugin's side: an address, and what to give the service
	ep, err := cl.CreateEndpoint(ctx, "telegram", "LuuCodeBot")
	if err != nil {
		t.Fatal(err)
	}
	if ep.ID == "" || len(ep.Secret) != 43 || ep.Provider != "telegram" || ep.Label != "LuuCodeBot" || ep.URL != base+"/wh/telegram/"+ep.ID {
		t.Fatalf("endpoint %+v", ep)
	}

	// the service's side: Telegram posts with the secret it was given
	post := func(secret string) int {
		t.Helper()
		body := `{"update_id":42,"message":{"message_id":1,"chat":{"id":6815799865},"text":"hola"}}`
		req, _ := http.NewRequest("POST", ep.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post("not-the-secret"); code != 401 {
		t.Fatalf("a wrong secret: %d", code)
	}
	if code := post(ep.Secret); code != 200 {
		t.Fatalf("the right secret: %d", code)
	}
	select {
	case ev := <-got:
		if ev.Endpoint != ep.ID || ev.Provider != "telegram" || ev.ContentType != "application/json" || !strings.Contains(ev.Body, `"text":"hola"`) || ev.ReceivedAt == 0 || ev.Seq == 0 {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook never reached the hub")
	}
	// acknowledged once the handler returned: the relay has nothing left for this tenant
	waitFor(t, "the relay to see the ack", func() bool {
		var rows []struct {
			ID      string
			Pending int
		}
		_ = json.Unmarshal(adminCall("GET", "/admin/tenants", nil), &rows)
		for _, r := range rows {
			if r.ID == tenant.ID {
				return r.Pending == 0
			}
		}
		return false
	})

	// giving the address back stops it
	if err := cl.DeleteEndpoint(ctx, ep.ID); err != nil {
		t.Fatal(err)
	}
	if err := cl.DeleteEndpoint(ctx, ep.ID); err != nil {
		t.Fatalf("deleting it twice is fine: %v", err)
	}
	if code := post(ep.Secret); code != 404 {
		t.Fatalf("after deleting: %d", code)
	}
}
