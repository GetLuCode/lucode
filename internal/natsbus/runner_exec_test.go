package natsbus

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// These tests talk to a real NATS server (the one lucode uses in development) and
// are skipped when none is listening.
func testBus(t *testing.T, id string) *Bus { return testBusAt(t, "nats://127.0.0.1:4222", id) }

func testBusAt(t *testing.T, url, id string) *Bus {
	t.Helper()
	nc, err := nats.Connect(url, nats.Timeout(time.Second))
	if err != nil {
		t.Skipf("no NATS server on %s: %v", url, err)
	}
	t.Cleanup(nc.Close)
	b := NewBus(nc)
	if err := b.StartDiscovery(InstanceInfo{ID: id, Type: "lucode-test"}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunnerStreamsEventsAndCancels(t *testing.T) {
	runnerID := "test-runner-" + time.Now().Format("150405.000")
	runner := testBus(t, runnerID)
	hub := testBus(t, "test-hub-"+runnerID)

	cancelled := make(chan string, 1)
	err := runner.StartRunnerServer(func(payload []byte, reply string) {
		runner.PublishStreamEvent(reply, []byte(`{"type":"message_ready","role":"assistant","content":"hi from runner"}`))
		runner.PublishStreamEvent(reply, []byte(`{"type":"heartbeat"}`))
		runner.PublishStreamDone(reply)
	}, func(sessID string) { cancelled <- sessID }, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	err = hub.RunnerExecute(runnerID, []byte(`{"req":{"session_id":"s1"}}`), func(ev []byte) bool {
		var m map[string]any
		json.Unmarshal(ev, &m)
		got = append(got, m["type"].(string))
		return true
	})
	if err != nil {
		t.Fatalf("RunnerExecute: %v", err)
	}
	if len(got) != 2 || got[0] != "message_ready" || got[1] != "heartbeat" {
		t.Errorf("events = %v", got)
	}

	hub.RunnerCancel(runnerID, "s1")
	select {
	case id := <-cancelled:
		if id != "s1" {
			t.Errorf("cancelled %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Error("the runner never received the cancel")
	}
}

func TestOwnsSessionGatesTaskTools(t *testing.T) {
	b := testBus(t, "test-owner-"+time.Now().Format("150405.000"))
	b.OwnsSession = func(id string) bool { return id == "mine" }
	called := make(chan string, 2)
	b.OnTaskTool = func(sessID, tool string, args json.RawMessage) (string, error) {
		called <- sessID
		return "ok", nil
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	// "mine" is answered; "theirs" must be ignored, so the request times out.
	if _, err := b.nc.Request("lucode.tasktools.mine", []byte(`{"tool":"x"}`), time.Second); err != nil {
		t.Errorf("owned session got no reply: %v", err)
	}
	if _, err := b.nc.Request("lucode.tasktools.theirs", []byte(`{"tool":"x"}`), 300*time.Millisecond); err == nil {
		t.Error("a session this instance does not own was answered")
	}
	if len(called) != 1 {
		t.Errorf("OnTaskTool called %d times, want 1", len(called))
	}
}

func TestRPCRequestsAreMarkedAsFromNATS(t *testing.T) {
	id := "rpc-peer-" + time.Now().Format("150405.000")
	peer := testBus(t, id)
	hub := testBus(t, "rpc-hub-"+id)
	seen := make(chan bool, 1)
	peer.StartRPCServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- IsRPC(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	nc := hub.nc
	nc.Flush()

	req, _ := http.NewRequest("GET", "http://x/api/anything", http.NoBody)
	req.Header.Set("Authorization", "Bearer not-valid-on-the-peer")
	if _, err := hub.RPCRequest(id, req); err != nil {
		t.Fatal(err)
	}
	if !<-seen {
		t.Error("IsRPC must be true for a request delivered over NATS")
	}
	plain, _ := http.NewRequest("GET", "http://x/", nil)
	if IsRPC(plain.Context()) {
		t.Error("an ordinary request must not look like a NATS one")
	}
}
