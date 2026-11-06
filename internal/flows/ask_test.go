package flows

import (
	"errors"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

func approvalGraph(extra map[string]any) Graph {
	config := cfg("question", "OK for {{card.title}}?")
	for k, v := range extra {
		config[k] = v
	}
	return Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "ask", Type: TypeApproval, Config: config},
			{ID: "yes", Type: TypeComment, Config: cfg("text", "yes {{nodes.ask.note}}")},
			{ID: "no", Type: TypeComment, Config: cfg("text", "no")},
			{ID: "late", Type: TypeComment, Config: cfg("text", "nobody answered")},
		},
		Edges: []Edge{{"t", PortNext, "ask"}, {"ask", PortApproved, "yes"}, {"ask", PortRejected, "no"}, {"ask", PortTimeout, "late"}},
	}
}

func (h *harness) waitingApproval(f dbpkg.Flow) dbpkg.FlowStep {
	h.t.Helper()
	steps, err := dbpkg.ListFlowSteps(h.e.db, h.onlyRun(f).ID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range steps {
		if s.NodeID == "ask" {
			return s
		}
	}
	h.t.Fatal("no approval step")
	return dbpkg.FlowStep{}
}

func TestApprovalTakesTheChosenPathOnce(t *testing.T) {
	h := newHarness(t)
	f := h.publish(approvalGraph(nil))
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	s := h.waitingApproval(f)
	if s.Status != dbpkg.StepWaiting || !strings.Contains(string(s.Output), "OK for Fix it?") {
		t.Fatalf("step: %+v %s", s, s.Output)
	}
	if len(h.host.comments) != 0 {
		t.Fatal("it did not stop to ask")
	}
	if err := h.e.Decide(s.Seq, PortApproved, "looks good"); err != nil {
		t.Fatal(err)
	}
	// a second click before the engine handles the first one wakes nothing more
	if err := h.e.Decide(s.Seq, PortRejected, ""); err != nil {
		t.Fatal(err)
	}
	h.e.Drain()
	if got := strings.Join(h.host.comments, ","); got != "yes looks good" {
		t.Fatalf("comments: %s", got)
	}
	if err := h.e.Decide(s.Seq, PortApproved, ""); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("after the end: %v", err)
	}
}

func TestDecideOnlyAnswersApprovals(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "ask", Type: TypeDelay, Config: cfg("seconds", float64(60))},
		},
		Edges: []Edge{{"t", PortNext, "ask"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if err := h.e.Decide(h.waitingApproval(f).Seq, PortApproved, ""); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("it answered a Delay: %v", err)
	}
	if err := h.e.Decide(1, "maybe", ""); err == nil {
		t.Fatal("it took an invalid decision")
	}
}

func TestApprovalWaitsWithoutATimeoutAndGivesUpWithOne(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.e.now = func() time.Time { return now }
	f := h.publish(approvalGraph(nil))
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	now = now.Add(365 * 24 * time.Hour)
	h.e.Drain()
	if h.waitingApproval(f).Status != dbpkg.StepWaiting {
		t.Fatal("without a timeout it must wait")
	}

	h2 := newHarness(t)
	now2 := time.Now()
	h2.e.now = func() time.Time { return now2 }
	f2 := h2.publish(approvalGraph(map[string]any{"timeout_minutes": float64(5)}))
	h2.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	now2 = now2.Add(6 * time.Minute)
	h2.e.Drain()
	h2.onlyRun(f2)
	if got := strings.Join(h2.host.comments, ","); got != "nobody answered" {
		t.Fatalf("comments: %s", got)
	}
}

func aiGraph() Graph {
	return Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "ai", Type: TypeAIEvaluate, Config: cfg("instruction", "Risk of {{card.title}}?", "input", "text", "outcome1", "low", "outcome2", "high", "model", "claude-opus-5-5", "effort", "low")},
			{ID: "l", Type: TypeComment, Config: cfg("text", "low because {{nodes.ai.reason}}")},
			{ID: "h", Type: TypeComment, Config: cfg("text", "high")},
			{ID: "bad", Type: TypeComment, Config: cfg("text", "failed: {{nodes.ai.error}}")},
		},
		Edges: []Edge{{"t", PortNext, "ai"}, {"ai", PortOutcome1, "l"}, {"ai", PortOutcome2, "h"}, {"ai", PortFailed, "bad"}},
	}
}

func (h *harness) settle() {
	h.e.Drain()
	h.e.WaitForEvaluations()
	h.e.Drain()
}

func TestAIEvaluateFollowsTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.host.aiOutcome = "low"
	f := h.publish(aiGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.settle()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if got := strings.Join(h.host.comments, ","); got != "low because because" {
		t.Fatalf("comments: %s", got)
	}
	req := h.host.evals[0]
	if req.Instruction != "Risk of Fix it?" || len(req.Outcomes) != 2 || req.Model != "claude-opus-5-5" {
		t.Fatalf("request: %+v", req)
	}
	if len(h.host.turns) != 0 {
		t.Fatal("it started an agent turn")
	}
}

