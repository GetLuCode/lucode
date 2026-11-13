package natshub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func start(t *testing.T) (*Hub, string) {
	t.Helper()
	h, err := Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Shutdown)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsUpgrade(r) {
			h.ServeWS(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	return h, "ws://" + strings.TrimPrefix(ts.URL, "http://")
}

func TestRunnerConnectsOverWebSocketWithJetStream(t *testing.T) {
	h, wsURL := start(t)

	hub, err := nats.Connect("", h.ConnectOptions()...)
	if err != nil {
		t.Fatalf("in-process connect: %v", err)
	}
	defer hub.Close()

	// the token travels in the URL, as in `lucode-runner --nats=ws://TOKEN@host:port`
	runner, err := nats.Connect(strings.Replace(wsURL, "ws://", "ws://"+h.Token+"@", 1), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer runner.Close()

	sub, _ := hub.SubscribeSync("t.hello")
	hub.Flush()
	if err := runner.Publish("t.hello", []byte("hola")); err != nil {
		t.Fatal(err)
	}
	if m, err := sub.NextMsg(3 * time.Second); err != nil || string(m.Data) != "hola" {
		t.Fatalf("runner -> hub: %v %v", m, err)
	}

	rsub, _ := runner.SubscribeSync("t.back")
	runner.Flush()
	big := bytes.Repeat([]byte("x"), 900<<10) // a large message crosses frames
	hub.Publish("t.back", big)
	if m, err := rsub.NextMsg(5 * time.Second); err != nil || len(m.Data) != len(big) {
		t.Fatalf("hub -> runner: %v", err)
	}

	js, err := runner.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "T", Subjects: []string{"t.js.>"}}); err != nil {
		t.Fatalf("jetstream over ws: %v", err)
	}
	if _, err := js.Publish("t.js.a", []byte("1")); err != nil {
		t.Fatalf("jetstream publish: %v", err)
	}
	if _, err := hub.Request("t.nobody", nil, 200*time.Millisecond); err == nil {
		t.Fatal("expected no responders")
	}
}

func TestWithoutTheTokenIsRefused(t *testing.T) {
	_, wsURL := start(t)
	if nc, err := nats.Connect(wsURL, nats.Timeout(3*time.Second)); err == nil {
		nc.Close()
		t.Fatal("connected without a token")
	}
	if nc, err := nats.Connect(strings.Replace(wsURL, "ws://", "ws://wrong@", 1), nats.Timeout(3*time.Second)); err == nil {
		nc.Close()
		t.Fatal("connected with a wrong token")
	}
}
