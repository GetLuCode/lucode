package relayclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"claude-orchestrator/internal/plugins"

	"github.com/gorilla/websocket"
)

// fakeRelay speaks the relay's protocol, as much as the client needs. Each connection is handed to
// onConn, which plays the relay's part.
type fakeRelay struct {
	t      *testing.T
	srv    *httptest.Server
	key    string
	conns  atomic.Int32
	onConn func(c *websocket.Conn, n int)
}

func newFakeRelay(t *testing.T, key string, onConn func(c *websocket.Conn, n int)) *fakeRelay {
	f := &fakeRelay{t: t, key: key, onConn: onConn}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		n := int(f.conns.Add(1))
		f.onConn(c, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelay) wsURL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws" }

func hello(c *websocket.Conn, pending int) {
	_ = c.WriteJSON(map[string]any{"op": "hello", "tenant": "t1", "name": "marco-hub", "pending": pending})
}

func readFrame(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		return nil // the connection ended: normal when a test finishes, and no place to fail from
	}
	return m
}

func fast(t *testing.T) {
	t.Helper()
	oldMin, oldMax, oldRef, oldReq := minBackoff, maxBackoff, refusedBackoff, requestTimeout
	minBackoff, maxBackoff, refusedBackoff, requestTimeout = 20*time.Millisecond, 80*time.Millisecond, 200*time.Millisecond, 2*time.Second
	t.Cleanup(func() { minBackoff, maxBackoff, refusedBackoff, requestTimeout = oldMin, oldMax, oldRef, oldReq })
}

func start(t *testing.T, url, key string) (*Client, context.CancelFunc) {
	t.Helper()
	fast(t)
	cl := New(Config{URL: url, Key: key, Tenant: "t1"}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { cl.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop when its context ended")
		}
	})
	return cl, cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestConnectsWithItsKeyAndSaysSo(t *testing.T) {
	hold := make(chan struct{})
	f := newFakeRelay(t, "lrk_good", func(c *websocket.Conn, n int) { hello(c, 0); <-hold })
	defer close(hold)
	cl, _ := start(t, f.wsURL(), "lrk_good")
	waitFor(t, "connected", func() bool { return cl.Status().Connected })
	st := cl.Status()
	if !st.Configured || st.URL != f.wsURL() {
		t.Fatalf("status %+v", st)
	}
	if strings.Contains(strings.ToLower(f.wsURL()), "lrk_") {
		t.Fatal("the key must not be in the address")
	}
}

func TestAWrongKeyIsRefusedAndRetriedSlowly(t *testing.T) {
	f := newFakeRelay(t, "lrk_good", func(c *websocket.Conn, n int) {})
	cl, _ := start(t, f.wsURL(), "lrk_bad")
	time.Sleep(150 * time.Millisecond)
	if cl.Status().Connected {
		t.Fatal("must not be connected with a wrong key")
	}
	if _, err := cl.CreateEndpoint(context.Background(), "telegram", "x"); err != ErrNotConnected {
		t.Fatalf("a request needs a connection: %v", err)
	}
}

