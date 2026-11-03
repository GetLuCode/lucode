package natsbus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startJetStreamServer launches a throwaway nats-server with JetStream, using the
// binary named by NATS_SERVER_BIN (or found on PATH). It skips when there is none.
func startJetStreamServer(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("NATS_SERVER_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("nats-server"); err != nil {
			t.Skip("no nats-server binary (set NATS_SERVER_BIN)")
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	cmd := exec.Command(bin, "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-js", "-sd", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			c.Close()
			time.Sleep(300 * time.Millisecond) // let JetStream finish starting
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

// A turn with a multi-megabyte image goes through, and so does a multi-megabyte
// event coming back, although both exceed max_payload.
func TestLargeMessagesGoThroughTheObjectStore(t *testing.T) {
	url := startJetStreamServer(t)
	runnerID := "large-runner"
	runner := testBusAt(t, url, runnerID)
	hub := testBusAt(t, url, "large-hub")

	bigIn := bytes.Repeat([]byte("a"), 3<<20)
	bigOut := strings.Repeat("b", 4<<20)

	gotIn := make(chan int, 1)
	err := runner.StartRunnerServer(func(payload []byte, reply string) {
		var rt struct {
			Image string `json:"image"`
		}
		json.Unmarshal(payload, &rt)
		gotIn <- len(rt.Image)
		ev, _ := json.Marshal(map[string]any{"type": "message_ready", "content": bigOut})
		if err := runner.PublishStreamEvent(reply, ev); err != nil {
			t.Errorf("publish big event: %v", err)
		}
		runner.PublishStreamDone(reply)
	}, func(string) {}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"image": string(bigIn)})
	if int64(len(payload)) <= hub.nc.MaxPayload() {
		t.Fatalf("test payload (%d) must exceed max_payload (%d)", len(payload), hub.nc.MaxPayload())
	}
	var gotOut int
	err = hub.RunnerExecute(runnerID, payload, func(ev []byte) bool {
		var m struct {
			Content string `json:"content"`
		}
		json.Unmarshal(ev, &m)
		gotOut = len(m.Content)
		return true
	})
	if err != nil {
		t.Fatalf("RunnerExecute: %v", err)
	}
	if n := <-gotIn; n != len(bigIn) {
		t.Errorf("runner received an image of %d bytes, want %d", n, len(bigIn))
	}
	if gotOut != len(bigOut) {
		t.Errorf("hub received %d bytes of content, want %d", gotOut, len(bigOut))
	}

	// objects are deleted once read
	store, err := hub.objectStore()
	if err != nil {
		t.Fatal(err)
	}
	if objs, _ := store.List(); len(objs) != 0 {
		t.Errorf("%d objects left in the store", len(objs))
	}
}

// Without JetStream a message above max_payload fails with an explanation instead
// of disappearing.
func TestLargeMessageWithoutJetStreamExplainsWhy(t *testing.T) {
	hub := testBus(t, "nojs-hub")
	if _, err := hub.objectStore(); err == nil {
		t.Skip("the local NATS server has JetStream enabled")
	}
	err := hub.RunnerExecute("nobody", bytes.Repeat([]byte("x"), int(hub.nc.MaxPayload())+10), func([]byte) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "-js") {
		t.Errorf("want an error pointing at nats-server -js, got %v", err)
	}
}