func TestAIEvaluateFailedPort(t *testing.T) {
	h := newHarness(t)
	h.host.aiErr = errors.New("rate limited")
	h.publish(aiGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.settle()
	if got := strings.Join(h.host.comments, ","); got != "failed: rate limited" {
		t.Fatalf("comments: %s", got)
	}
}

func TestAIEvaluateAnswerOutsideTheListNeverPicksAPath(t *testing.T) {
	h := newHarness(t)
	h.host.aiOutcome = "medium"
	h.publish(aiGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.settle()
	if len(h.host.comments) != 1 || !strings.Contains(h.host.comments[0], `answered "medium"`) {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestAIEvaluateTimesOut(t *testing.T) {
	h := newHarness(t)
	h.host.aiOutcome = "low"
	now := time.Now()
	h.e.now = func() time.Time { return now }
	f := h.publish(aiGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	// the answer has not been handled when the step's timer fires
	h.e.WaitForEvaluations()
	if _, err := h.e.db.Exec(`DELETE FROM flow_events WHERE type=?`, dbpkg.EventAIEvaluated); err != nil {
		t.Fatal(err)
	}
	now = now.Add(aiStepTimeout + time.Second)
	h.e.Drain()
	h.onlyRun(f)
	if len(h.host.comments) != 1 || !strings.Contains(h.host.comments[0], "did not answer in time") {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestAIEvaluatesCountTowardTheTurnLimit(t *testing.T) {
	h := newHarness(t)
	h.host.aiOutcome = "low"
	f := h.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "ai", Type: TypeAIEvaluate, Config: cfg("instruction", "x", "input", "x", "outcome1", "low", "outcome2", "high", "model", "claude-opus-5-5", "effort", "low")},
			{ID: "r", Type: TypeRetry, Config: cfg("max", float64(1000))},
		},
		Edges: []Edge{{"t", PortNext, "ai"}, {"ai", PortOutcome1, "r"}, {"r", PortAgain, "ai"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	for i := 0; i < 30; i++ {
		h.settle()
	}
	got := h.onlyRun(f)
	if got.Status != dbpkg.RunFailed || !strings.Contains(got.Error, "agent or AI turns") {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.evals) != MaxAgentTurns {
		t.Fatalf("spent %d calls, the limit is %d", len(h.host.evals), MaxAgentTurns)
	}
}

func TestAskNodesValidate(t *testing.T) {
	trigger := Node{ID: "t", Type: TypeManual}
	ai := func(extra ...any) Node {
		c := cfg("instruction", "x", "input", "y", "outcome1", "a", "outcome2", "b", "model", "claude-opus-5-5", "effort", "low")
		for i := 0; i+1 < len(extra); i += 2 {
			c[extra[i].(string)] = extra[i+1]
		}
		return Node{ID: "ai", Type: TypeAIEvaluate, Config: c}
	}
	for _, c := range []struct {
		name string
		node Node
		edge Edge
		want string
	}{
		{"same answer twice", ai("outcome2", "a"), Edge{"t", PortNext, "ai"}, "there twice"},
		{"unknown model", ai("model", "gpt-5"), Edge{"t", PortNext, "ai"}, "not an option"},
		{"no answer", ai("outcome1", ""), Edge{"t", PortNext, "ai"}, "Answer 1 is required"},
		{"no question", Node{ID: "ask", Type: TypeApproval}, Edge{"t", PortNext, "ask"}, "Question is required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var all []string
			for _, p := range Validate(Graph{Nodes: []Node{trigger, c.node}, Edges: []Edge{c.edge}}) {
				all = append(all, p.Message)
			}
			if !strings.Contains(strings.Join(all, " | "), c.want) {
				t.Fatalf("want %q in %q", c.want, all)
			}
		})
	}
	bad := Graph{
		Nodes: []Node{trigger, ai(), {ID: "c", Type: TypeComment, Config: cfg("text", "x")}},
		Edges: []Edge{{"t", PortNext, "ai"}, {"ai", PortOutcome3, "c"}},
	}
	var msgs []string
	for _, p := range Validate(bad) {
		msgs = append(msgs, p.Message)
	}
	if !strings.Contains(strings.Join(msgs, " | "), "Answer 3 is empty") {
		t.Fatalf("got %q", msgs)
	}
	if p := Validate(Graph{Nodes: []Node{trigger, ai()}, Edges: []Edge{{"t", PortNext, "ai"}}}); len(p) != 0 {
		t.Fatalf("a good node: %+v", p)
	}
}
