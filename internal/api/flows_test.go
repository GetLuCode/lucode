package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
)

// flowHarness runs the real engine over the real board. Turns are recorded, not started.
func flowHarness(t *testing.T) (*Handler, string, map[string]dbpkg.KanbanList, *[]sessionpkg.TurnOptions) {
	t.Helper()
	h, pid, lists := newTaskTestHandler(t)
	turns := &[]sessionpkg.TurnOptions{}
	h.startTurnFn = func(o sessionpkg.TurnOptions) error { *turns = append(*turns, o); return nil }
	h.bus = natspkg.NewBus(nil) // only SelfInfo and Broadcast are used
	h.flows = flows.NewEngine(h.db, flowHost{h})
	h.flows.TurnEndGrace = 0
	return h, pid, lists, turns
}

func publishFlow(t *testing.T, h *Handler, pid string, g flows.Graph) dbpkg.Flow {
	t.Helper()
	if p := flows.Validate(g); len(p) > 0 {
		t.Fatalf("invalid graph: %+v", p)
	}
	raw, _ := json.Marshal(g)
	f, err := dbpkg.InsertFlow(h.db, dbpkg.Flow{ProjectID: pid, Name: "f", Draft: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbpkg.PublishFlow(h.db, f.ID, raw); err != nil {
		t.Fatal(err)
	}
	return f
}

func lastRun(t *testing.T, h *Handler, f dbpkg.Flow) dbpkg.FlowRun {
	t.Helper()
	runs, err := dbpkg.ListFlowRuns(h.db, f.ID, 10)
	if err != nil || len(runs) == 0 {
		t.Fatalf("no run: %v", err)
	}
	return runs[0]
}

// A board move fires the flow, and the flow's actions reach the board with its rules.
func TestFlowReactsToABoardMove(t *testing.T) {
	h, pid, lists, _ := flowHarness(t)
	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "in {{card.list}}: {{card.title}}"}},
			{ID: "g", Type: flows.TypeAddTag, Config: map[string]any{"tag": "seen"}},
			{ID: "m", Type: flows.TypeMoveCard, Config: map[string]any{"to_kind": "done"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "c"}, {From: "c", Port: flows.PortText, To: "g"}, {From: "g", Port: flows.PortTag, To: "m"}},
	})
	card := newCard(t, h, pid, lists["todo"], "the task")
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	if r := lastRun(t, h, f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	card, _ = dbpkg.GetKanbanCard(h.db, card.ID)
	if card.ListID != lists["done"].ID {
		t.Fatalf("card is in %s", card.ListID)
	}
	if len(card.Tags) != 1 || card.Tags[0] != "seen" {
		t.Fatalf("tags: %v", card.Tags)
	}
	comments, _ := dbpkg.ListCardComments(h.db, card.ID)
	if len(comments) != 1 || comments[0].Body != "in Review: the task" {
		t.Fatalf("comments: %+v", comments)
	}
}

// Every route that changes a card's list logs the event.
func TestEveryMoveLogsAnEvent(t *testing.T) {
	h, pid, lists, _ := flowHarness(t)
	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "{{event.source}}:{{event.to_kind}}"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "c"}},
	})
	card := newCard(t, h, pid, lists["todo"], "x")

	if _, err := h.moveCardToKind(card, dbpkg.KanbanBlocked); err != nil { // the system
		t.Fatal(err)
	}
	h.flows.Drain()
	sess := dbpkg.Session{ProjectID: pid}
	if _, err := h.taskUpdate("s", sess, json.RawMessage(`{"task_id":"`+card.ID+`","status":"backlog"}`)); err != nil { // an agent
		t.Fatal(err)
	}
	h.flows.Drain()

	comments, _ := dbpkg.ListCardComments(h.db, card.ID)
	got := map[string]bool{}
	for _, c := range comments {
		got[c.Body] = true
	}
	if !got["system:blocked"] || !got["agent:backlog"] {
		t.Fatalf("events seen: %v", got)
	}
	_ = f
}

