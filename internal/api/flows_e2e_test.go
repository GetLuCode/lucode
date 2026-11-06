package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/nats-io/nats.go"
)

func TestMain(m *testing.M) {
	fakeclaudetest.Intercept() // a turn in these tests replays a recording: no tokens
	os.Exit(m.Run())
}

func jetStreamURL(t *testing.T) string {
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
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			c.Close()
			time.Sleep(300 * time.Millisecond)
			return fmt.Sprintf("nats://127.0.0.1:%d", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func busOn(t *testing.T, url string) *natspkg.Bus {
	t.Helper()
	nc, err := nats.Connect(url, nats.Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return natspkg.NewBus(nc)
}

// The whole road, with nothing simulated but claude: a board move fires a flow on the
// hub, Run agent sends the turn to a runner over NATS, the runner replays the recording,
// the end of the turn comes back and the flow continues from where it waited.
func TestFlowRunsAgentOnARunnerEndToEnd(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	url := jetStreamURL(t)

	runnerDB, err := dbpkg.Init(filepath.Join(t.TempDir(), "runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runnerDB.Close() })
	runnerBus := busOn(t, url)
	runner := sessionpkg.NewManager(runnerDB, runnerBus, url, "")
	if err := runnerBus.StartDiscovery(natspkg.InstanceInfo{ID: "e2e-runner", Type: "lucode"}); err != nil {
		t.Fatal(err)
	}
	if err := runnerBus.StartRunnerServer(runner.ServeRemoteTurn, runner.CancelRemoteTurn, runner.CloseRemoteLive); err != nil {
		t.Fatal(err)
	}

	hubDB, err := dbpkg.Init(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hubDB.Close() })
	hubBus := busOn(t, url)
	hub := sessionpkg.NewManager(hubDB, hubBus, url, "")
	if err := hubBus.StartDiscovery(natspkg.InstanceInfo{ID: "e2e-hub", Type: "lucode", Extra: map[string]any{"hub_only": true}}); err != nil {
		t.Fatal(err)
	}
	h := New(hubDB, hubBus, hub, "")
	h.SetHubOnly(true)
	hub.OnTurnEnd = func(id string) { h.DrainInbox(id); h.FlowTurnEnded(id) }
	hub.OnTurnFailed = h.HandleTurnFailed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.StartFlows(ctx)

	work := t.TempDir()
	proj := dbpkg.Project{
		ID: "p1", Name: "p1", DefaultModel: "haiku", RunnerID: "e2e-runner",
		EnvDirs: map[string][]string{"e2e-runner": {work}},
	}
	if err := dbpkg.InsertProject(hubDB, proj); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureDefaultKanbanLists(hubDB, proj.ID); err != nil {
		t.Fatal(err)
	}
	agent := dbpkg.Agent{ID: "a1", ProjectID: proj.ID, Name: "dev", Model: "haiku", Executor: "claude", Tools: []string{"Read"}, LucodeTools: []string{}}
	if err := dbpkg.InsertAgent(hubDB, agent); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "a", Type: flows.TypeRunAgent, Config: map[string]any{"agent": "dev", "prompt": "Check {{card.title}}"}},
			{ID: "ok", Type: flows.TypeComment, Config: map[string]any{"text": "turn ended on {{nodes.a.session_id}}"}},
			{ID: "bad", Type: flows.TypeComment, Config: map[string]any{"text": "turn failed"}},
		},
		Edges: []flows.Edge{{From: "t", Port: "next", To: "a"}, {From: "a", Port: "next", To: "ok"}, {From: "a", Port: "failed", To: "bad"}},
	})
	f, err := dbpkg.InsertFlow(hubDB, dbpkg.Flow{ProjectID: proj.ID, Name: "e2e", Draft: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbpkg.PublishFlow(hubDB, f.ID, raw); err != nil {
		t.Fatal(err)
	}

	todo, _ := dbpkg.FindKanbanListByKind(hubDB, proj.ID, dbpkg.KanbanTodo)
	card := dbpkg.KanbanCard{ID: "c1", ProjectID: proj.ID, ListID: todo.ID, Title: "the task", CardType: "task", Tags: []string{}, DependsOn: []string{}}
	if err := dbpkg.InsertKanbanCard(hubDB, card); err != nil {
		t.Fatal(err)
	}
	card, _ = dbpkg.GetKanbanCard(hubDB, card.ID)

	// the runner must be known to the hub before a turn can go to it
	for i := 0; i < 100; i++ {
		found := false
		for _, p := range hubBus.Peers() {
			found = found || p.ID == "e2e-runner"
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	h.notifyFlows()

	var run dbpkg.FlowRun
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if runs, _ := dbpkg.ListFlowRuns(hubDB, f.ID, 1); len(runs) == 1 {
			run = runs[0]
			if run.Status != dbpkg.RunRunning {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run.Status != dbpkg.RunSucceeded {
		steps, _ := dbpkg.ListFlowSteps(hubDB, run.ID)
		t.Fatalf("run: %q %q; steps: %+v", run.Status, run.Error, steps)
	}
	sess, err := dbpkg.GetSessionByCard(hubDB, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	comments, _ := dbpkg.ListCardComments(hubDB, card.ID)
	want := "turn ended on " + sess.ID
	found := false
	for _, c := range comments {
		found = found || c.Body == want
	}
	if !found {
		t.Fatalf("the flow did not continue after the turn: %+v", comments)
	}
	msgs, _ := dbpkg.ListMessages(hubDB, sess.ID)
	if len(msgs) == 0 {
		t.Fatal("the replayed turn left no messages in the hub")
	}
	var answered bool
	for _, m := range msgs {
		answered = answered || m.Role == "assistant"
	}
	if !answered {
		t.Fatalf("no reply from the runner in the hub: %+v", msgs)
	}
}
