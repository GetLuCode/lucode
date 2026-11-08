package flows

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// fakeHost records what the engine asked for. No turn, no board, no tokens.
type fakeHost struct {
	card     map[string]any
	comments []string
	tags     []string
	moves    []string
	created  []string
	turns    []string
	reject   string
	turnErr  error
	session  string

	git           GitState
	gitErr        error
	merged        []MergeOptions
	mergePort     string
	mergeErr      error
	mergeFinished int
	notified      []string

	evals     []AIRequest
	aiOutcome string
	aiErr     error
	aiHold    chan struct{} // when set, a call waits for it: an API that does not answer

	pushes  []PushMessage
	pushRes PushResult
	pushErr error
	// when set, a call waits for it: push services that do not answer
	pushHold chan struct{}
}

func (f *fakeHost) SendPush(_ context.Context, m PushMessage) (PushResult, error) {
	if f.pushHold != nil {
		<-f.pushHold
	}
	f.pushes = append(f.pushes, m)
	return f.pushRes, f.pushErr
}

func (f *fakeHost) CardScope(string) (map[string]any, error) { return f.card, nil }
func (f *fakeHost) MoveCard(_, kind, list string, _ dbpkg.EventCause) (string, error) {
	if f.reject != "" {
		return f.reject, nil
	}
	f.moves = append(f.moves, kind+list)
	return "", nil
}
func (f *fakeHost) Comment(_, text string) error { f.comments = append(f.comments, text); return nil }
func (f *fakeHost) AddTag(_, tag string) error   { f.tags = append(f.tags, tag); return nil }
func (f *fakeHost) CreateTask(_, title, _, _, _ string, _ dbpkg.EventCause) (string, error) {
	f.created = append(f.created, title)
	return "new-card", nil
}
func (f *fakeHost) RunAgent(_, _, prompt string, _ dbpkg.EventCause) (string, error) {
	if f.turnErr != nil {
		return "", f.turnErr
	}
	f.turns = append(f.turns, prompt)
	return f.session, nil
}
func (f *fakeHost) RunChanged(dbpkg.FlowRun) {}
func (f *fakeHost) GitStatus(string) (GitState, error) {
	if f.gitErr != nil {
		return GitState{}, f.gitErr
	}
	return f.git, nil
}
func (f *fakeHost) GitMerge(_ context.Context, _ string, o MergeOptions, _ dbpkg.EventCause) (string, map[string]any, error) {
	f.merged = append(f.merged, o)
	if f.mergePort == "" {
		return PortMerged, map[string]any{"base": "main"}, f.mergeErr
	}
	return f.mergePort, map[string]any{}, f.mergeErr
}
func (f *fakeHost) GitMergeFinished(string, MergeOptions, dbpkg.EventCause) error {
	f.mergeFinished++
	return nil
}
func (f *fakeHost) NotifySupervisor(_, text string) error {
	f.notified = append(f.notified, text)
	return nil
}
func (f *fakeHost) AIEvaluate(_ context.Context, req AIRequest) (AIResult, error) {
	f.evals = append(f.evals, req)
	if f.aiHold != nil {
		<-f.aiHold
	}
	if f.aiErr != nil {
		return AIResult{}, f.aiErr
	}
	return AIResult{Outcome: f.aiOutcome, Reason: "because", Model: req.Model, InputTokens: 10, OutputTokens: 2}, nil
}

type harness struct {
	t    *testing.T
	e    *Engine
	host *fakeHost
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	host := &fakeHost{session: "sess1", card: map[string]any{
		"id": "c1", "title": "Fix it", "type": "bug", "tags": []string{"urgent"}, "kind": "review",
	}}
	e := NewEngine(db, host)
	e.TurnEndGrace = 0
	return &harness{t: t, e: e, host: host}
}

func (h *harness) publish(g Graph) dbpkg.Flow {
	h.t.Helper()
	if problems := Validate(g); len(problems) > 0 {
		h.t.Fatalf("graph does not validate: %+v", problems)
	}
	raw, _ := json.Marshal(g)
	f, err := dbpkg.InsertFlow(h.e.db, dbpkg.Flow{ProjectID: "p1", Name: "flow", Draft: raw})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := dbpkg.PublishFlow(h.e.db, f.ID, raw); err != nil {
		h.t.Fatal(err)
	}
	f, _ = dbpkg.GetFlow(h.e.db, f.ID)
	return f
}

func (h *harness) event(typ, card, session string, payload map[string]any) {
	h.t.Helper()
	if err := dbpkg.RecordFlowEvent(h.e.db, "p1", typ, card, session, payload); err != nil {
		h.t.Fatal(err)
	}
	h.e.Drain()
}

