package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// Runs two turns of one session through ServeRemoteTurn with the real claude CLI
// (haiku) and checks the second turn reuses the same persistent process.
// Opt in with LUCODE_E2E_CLAUDE=1; it needs NATS on 4222 and a logged-in claude.
func TestRemotePersistentProcessIsReused(t *testing.T) {
	if os.Getenv("LUCODE_E2E_CLAUDE") != "1" {
		t.Skip("set LUCODE_E2E_CLAUDE=1 to run against the real claude CLI")
	}
	nc, err := nats.Connect("nats://127.0.0.1:4222", nats.Timeout(time.Second))
	if err != nil {
		t.Skipf("no NATS: %v", err)
	}
	defer nc.Close()
	bus := natspkg.NewBus(nc)
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewManager(db, bus, "nats://127.0.0.1:4222", "")

	sess := uuid.New().String()
	workDir := t.TempDir()
	turn := func(msg string) (text string, usage bool) {
		rt := RemoteTurn{Persistent: true, LucodeTools: []string{}}
		rt.Req.SessionID = sess
		rt.Req.Message = msg
		rt.Req.Model = "haiku"
		rt.Req.Provider = "claude"
		rt.Req.WorkDirs = []string{workDir}
		rt.Req.Tools = []string{"Read"}
		payload, _ := json.Marshal(rt)

		inbox := nats.NewInbox()
		sub, _ := nc.SubscribeSync(inbox)
		defer sub.Unsubscribe()
		go m.ServeRemoteTurn(payload, inbox)
		for {
			msg, err := sub.NextMsg(90 * time.Second)
			if err != nil {
				t.Fatalf("stream: %v (text so far %q)", err, text)
			}
			var ev map[string]any
			json.Unmarshal(msg.Data, &ev)
			switch ev["type"] {
			case "stream_done":
				return text, usage
			case "message_ready":
				if c, _ := ev["content"].(string); ev["role"] == "assistant" {
					text += c
				}
			case "context_usage":
				usage = true
			case "error":
				t.Fatalf("error event: %v", ev)
			}
		}
	}

	first, _ := turn("Remember the secret word PINEAPPLE. Reply with just: ok")
	remoteMu.Lock()
	rl1 := remoteLives[sess]
	remoteMu.Unlock()
	if rl1 == nil {
		t.Fatalf("no persistent process registered after the first turn (reply %q)", first)
	}
	second, usage := turn("What was the secret word? Answer with just the word.")
	remoteMu.Lock()
	rl2 := remoteLives[sess]
	remoteMu.Unlock()
	if rl2 != rl1 {
		t.Error("the second turn started a new process instead of reusing the persistent one")
	}
	if !strings.Contains(strings.ToUpper(second), "PINEAPPLE") {
		t.Errorf("the process lost the conversation: %q", second)
	}
	if !usage {
		t.Error("the context usage of the turn never reached the stream")
	}
	m.CloseRemoteLive(sess)
}