func TestCreateAndDeleteAnEndpoint(t *testing.T) {
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 0)
		for {
			m := readFrame(t, c)
			if m == nil {
				return
			}
			switch m["op"] {
			case "create_endpoint":
				if m["provider"] == "nope" {
					_ = c.WriteJSON(map[string]any{"op": "error", "req": m["req"], "message": "unknown provider nope"})
					continue
				}
				_ = c.WriteJSON(map[string]any{"op": "endpoint", "req": m["req"], "id": "ep1", "provider": m["provider"], "label": m["label"], "url": "https://relay.example.com/wh/telegram/ep1", "secret": "s3cret-value"})
			case "delete_endpoint":
				if m["id"] == "gone" {
					_ = c.WriteJSON(map[string]any{"op": "error", "req": m["req"], "message": "no such endpoint"})
					continue
				}
				_ = c.WriteJSON(map[string]any{"op": "deleted", "req": m["req"], "id": m["id"]})
			}
		}
	})
	cl, _ := start(t, f.wsURL(), "k1")
	waitFor(t, "connected", func() bool { return cl.Status().Connected })
	ctx := context.Background()

	ep, err := cl.CreateEndpoint(ctx, "telegram", "LuuCodeBot")
	if err != nil {
		t.Fatal(err)
	}
	if ep.ID != "ep1" || ep.Secret != "s3cret-value" || ep.Label != "LuuCodeBot" || !strings.HasSuffix(ep.URL, "/wh/telegram/ep1") {
		t.Fatalf("endpoint %+v", ep)
	}
	if _, err := cl.CreateEndpoint(ctx, "nope", ""); err == nil || !strings.Contains(err.Error(), "unknown provider nope") {
		t.Fatalf("the relay's refusal reaches the caller: %v", err)
	}
	if err := cl.DeleteEndpoint(ctx, "ep1"); err != nil {
		t.Fatal(err)
	}
	if err := cl.DeleteEndpoint(ctx, "gone"); err != nil {
		t.Fatalf("an address the relay no longer has is deleted already: %v", err)
	}
}

func TestAnEventIsAcknowledgedOnlyAfterItsHandlerReturns(t *testing.T) {
	acks := make(chan uint64, 4)
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 1)
		_ = c.WriteJSON(map[string]any{"op": "event", "seq": 7, "endpoint": "ep1", "provider": "telegram", "received_at": 1700000000000, "content_type": "application/json", "body": `{"update_id":1}`})
		for {
			m := readFrame(t, c)
			if m == nil {
				return
			}
			if m["op"] == "ack" {
				acks <- uint64(m["seq"].(float64))
			}
		}
	})
	release := make(chan struct{})
	got := make(chan plugins.RelayEvent, 1)
	cl := New(Config{URL: f.wsURL(), Key: "k1"}, true)
	cl.SetHandler(func(_ context.Context, ev plugins.RelayEvent) { got <- ev; <-release })
	fast(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)

	select {
	case ev := <-got:
		if ev.Seq != 7 || ev.Endpoint != "ep1" || ev.Provider != "telegram" || ev.Body != `{"update_id":1}` || ev.ContentType != "application/json" || ev.ReceivedAt != 1700000000000 {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never got the event")
	}
	select {
	case a := <-acks:
		t.Fatalf("acknowledged %d before the handler returned", a)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case a := <-acks:
		if a != 7 {
			t.Fatalf("acked %d", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no ack after the handler returned")
	}
}

func TestAnEventWithoutAHandlerIsNotAcknowledged(t *testing.T) {
	acks := make(chan uint64, 1)
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 1)
		_ = c.WriteJSON(map[string]any{"op": "event", "seq": 1, "endpoint": "e", "provider": "telegram", "body": "{}"})
		for {
			m := readFrame(t, c)
			if m == nil {
				return
			}
			if m["op"] == "ack" {
				acks <- 1
			}
		}
	})
	start(t, f.wsURL(), "k1") // no SetHandler
	select {
	case <-acks:
		t.Fatal("nobody took it: the relay must keep it")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAPanickingHandlerStillGetsItsEventAcknowledged(t *testing.T) {
	acks := make(chan uint64, 2)
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 2)
		_ = c.WriteJSON(map[string]any{"op": "event", "seq": 1, "endpoint": "e", "provider": "telegram", "body": "{}"})
		_ = c.WriteJSON(map[string]any{"op": "event", "seq": 2, "endpoint": "e", "provider": "telegram", "body": "{}"})
		for {
			m := readFrame(t, c)
			if m == nil {
				return
			}
			if m["op"] == "ack" {
				acks <- uint64(m["seq"].(float64))
			}
		}
	})
	cl := New(Config{URL: f.wsURL(), Key: "k1"}, true)
	cl.SetHandler(func(_ context.Context, ev plugins.RelayEvent) {
		if ev.Seq == 1 {
			panic("a plugin bug")
		}
	})
	fast(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	for want := uint64(1); want <= 2; want++ {
		select {
		case a := <-acks:
			if a != want {
				t.Fatalf("ack %d, want %d (in order)", a, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no ack for %d", want)
		}
	}
}

func TestItReconnectsAndTheRelayResendsWhatWasNotAcknowledged(t *testing.T) {
	var mu sync.Mutex
	var handled []uint64
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 1)
		_ = c.WriteJSON(map[string]any{"op": "event", "seq": 1, "endpoint": "e", "provider": "telegram", "body": "{}"})
		if n == 1 {
			return // the relay drops the connection before an ack: the event is still owed
		}
		for {
			if readFrame(t, c) == nil {
				return
			}
		}
	})
	cl := New(Config{URL: f.wsURL(), Key: "k1"}, true)
	cl.SetHandler(func(_ context.Context, ev plugins.RelayEvent) {
		mu.Lock()
		handled = append(handled, ev.Seq)
		mu.Unlock()
	})
	fast(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	waitFor(t, "a second connection", func() bool { return f.conns.Load() >= 2 })
	waitFor(t, "the event handled again", func() bool { mu.Lock(); defer mu.Unlock(); return len(handled) >= 2 })
	waitFor(t, "connected again", func() bool { return cl.Status().Connected })
}