func (h *harness) runs(f dbpkg.Flow) []dbpkg.FlowRun {
	h.t.Helper()
	runs, err := dbpkg.ListFlowRuns(h.e.db, f.ID, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	return runs
}

func (h *harness) onlyRun(f dbpkg.Flow) dbpkg.FlowRun {
	h.t.Helper()
	runs := h.runs(f)
	if len(runs) != 1 {
		h.t.Fatalf("want 1 run, got %d", len(runs))
	}
	return runs[0]
}

func moved(to string) map[string]any { return map[string]any{"to_kind": to, "from_kind": "inprogress"} }

func cfg(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func reviewTrigger() Node {
	return Node{ID: "t", Type: TypeCardMoved, Config: cfg("to_kind", "review")}
}

func TestBranchesOnCardData(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "if", Type: TypeIf, Config: cfg("condition", `"urgent" in card.tags && event.to_kind == "review"`)},
			{ID: "yes", Type: TypeComment, Config: cfg("text", "urgent: {{card.title}}")},
			{ID: "no", Type: TypeComment, Config: cfg("text", "calm")},
		},
		Edges: []Edge{{"t", PortEvent, "if"}, {"if", PortTrue, "yes"}, {"if", PortFalse, "no"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("todo")) // no trigger for this list
	if n := len(h.runs(f)); n != 0 {
		t.Fatalf("a move to another list started %d runs", n)
	}
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "urgent: Fix it" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestTemplateDoesNotEvaluateData(t *testing.T) {
	h := newHarness(t)
	h.host.card["title"] = `{{card.id}}`
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "{{card.title}}")}},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.onlyRun(f)
	if h.host.comments[0] != `{{card.id}}` {
		t.Fatalf("a card's title was evaluated as an expression: %q", h.host.comments[0])
	}
}

func TestSameEventNeverRunsAFlowTwice(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "hi")}},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	events := dbpkg.FlowEvent{ID: 1, ProjectID: "p1", Type: dbpkg.EventCardMoved, CardID: "c1", Payload: moved("review")}
	h.e.startRuns(events) // as if a stop had left the event unmarked
	h.e.Drain()
	h.onlyRun(f)
	if len(h.host.comments) != 1 {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestRunAgentWaitsForTheTurnAndSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "review {{card.title}}")},
			{ID: "ok", Type: TypeComment, Config: cfg("text", "agent said {{input}}")},
			{ID: "bad", Type: TypeComment, Config: cfg("text", "failed")},
		},
		Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortReply, "ok"}, {"a", PortFailed, "bad"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if got := h.onlyRun(f); got.Status != dbpkg.RunRunning {
		t.Fatalf("run should wait for the turn: %s %s", got.Status, got.Error)
	}
	if len(h.host.turns) != 1 || h.host.turns[0] != "review Fix it" {
		t.Fatalf("turns: %q", h.host.turns)
	}

	// the hub restarts while the agent works: a new engine over the same database
	e2 := NewEngine(h.e.db, h.host)
	e2.TurnEndGrace = 0
	e2.Recover()
	h.e = e2
	h.e.Drain()
	if len(h.host.turns) != 1 {
		t.Fatalf("the restart repeated the turn: %q", h.host.turns)
	}

	h.event(dbpkg.EventTurnEnded, "c1", "other-session", nil)
	if h.onlyRun(f).Status != dbpkg.RunRunning {
		t.Fatal("another session's turn woke the step")
	}
	h.event(dbpkg.EventTurnEnded, "c1", "sess1", map[string]any{"reply": "all done"})
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "agent said all done" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestTurnFailedBeatsTurnEnded(t *testing.T) {
	h := newHarness(t)
	h.e.TurnEndGrace = time.Hour // the end of the turn is still inside its grace
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")},
			{ID: "ok", Type: TypeComment, Config: cfg("text", "ok")},
			{ID: "bad", Type: TypeComment, Config: cfg("text", "failed")},
		},
		Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortReply, "ok"}, {"a", PortFailed, "bad"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.event(dbpkg.EventTurnEnded, "c1", "sess1", nil)
	h.event(dbpkg.EventTurnFailed, "c1", "sess1", map[string]any{"message": "boom"})
	h.onlyRun(f)
	if len(h.host.comments) != 1 || h.host.comments[0] != "failed" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestParallelAndJoin(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "p", Type: TypeParallel},
			{ID: "a", Type: TypeComment, Config: cfg("text", "a")},
			{ID: "b", Type: TypeRunAgent, Config: cfg("prompt", "b")},
			{ID: "j", Type: TypeJoin},
			{ID: "end", Type: TypeComment, Config: cfg("text", "end {{input}}")},
		},
		Edges: []Edge{
			{"t", PortEvent, "p"}, {"p", "a", "a"}, {"p", "b", "b"},
			{"a", PortText, "j"}, {"b", PortReply, "j"}, {"j", PortValues, "end"},
		},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if len(h.host.comments) > 1 {
		t.Fatal("Join continued before the slow path arrived")
	}
	if h.onlyRun(f).Status != dbpkg.RunRunning {
		t.Fatal("the run should wait for the agent")
	}
	h.event(dbpkg.EventTurnEnded, "c1", "sess1", map[string]any{"reply": "b done"})
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	count := 0
	for _, c := range h.host.comments {
		if c == `end ["a","b done"]` { // Join gives what each path brought, in the order they are wired
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Join continued %d times: %q", count, h.host.comments)
	}
}

