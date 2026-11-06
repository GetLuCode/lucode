package natsbus

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// A person can take minutes to answer an approval. Meanwhile the requests of other sessions
// have to get their own answer, not wait in line behind it.
func TestPermissionRequestDoesNotHoldUpOtherSessions(t *testing.T) {
	url := startJetStreamServer(t)
	hub := testBusAt(t, url, "perm-hub")

	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	hub.OnPermissionRequest = func(req PermissionRequest) (string, string) {
		if req.SessionID == "waiting" {
			<-release // the person is still thinking
		}
		return "allow", "ok"
	}
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	first := make(chan error, 1)
	go func() {
		_, err := nc.Request(PermissionSubject("waiting"), []byte(`{"tool_use_id":"a","tool_name":"Bash"}`), 10*time.Second)
		first <- err
	}()
	time.Sleep(200 * time.Millisecond) // the first request reaches the hub and blocks

	msg, err := nc.Request(PermissionSubject("other"), []byte(`{"tool_use_id":"b","tool_name":"Bash"}`), 2*time.Second)
	if err != nil {
		t.Fatalf("another session's request stayed blocked behind the pending one: %v", err)
	}
	var reply struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(msg.Data, &reply); err != nil || reply.Decision != "allow" {
		t.Fatalf("reply %s: %v", msg.Data, err)
	}

	once.Do(func() { close(release) })
	if err := <-first; err != nil {
		t.Errorf("the pending request lost its answer: %v", err)
	}
}