func TestARequestFailsFastWhenTheConnectionIsLost(t *testing.T) {
	f := newFakeRelay(t, "k1", func(c *websocket.Conn, n int) {
		hello(c, 0)
		if m := readFrame(t, c); m != nil {
			return // closes without answering
		}
	})
	cl, _ := start(t, f.wsURL(), "k1")
	waitFor(t, "connected", func() bool { return cl.Status().Connected })
	start := time.Now()
	_, err := cl.CreateEndpoint(context.Background(), "telegram", "")
	if err != ErrNotConnected {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("it should not wait out the whole timeout")
	}
}

func TestNoRelayConfiguredSaysSo(t *testing.T) {
	cl := New(Config{}, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cl.Run(ctx) // returns at once
	if ctx.Err() != nil {
		t.Fatal("Run should return at once with no relay")
	}
	if st := cl.Status(); st.Configured || st.Connected || st.URL != "" {
		t.Fatalf("status %+v", st)
	}
	if _, err := cl.CreateEndpoint(context.Background(), "telegram", ""); err == nil || !strings.Contains(err.Error(), "relay.json") {
		t.Fatalf("it should say where to set it up: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, ok, err := LoadConfig(filepath.Join(dir, "missing.json")); ok || err != nil {
		t.Fatalf("no file means no relay, and no error: %v %v", ok, err)
	}
	cfg, ok, err := LoadConfig(write("ok.json", `{"url":"wss://relay.example.com/ws","tenant":"t1","name":"marco-hub","key":"lrk_abc"}`))
	if !ok || err != nil || cfg.Key != "lrk_abc" || cfg.Tenant != "t1" {
		t.Fatalf("%+v %v %v", cfg, ok, err)
	}
	if _, ok, err := LoadConfig(write("local.json", `{"url":"ws://127.0.0.1:9/ws","key":"k"}`)); !ok || err != nil {
		t.Fatalf("ws to this machine is fine: %v", err)
	}
	for name, body := range map[string]string{
		"remote-ws":  `{"url":"ws://relay.example.com/ws","key":"k"}`,
		"http":       `{"url":"https://relay.example.com/ws","key":"k"}`,
		"no-key":     `{"url":"wss://relay.example.com/ws","key":" "}`,
		"no-address": `{"url":"","key":"k"}`,
		"not-json":   `{`,
	} {
		if _, ok, err := LoadConfig(write(name+".json", body)); ok || err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

func TestTheKeyNeverShowsInAnError(t *testing.T) {
	// nothing listens here: the dial error names the address, never the key
	cl := New(Config{URL: "ws://127.0.0.1:1/ws", Key: "lrk_very_secret"}, true)
	err := cl.serve(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "lrk_very_secret") {
		t.Fatalf("the error shows the key: %v", err)
	}
	var b []byte
	b, _ = json.Marshal(cl.Status())
	if strings.Contains(string(b), "lrk_very_secret") {
		t.Fatal("the status shows the key")
	}
}