func TestRetryIsBounded(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "c", Type: TypeComment, Config: cfg("text", "try")},
			{ID: "r", Type: TypeRetry, Config: cfg("max", float64(2))},
			{ID: "end", Type: TypeComment, Config: cfg("text", "gave up")},
		},
		Edges: []Edge{{"t", PortEvent, "c"}, {"c", PortText, "r"}, {"r", PortAgain, "c"}, {"r", PortExhausted, "end"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	want := "try,try,try,gave up"
	if got := strings.Join(h.host.comments, ","); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestAgentTurnLimitStopsALoop(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")},
			{ID: "r", Type: TypeRetry, Config: cfg("max", float64(1000))},
		},
		Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortReply, "r"}, {"r", PortAgain, "a"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	for i := 0; i < 50 && h.onlyRun(f).Status == dbpkg.RunRunning; i++ {
		h.event(dbpkg.EventTurnEnded, "c1", "sess1", nil)
	}
	got := h.onlyRun(f)
	if got.Status != dbpkg.RunFailed || !strings.Contains(got.Error, "agent or AI turns") {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.turns) != MaxAgentTurns {
		t.Fatalf("spent %d turns, the limit is %d", len(h.host.turns), MaxAgentTurns)
	}
}

// A flow triggered by the end of a turn that starts another turn on the same card moves
// nothing, and it must still stop: this is the loop that burns tokens.
func TestDepthLimitStopsChainsOfRuns(t *testing.T) {
	h := newHarness(t)
	h.host.turnErr = nil
	f := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeTurnEnded},
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "again")},
		},
		Edges: []Edge{{"t", PortEvent, "a"}},
	})
	// the real host records the chain when it starts a turn; do the same here
	h.e.host = &chainHost{fakeHost: h.host, db: h.e}
	for i := 0; i < 40; i++ {
		h.event(dbpkg.EventTurnEnded, "c1", "sess1", nil)
	}
	if n := len(h.host.turns); n > MaxDepth+1 {
		t.Fatalf("the chain ran %d turns; the depth limit is %d", n, MaxDepth)
	}
	var refused bool
	for _, r := range h.runs(f) {
		if r.Status == dbpkg.RunFailed && strings.Contains(r.Error, "loop") {
			refused = true
		}
	}
	if !refused {
		t.Fatal("no run was refused by the depth limit")
	}
}

type chainHost struct {
	*fakeHost
	db *Engine
}

func (c *chainHost) RunAgent(card, agent, prompt string, cause dbpkg.EventCause) (string, error) {
	if err := dbpkg.RecordFlowChain(c.db.db, "p1", card, cause); err != nil {
		return "", err
	}
	return c.fakeHost.RunAgent(card, agent, prompt, cause)
}

func TestFlowIgnoresItsOwnMoves(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeCardMoved},
			{ID: "c", Type: TypeComment, Config: cfg("text", "x")},
		},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	cause := dbpkg.EventCause{Source: "flow", FlowID: f.ID, RunID: "r", Depth: 1}
	if err := dbpkg.RecordFlowChain(h.e.db, "p1", "c1", cause); err != nil {
		t.Fatal(err)
	}
	h.e.Drain()
	if n := len(h.runs(f)); n != 0 {
		t.Fatalf("its own event started %d runs", n)
	}
}

