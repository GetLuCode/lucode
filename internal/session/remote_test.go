package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

func TestMain(m *testing.M) {
	fakeclaudetest.Intercept()
	os.Exit(m.Run())
}

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
			time.Sleep(300 * time.Millisecond)
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func connect(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, nats.Timeout(time.Second))
	if err != nil {
		t.Skipf("no NATS: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func testBus(t *testing.T, url string) *natspkg.Bus { return natspkg.NewBus(connect(t, url)) }

func testManager(t *testing.T, bus *natspkg.Bus, url string) (*Manager, *sql.DB) {
	t.Helper()
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewManager(db, bus, url, ""), db
}

// Runs two turns of one session through ServeRemoteTurn and checks the second turn
// reuses the same persistent process. claude is replayed from a recording (no
// tokens spent); it needs a nats-server with JetStream and is skipped without one.
func TestRemotePersistentProcessIsReused(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	url := startJetStreamServer(t)
	bus := testBus(t, url)
	m, _ := testManager(t, bus, url)

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

		turnID := uuid.New().String()
		done := make(chan struct{})
		f, err := bus.FollowRunnerTurn("rt", sess, false, nil, func(ev natspkg.RunnerEvent) error {
			if ev.TurnID != turnID {
				return nil
			}
			if ev.Done {
				close(done)
				return nil
			}
			var e map[string]any
			json.Unmarshal(ev.Event, &e)
			switch e["type"] {
			case "message_ready":
				if c, _ := e["content"].(string); e["role"] == "assistant" {
					text += c
				}
			case "context_usage":
				usage = true
			case "error":
				t.Errorf("error event: %v", e)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		go m.ServeRemoteTurn(payload, bus.NewTurnSink("rt", sess, turnID))
		select {
		case <-done:
		case <-time.After(90 * time.Second):
			t.Fatalf("no turn_done (text so far %q)", text)
		}
		return text, usage
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

// A turn the hub started and then lost (it went down right after the runner's ack)
// finishes on the runner by itself; when the hub comes back, RecoverRunnerTurns
// relays everything, although the consumer starts after the runner published the
// whole turn, and ends the turn as usual.
func TestHubRecoversATurnThatFinishedWhileItWasDown(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	url := startJetStreamServer(t)

	// the runner
	runnerBus := testBus(t, url)
	if err := runnerBus.StartDiscovery(natspkg.InstanceInfo{ID: "recover-runner", Type: "lucode-test"}); err != nil {
		t.Fatal(err)
	}
	runnerM, _ := testManager(t, runnerBus, url)
	if err := runnerBus.StartRunnerServer(runnerM.ServeRemoteTurn, runnerM.CancelRemoteTurn, runnerM.CloseRemoteLive); err != nil {
		t.Fatal(err)
	}

	// the hub, first life: it sends the turn and dies
	hub1 := testBus(t, url)
	sess := uuid.New().String()
	rt := RemoteTurn{LucodeTools: []string{}}
	rt.Req.SessionID = sess
	rt.Req.Message = "Remember the secret word PINEAPPLE. Reply with just: ok"
	rt.Req.Model = "haiku"
	rt.Req.Provider = "claude"
	rt.Req.WorkDirs = []string{t.TempDir()}
	rt.Req.Tools = []string{"Read"}
	payload, _ := json.Marshal(rt)
	turnID := uuid.New().String()
	if err := hub1.RunnerStartTurn("recover-runner", sess, turnID, payload); err != nil {
		t.Fatal(err)
	}

	// the runner finishes alone: wait for turn_done in the stream (a reader that is
	// not the hub's consumer)
	nc := connect(t, url)
	js, _ := nc.JetStream()
	probe, err := js.SubscribeSync("lucode.runner.recover-runner.events."+sess, nats.DeliverAll())
	if err != nil {
		t.Fatal(err)
	}
	finished := false
	for !finished {
		msg, err := probe.NextMsg(30 * time.Second)
		if err != nil {
			t.Fatalf("the runner never finished the turn: %v", err)
		}
		finished = strings.Contains(string(msg.Data), `"turn_done"`)
	}

	// the hub, second life
	hubBus := testBus(t, url)
	hubM, hdb := testManager(t, hubBus, url)
	if err := dbpkg.InsertProject(hdb, dbpkg.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.InsertSession(hdb, dbpkg.Session{ID: sess, ProjectID: "p1", Name: "recover", Model: "haiku", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.SaveRunnerTurn(hdb, dbpkg.RunnerTurn{SessionID: sess, RunnerID: "recover-runner", TurnID: turnID, Opts: `{"model":"haiku"}`}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var broadcast []map[string]any
	hubBus.OnBroadcast = func(data []byte) {
		var e map[string]any
		json.Unmarshal(data, &e)
		mu.Lock()
		broadcast = append(broadcast, e)
		mu.Unlock()
	}
	ended := make(chan string, 1)
	hubM.OnTurnEnd = func(id string) { ended <- id }

	hubM.RecoverRunnerTurns()
	select {
	case id := <-ended:
		if id != sess {
			t.Errorf("turn ended for %s", id)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the recovered turn never ended")
	}

	mu.Lock()
	defer mu.Unlock()
	var reply string
	running := false
	for _, e := range broadcast {
		if e["type"] == "session_status" && e["status"] == "running" {
			running = true
		}
		if e["type"] == "message_ready" {
			if msg, _ := e["message"].(map[string]any); msg["role"] == "assistant" {
				reply += fmt.Sprint(msg["content"])
			}
		}
		if e["type"] == "error" {
			t.Errorf("error event: %v", e)
		}
	}
	if !running {
		t.Error("the session was not marked running again")
	}
	if strings.TrimSpace(reply) == "" {
		t.Errorf("the reply written while the hub was down never arrived; events: %v", broadcast)
	}
	if _, err := dbpkg.GetRunnerTurn(hdb, sess); err == nil {
		t.Error("the turn is still recorded as in flight")
	}
	if got := hubM.GetStatus(sess); got == StatusRunning && len(hubM.procs) != 0 {
		t.Error("the session is still busy")
	}
}

// An event delivered twice (a redelivery after a crash between relaying and acking)
// is relayed once.
func TestRedeliveredRunnerEventIsRelayedOnce(t *testing.T) {
	url := startJetStreamServer(t)
	runnerBus := testBus(t, url)
	hubBus := testBus(t, url)
	hubM, hdb := testManager(t, hubBus, url)

	sess, turnID := uuid.New().String(), uuid.New().String()
	turn := dbpkg.RunnerTurn{SessionID: sess, RunnerID: "dup-runner", TurnID: turnID}
	if err := dbpkg.SaveRunnerTurn(hdb, turn); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	publish := func(ev map[string]any) {
		mu.Lock()
		got = append(got, fmt.Sprint(ev["n"]))
		mu.Unlock()
	}

	sink := runnerBus.NewTurnSink("dup-runner", sess, turnID)
	ev := func(n int) []byte { return []byte(fmt.Sprintf(`{"type":"message_ready","n":%d}`, n)) }
	sink.Event(ev(1)) // seq 1
	sink.Event(ev(2)) // seq 2
	// a second copy of seq 2, as a redelivery would bring (another message id, so
	// JetStream's own de-duplication does not hide it)
	js, _ := connect(t, url).JetStream()
	dup, _ := json.Marshal(map[string]any{"turn_id": turnID, "seq": 2, "type": "event", "event": json.RawMessage(ev(2))})
	if _, err := js.Publish("lucode.runner.dup-runner.events."+sess, dup, nats.MsgId("copy-of-2")); err != nil {
		t.Fatal(err)
	}
	// seq 3 and the end: the copy sits between 2 and 3 in the stream
	sink.Event(ev(3))
	sink.Done()

	done := make(chan struct{})
	go func() {
		hubM.followRemoteTurn(context.Background(), turn, publish, true, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("followRemoteTurn did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "1,2,3" {
		t.Errorf("relayed %v, want 1,2,3", got)
	}
	if _, err := dbpkg.GetRunnerTurn(hdb, sess); err == nil {
		t.Error("the finished turn is still recorded")
	}
}