// A flow's Move card obeys the board: without an agent a card cannot enter In Progress.
func TestFlowMoveObeysTheBoardRules(t *testing.T) {
	h, pid, lists, turns := flowHarness(t)
	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "m", Type: flows.TypeMoveCard, Config: map[string]any{"to_kind": "inprogress"}},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "{{input}}"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "m"}, {From: "m", Port: "rejected", To: "c"}},
	})
	card := newCard(t, h, pid, lists["todo"], "no agent")
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	if r := lastRun(t, h, f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	card, _ = dbpkg.GetKanbanCard(h.db, card.ID)
	if card.ListID != lists["review"].ID {
		t.Fatal("the move should have been refused")
	}
	if len(*turns) != 0 {
		t.Fatal("no turn should start")
	}
}

// Run agent starts one turn with the rendered instruction and waits for it to end.
func TestFlowRunsAgentOnce(t *testing.T) {
	h, pid, lists, turns := flowHarness(t)
	agent := dbpkg.Agent{ID: "a1", ProjectID: pid, Name: "dev", Model: "haiku", Executor: "claude"}
	if err := dbpkg.InsertAgent(h.db, agent); err != nil {
		t.Fatal(err)
	}
	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "a", Type: flows.TypeRunAgent, Config: map[string]any{"agent": "dev", "prompt": "check {{card.title}}"}},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "agent finished"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "a"}, {From: "a", Port: flows.PortReply, To: "c"}},
	})
	card := newCard(t, h, pid, lists["todo"], "the task")
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	if len(*turns) != 1 {
		t.Fatalf("turns: %d", len(*turns))
	}
	card, _ = dbpkg.GetKanbanCard(h.db, card.ID)
	if card.ListID != lists["inprogress"].ID || card.AgentID != "a1" {
		t.Fatalf("card: list %s agent %s", card.ListID, card.AgentID)
	}
	if r := lastRun(t, h, f); r.Status != dbpkg.RunRunning {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	sess, err := dbpkg.GetSessionByCard(h.db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.FlowTurnEnded(sess.ID)
	h.flows.Drain()
	if r := lastRun(t, h, f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	if len(*turns) != 1 {
		t.Fatalf("turns after the end: %d", len(*turns))
	}
}

// Without a flow nothing changes: the event log is the only addition to a move.
func TestNoFlowsNoEffect(t *testing.T) {
	h, pid, lists, turns := flowHarness(t)
	card := newCard(t, h, pid, lists["todo"], "x")
	if _, err := h.moveCardToKind(card, dbpkg.KanbanDone); err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	if len(*turns) != 0 {
		t.Fatal("no turn should start")
	}
	got, _ := dbpkg.GetKanbanCard(h.db, card.ID)
	if got.ListID != lists["done"].ID {
		t.Fatalf("card moved to %s", got.ListID)
	}
}

// The rules of the card machine, checked against the same fixture the web test uses, so
// the Go table, the JSON served to the web and XState cannot drift apart.
func TestCardMachineMatchesTheWebFixture(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "tests", "fixtures")
	raw, _ := json.MarshalIndent(flows.CardMachine(), "", "  ")
	path := filepath.Join(dir, "card-machine.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(want, &a)
	_ = json.Unmarshal(raw, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("web/tests/fixtures/card-machine.json is out of date: run with UPDATE_GOLDEN=1")
	}

	casesRaw, err := os.ReadFile(filepath.Join(dir, "card-machine-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		From    string                 `json:"from"`
		To      string                 `json:"to"`
		Context flows.CardGuardContext `json:"context"`
		Allowed bool                   `json:"allowed"`
	}
	if err := json.Unmarshal(casesRaw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := flows.CardGuardPasses(flows.CardGuard(c.From, c.To), c.Context); got != c.Allowed {
			t.Errorf("%s → %s %+v: allowed=%v, want %v", c.From, c.To, c.Context, got, c.Allowed)
		}
	}
}