func TestOneRunPerCardTheRestQueue(t *testing.T) {
	h := newHarness(t)
	h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")}},
		Edges: []Edge{{"t", PortEvent, "a"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if len(h.host.turns) != 1 {
		t.Fatalf("two runs at once on a card: %d turns", len(h.host.turns))
	}
	h.event(dbpkg.EventTurnEnded, "c1", "sess1", nil)
	h.e.Drain()
	if len(h.host.turns) != 2 {
		t.Fatalf("the queued run did not start: %d turns", len(h.host.turns))
	}
}

func TestPauseHoldsEverything(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "x")}},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	if err := dbpkg.SetFlowsPaused(h.e.db, "p1", true); err != nil {
		t.Fatal(err)
	}
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if n := len(h.runs(f)); n != 0 {
		t.Fatalf("a paused project started %d runs", n)
	}
}

func TestDisabledFlowDoesNotRun(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "x")}},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	if err := dbpkg.SetFlowEnabled(h.e.db, f.ID, false); err != nil {
		t.Fatal(err)
	}
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if n := len(h.runs(f)); n != 0 {
		t.Fatalf("a disabled flow started %d runs", n)
	}
}

func TestRunUsesTheVersionItStartedWith(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")},
			{ID: "c", Type: TypeComment, Config: cfg("text", "v1")},
		},
		Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortReply, "c"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	v2, _ := json.Marshal(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")},
			{ID: "c", Type: TypeComment, Config: cfg("text", "v2")},
		},
		Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortReply, "c"}},
	})
	if _, err := dbpkg.PublishFlow(h.e.db, f.ID, v2); err != nil {
		t.Fatal(err)
	}
	h.event(dbpkg.EventTurnEnded, "c1", "sess1", nil)
	if len(h.host.comments) != 1 || h.host.comments[0] != "v1" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestActionsFailClearly(t *testing.T) {
	h := newHarness(t)
	h.host.turnErr = errors.New("no runner")
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "a", Type: TypeRunAgent, Config: cfg("prompt", "go")}},
		Edges: []Edge{{"t", PortEvent, "a"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	got := h.onlyRun(f)
	if got.Status != dbpkg.RunFailed || got.Error != "no runner" {
		t.Fatalf("run: %s %q", got.Status, got.Error)
	}
}

func TestMoveRejectedTakesItsPort(t *testing.T) {
	h := newHarness(t)
	h.host.reject = "needs an agent"
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "m", Type: TypeMoveCard, Config: cfg("to_kind", "inprogress")},
			{ID: "c", Type: TypeComment, Config: cfg("text", "refused: {{input}}")},
		},
		Edges: []Edge{{"t", PortEvent, "m"}, {"m", PortRejected, "c"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.onlyRun(f)
	if len(h.host.comments) != 1 || h.host.comments[0] != "refused: needs an agent" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestDelayAndWaitTimeout(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.e.now = func() time.Time { return now }
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "d", Type: TypeDelay, Config: cfg("seconds", float64(30))},
			{ID: "w", Type: TypeWaitEvent, Config: cfg("event", "card.moved", "to_kind", "done", "timeout_minutes", float64(5))},
			{ID: "got", Type: TypeComment, Config: cfg("text", "done")},
			{ID: "late", Type: TypeComment, Config: cfg("text", "timeout")},
		},
		Edges: []Edge{{"t", PortEvent, "d"}, {"d", PortOutput, "w"}, {"w", PortEvent, "got"}, {"w", PortTimeout, "late"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if h.onlyRun(f).Status != dbpkg.RunRunning {
		t.Fatal("delay did not wait")
	}
	now = now.Add(31 * time.Second)
	h.e.Drain()
	h.event(dbpkg.EventCardMoved, "c1", "", moved("todo")) // not what it waits for
	if len(h.host.comments) != 0 {
		t.Fatalf("woke on the wrong list: %q", h.host.comments)
	}
	now = now.Add(6 * time.Minute)
	h.e.Drain()
	h.onlyRun(f)
	if len(h.host.comments) != 1 || h.host.comments[0] != "timeout" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestSchedule(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.e.now = func() time.Time { return now }
	f := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeSchedule, Config: cfg("minutes", float64(10))},
			{ID: "c", Type: TypeCreateTask, Config: cfg("title", "weekly")},
		},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	h.e.Drain()
	if n := len(h.runs(f)); n != 0 {
		t.Fatalf("fired at publish: %d", n)
	}
	now = now.Add(11 * time.Minute)
	h.e.Drain()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.created) != 1 {
		t.Fatalf("created: %q", h.host.created)
	}
}

