package flows

import (
	"errors"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

func pushGraph(extra ...Node) Graph {
	g := Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeManual},
			{ID: "p", Type: TypePushNotification, Config: cfg("title", "Hello {{run.depth}}", "body", "Run {{run.flow_id}}")},
		},
		Edges: []Edge{{"t", PortNext, "p"}},
	}
	g.Nodes = append(g.Nodes, extra...)
	return g
}

func TestPushRendersItsTextAndContinues(t *testing.T) {
	h := newHarness(t)
	h.host.pushRes = PushResult{Sent: 2, Failed: 1}
	g := pushGraph(Node{ID: "c", Type: TypeComment, Config: cfg("text", "sent {{nodes.p.sent}}, failed {{nodes.p.failed}}")})
	g.Edges = append(g.Edges, Edge{"p", PortNext, "c"})
	f := h.publish(g)
	if _, err := h.e.StartManual(f, "c1"); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()

	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.pushes) != 1 {
		t.Fatalf("pushes: %+v", h.host.pushes)
	}
	m := h.host.pushes[0]
	if m.Title != "Hello 0" || m.Body != "Run "+f.ID || m.ProjectID != "p1" || m.FlowID != f.ID || m.RunID == "" || m.Seq == 0 {
		t.Fatalf("message: %+v", m)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "sent 2, failed 1" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

// A run needs no card: Schedule and Manual runs can notify too.
func TestPushNeedsNoCard(t *testing.T) {
	h := newHarness(t)
	h.host.pushRes = PushResult{Sent: 1}
	f := h.publish(pushGraph())
	if _, err := h.e.StartManual(f, ""); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded || len(h.host.pushes) != 1 {
		t.Fatalf("run: %s %s, pushes %d", got.Status, got.Error, len(h.host.pushes))
	}
}

func TestPushFailureFailsTheRunWhenNothingHandlesIt(t *testing.T) {
	h := newHarness(t)
	h.host.pushErr = errors.New("no device has notifications on")
	f := h.publish(pushGraph())
	if _, err := h.e.StartManual(f, ""); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunFailed || got.Error != "no device has notifications on" {
		t.Fatalf("run: %s %q", got.Status, got.Error)
	}
}

func TestPushFailureTakesTheFailedOutput(t *testing.T) {
	h := newHarness(t)
	h.host.pushErr = errors.New("nobody home")
	g := pushGraph(Node{ID: "c", Type: TypeComment, Config: cfg("text", "lost: {{nodes.p.error}}")})
	g.Edges = append(g.Edges, Edge{"p", PortFailed, "c"})
	f := h.publish(g)
	if _, err := h.e.StartManual(f, "c1"); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "lost: nobody home" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

func TestPushWithEmptyTextDoesNotSend(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{{ID: "t", Type: TypeManual}, {ID: "p", Type: TypePushNotification, Config: cfg("title", "x", "body", `{{""}}`)}},
		Edges: []Edge{{"t", PortNext, "p"}},
	})
	if _, err := h.e.StartManual(f, ""); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunFailed || len(h.host.pushes) != 0 {
		t.Fatalf("run: %s %q, pushes %d", got.Status, got.Error, len(h.host.pushes))
	}
}

// The hub stops between parking the step and hearing back: the same text goes out again, under
// the same step number, which is what makes the device show it once.
func TestPushInterruptedByARestartIsSentAgain(t *testing.T) {
	h := newHarness(t)
	h.host.pushRes = PushResult{Sent: 1}
	f := h.publish(pushGraph())
	run, err := h.e.StartManual(f, "")
	if err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	first := h.host.pushes[0]

	// back to how a stop would have left it: waiting, with the rendered text
	steps, err := dbpkg.ListFlowSteps(h.e.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var step dbpkg.FlowStep
	for _, s := range steps {
		if s.NodeID == "p" {
			step = s
		}
	}
	h.e.db.Exec(`UPDATE flow_steps SET status=?, wait_key=?, port='' WHERE seq=?`, dbpkg.StepWaiting, pushWaitKey(step.Seq), step.Seq)
	h.e.db.Exec(`UPDATE flow_runs SET status=?, finished_at='' WHERE id=?`, dbpkg.RunRunning, run.ID)

	h.e.Recover()
	h.settleCalls()

	if len(h.host.pushes) != 2 {
		t.Fatalf("pushes: %d", len(h.host.pushes))
	}
	if again := h.host.pushes[1]; again != first {
		t.Fatalf("sent %+v, then %+v", first, again)
	}
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
}

func TestPushThatNeverAnswersTimesOut(t *testing.T) {
	h := newHarness(t)
	h.host.pushHold = make(chan struct{})
	f := h.publish(pushGraph())
	if _, err := h.e.StartManual(f, ""); err != nil {
		t.Fatal(err)
	}
	h.e.Drain() // parked, and the call is stuck
	if got := h.onlyRun(f); got.Status != dbpkg.RunRunning {
		t.Fatalf("run before the timeout: %s %s", got.Status, got.Error)
	}

	later := time.Now().Add(pushStepTimeout + time.Minute)
	h.e.now = func() time.Time { return later }
	h.e.Drain()
	if got := h.onlyRun(f); got.Status != dbpkg.RunFailed || got.Error != "the push services did not answer in time" {
		t.Fatalf("run: %s %q", got.Status, got.Error)
	}

	// the answer that arrives after giving up changes nothing
	close(h.host.pushHold)
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunFailed {
		t.Fatalf("run after the late answer: %s", got.Status)
	}
}