func TestInterruptedSideEffectIsNotRepeated(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "x")}},
		Edges: []Edge{{"t", PortEvent, "c"}},
	})
	run, err := dbpkg.CreateFlowRun(h.e.db, dbpkg.FlowRun{FlowID: f.ID, Version: 1, ProjectID: "p1", CardID: "c1", Status: dbpkg.RunRunning}, []string{"c"})
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := dbpkg.ListFlowSteps(h.e.db, run.ID)
	_ = dbpkg.StartFlowStep(h.e.db, steps[0].Seq) // the hub dies here
	h.e.Recover()
	h.e.Drain()
	got, _ := dbpkg.GetFlowRun(h.e.db, run.ID)
	if got.Status != dbpkg.RunFailed || len(h.host.comments) != 0 {
		t.Fatalf("run: %s, comments: %q", got.Status, h.host.comments)
	}
}

func TestValidate(t *testing.T) {
	trigger := Node{ID: "t", Type: TypeManual}
	cases := []struct {
		name string
		g    Graph
		want string
	}{
		{"empty", Graph{}, "empty"},
		{"no trigger", Graph{Nodes: []Node{{ID: "c", Type: TypeComment, Config: cfg("text", "x")}}}, "needs a trigger"},
		{"unknown type", Graph{Nodes: []Node{trigger, {ID: "x", Type: "basic/script"}}}, "unknown node type"},
		{"required", Graph{Nodes: []Node{trigger, {ID: "c", Type: TypeComment}}, Edges: []Edge{{"t", PortEvent, "c"}}}, "Text is required"},
		{"bad expr", Graph{Nodes: []Node{trigger, {ID: "i", Type: TypeIf, Config: cfg("condition", "card.(")}}, Edges: []Edge{{"t", PortEvent, "i"}}}, "Condition"},
		{"bad template", Graph{Nodes: []Node{trigger, {ID: "c", Type: TypeComment, Config: cfg("text", "{{nope}}")}}, Edges: []Edge{{"t", PortEvent, "c"}}}, "nope"},
		{"unconnected", Graph{Nodes: []Node{trigger, {ID: "c", Type: TypeComment, Config: cfg("text", "x")}}}, "not connected"},
		{"bad port", Graph{Nodes: []Node{trigger, {ID: "c", Type: TypeComment, Config: cfg("text", "x")}}, Edges: []Edge{{"t", "nope", "c"}}}, "no output"},
		{"into trigger", Graph{Nodes: []Node{trigger, {ID: "t2", Type: TypeManual}}, Edges: []Edge{{"t", PortEvent, "t2"}}}, "is a trigger"},
		{"loop", Graph{
			Nodes: []Node{trigger, {ID: "a", Type: TypeComment, Config: cfg("text", "x")}, {ID: "b", Type: TypeComment, Config: cfg("text", "y")}},
			Edges: []Edge{{"t", PortEvent, "a"}, {"a", PortText, "b"}, {"b", PortText, "a"}},
		}, "loop"},
		{"number", Graph{Nodes: []Node{trigger, {ID: "d", Type: TypeDelay, Config: cfg("seconds", "soon")}}, Edges: []Edge{{"t", PortEvent, "d"}}}, "must be a number"},
		{"option", Graph{Nodes: []Node{trigger, {ID: "m", Type: TypeMoveCard, Config: cfg("to_kind", "nowhere")}}, Edges: []Edge{{"t", PortEvent, "m"}}}, "not an option"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var all []string
			for _, p := range Validate(c.g) {
				all = append(all, p.Message)
			}
			if !strings.Contains(strings.Join(all, " | "), c.want) {
				t.Fatalf("want %q in %q", c.want, all)
			}
		})
	}
	ok := Graph{
		Nodes: []Node{trigger, {ID: "c", Type: TypeComment, Config: cfg("text", "x")}, {ID: "r", Type: TypeRetry, Config: cfg("max", float64(2))}},
		Edges: []Edge{{"t", PortEvent, "c"}, {"c", PortText, "r"}, {"r", PortAgain, "c"}},
	}
	if p := Validate(ok); len(p) != 0 {
		t.Fatalf("a loop through Retry must be valid: %+v", p)
	}
}

func TestExpressionsCannotRunAway(t *testing.T) {
	// a huge comprehension is stopped by the cost limit instead of hanging the engine
	expr := `[1,2,3,4,5,6,7,8,9,10].all(a, [1,2,3,4,5,6,7,8,9,10].all(b, [1,2,3,4,5,6,7,8,9,10].all(c, [1,2,3,4,5,6,7,8,9,10].all(d, [1,2,3,4,5,6,7,8,9,10].all(e, a+b+c+d+e > 0)))))`
	if _, err := EvalBool(expr, Scope{"card": map[string]any{}, "event": map[string]any{}, "run": map[string]any{}, "input": ""}); err == nil {
		t.Fatal("the cost limit did not stop it")
	}
}
