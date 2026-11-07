package flows

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

const testNode = "plugin.acme.hello.ask"

// fakePlugins answers the engine like a plugin host, with no code to run.
type fakePlugins struct {
	nodes   []NodeType
	calls   []PluginCall
	port    string
	output  map[string]any
	err     error
	hold    chan struct{}
	missing bool
}

func (f *fakePlugins) NodeTypes() []NodeType {
	if f.missing {
		return nil
	}
	return f.nodes
}
func (f *fakePlugins) Blueprints() []Blueprint  { return nil }
func (f *fakePlugins) Installed(id string) bool { return !f.missing && id == "acme.hello" }
func (f *fakePlugins) RunNode(_ context.Context, c PluginCall) (string, map[string]any, error) {
	f.calls = append(f.calls, c)
	if f.hold != nil {
		<-f.hold
	}
	return f.port, f.output, f.err
}

func usePlugins(t *testing.T) *fakePlugins {
	t.Helper()
	f := &fakePlugins{port: PortNext, output: map[string]any{"answer": "42"}, nodes: []NodeType{{
		Type: testNode, Title: "Ask", Category: CategoryAction, Ports: []string{PortNext, "other", PortFailed},
		Fields: []Field{
			{Name: "prompt", Label: "Prompt", Kind: FieldTemplate, Required: true},
			{Name: "cond", Label: "Cond", Kind: FieldExpr},
			{Name: "tone", Label: "Tone", Kind: FieldSelect, Options: []string{"a", "b"}, Default: "a"},
		},
		PluginID: "acme.hello", PluginName: "Hello", TimeoutSeconds: 60,
	}}}
	SetPluginSource(f)
	t.Cleanup(func() { SetPluginSource(nil) })
	return f
}

func pluginGraph(extra ...Node) Graph {
	g := Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeCardMoved},
			{ID: "p", Type: testNode, Config: cfg("prompt", "say {{card.title}}", "cond", `card.title`)},
			{ID: "c", Type: TypeComment, Config: cfg("text", "answer {{nodes.p.answer}}")},
		},
		Edges: []Edge{{From: "t", Port: PortNext, To: "p"}, {From: "p", Port: PortNext, To: "c"}},
	}
	g.Nodes = append(g.Nodes, extra...)
	return g
}

func settle(h *harness) {
	h.e.Drain()
	h.e.WaitForEvaluations()
	h.e.Drain()
}

func TestPluginNodeRunsAndFeedsLaterNodes(t *testing.T) {
	p := usePlugins(t)
	h := newHarness(t)
	f := h.publish(pluginGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	settle(h)
	if r := h.onlyRun(f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run %s: %s", r.Status, r.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "answer 42" {
		t.Fatalf("comments: %v", h.host.comments)
	}
	c := p.calls[0]
	if c.Config["prompt"] != "say Fix it" || c.Config["cond"] != "Fix it" || c.Config["tone"] != "a" {
		t.Fatalf("config: %+v", c.Config)
	}
	if c.ProjectID != "p1" || c.CardID != "c1" || c.Card["title"] != "Fix it" {
		t.Fatalf("call: %+v", c)
	}
}

// While a node waits for its plugin the engine goes on: other runs are not held back.
func TestPluginNodeDoesNotHoldTheEngine(t *testing.T) {
	p := usePlugins(t)
	p.hold = make(chan struct{})
	h := newHarness(t)
	slow := h.publish(pluginGraph())
	quick := h.publish(Graph{
		Nodes: []Node{{ID: "t", Type: TypeCardMoved}, {ID: "c", Type: TypeComment, Config: cfg("text", "quick")}},
		Edges: []Edge{{From: "t", Port: PortNext, To: "c"}},
	})
	done := make(chan struct{})
	go func() {
		h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine blocked on the plugin")
	}
	if r := h.onlyRun(quick); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("the quick flow: %s", r.Status)
	}
	if r := h.onlyRun(slow); r.Status != dbpkg.RunRunning {
		t.Fatalf("the slow flow: %s", r.Status)
	}
	close(p.hold)
	settle(h)
	if r := h.onlyRun(slow); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("the slow flow after it answered: %s %s", r.Status, r.Error)
	}
}

// A plugin's output cannot name another step's wait, or the port its step takes.
func TestPluginOutputCannotWakeAnotherStep(t *testing.T) {
	p := usePlugins(t)
	h := newHarness(t)
	approval := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeManual},
			{ID: "a", Type: TypeApproval, Config: cfg("question", "ok?")},
			{ID: "c", Type: TypeComment, Config: cfg("text", "approved")},
		},
		Edges: []Edge{{From: "t", Port: PortNext, To: "a"}, {From: "a", Port: PortApproved, To: "c"}},
	})
	if _, err := h.e.StartManual(approval, ""); err != nil {
		t.Fatal(err)
	}
	h.e.Drain()
	steps, _ := dbpkg.WaitingFlowStepsWithPrefix(h.e.db, approvalWaitPrefix)
	if len(steps) != 1 {
		t.Fatalf("waiting: %+v", steps)
	}
	p.output = map[string]any{"wait_key": steps[0].WaitKey, "port": PortApproved, "decision": PortApproved, "answer": "x"}
	f := h.publish(pluginGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	settle(h)
	if r := h.onlyRun(f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	if r := h.onlyRun(approval); r.Status != dbpkg.RunRunning {
		t.Fatalf("the plugin answered somebody else's approval: %s", r.Status)
	}
}

func TestPluginNodeFailures(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(p *fakePlugins)
		failed  bool // connected: the run goes on through "failed"
		wantErr string
	}{
		{"an error takes failed", func(p *fakePlugins) { p.err = errors.New("The plugin Hello is disabled") }, true, ""},
		{"nobody handles it: the run fails", func(p *fakePlugins) { p.err = errors.New("The plugin Hello is disabled") }, false, "disabled"},
		{"a port the plugin takes", func(p *fakePlugins) { p.port = "other" }, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := usePlugins(t)
			c.setup(p)
			h := newHarness(t)
			g := pluginGraph()
			if c.failed {
				g.Nodes = append(g.Nodes, Node{ID: "f", Type: TypeComment, Config: cfg("text", "failed: {{nodes.p.error}}")})
				g.Edges = append(g.Edges, Edge{From: "p", Port: PortFailed, To: "f"})
			}
			f := h.publish(g)
			h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
			settle(h)
			r := h.onlyRun(f)
			if c.wantErr != "" {
				if r.Status != dbpkg.RunFailed || !strings.Contains(r.Error, c.wantErr) {
					t.Fatalf("run: %s %q", r.Status, r.Error)
				}
				return
			}
			if r.Status != dbpkg.RunSucceeded {
				t.Fatalf("run: %s %q", r.Status, r.Error)
			}
			if c.failed && (len(h.host.comments) != 1 || !strings.Contains(h.host.comments[0], "disabled")) {
				t.Fatalf("comments: %v", h.host.comments)
			}
		})
	}
}

// A stop in the middle of a plugin node repeats nothing.
func TestPluginNodeSurvivesARestart(t *testing.T) {
	p := usePlugins(t)
	p.hold = make(chan struct{})
	defer close(p.hold)
	h := newHarness(t)
	g := pluginGraph()
	g.Nodes = append(g.Nodes, Node{ID: "f", Type: TypeComment, Config: cfg("text", "failed: {{nodes.p.error}}")})
	g.Edges = append(g.Edges, Edge{From: "p", Port: PortFailed, To: "f"})
	f := h.publish(g)
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if r := h.onlyRun(f); r.Status != dbpkg.RunRunning {
		t.Fatalf("run: %s", r.Status)
	}

	again := NewEngine(h.e.db, h.host)
	again.TurnEndGrace = 0
	again.Recover()
	again.Drain()
	if r := h.onlyRun(f); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	if len(h.host.comments) != 1 || !strings.Contains(h.host.comments[0], "nothing was retried") {
		t.Fatalf("comments: %v", h.host.comments)
	}
	if len(p.calls) != 1 {
		t.Fatalf("the node ran again: %d calls", len(p.calls))
	}
}

// A plugin that never answers gives up on its timer.
func TestPluginNodeTimesOut(t *testing.T) {
	p := usePlugins(t)
	p.hold = make(chan struct{})
	defer close(p.hold)
	h := newHarness(t)
	f := h.publish(pluginGraph())
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.e.now = func() time.Time { return time.Now().Add(time.Hour) }
	h.e.Drain()
	if r := h.onlyRun(f); r.Status != dbpkg.RunFailed || !strings.Contains(r.Error, "did not answer") {
		t.Fatalf("run: %s %q", r.Status, r.Error)
	}
}

// Plugin nodes cost like agent turns, so a loop of them hits the same limit.
func TestPluginNodesCountAsTurns(t *testing.T) {
	usePlugins(t)
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeCardMoved},
			{ID: "p", Type: testNode, Config: cfg("prompt", "x")},
			{ID: "r", Type: TypeRetry, Config: cfg("max", float64(50))},
		},
		Edges: []Edge{{From: "t", Port: PortNext, To: "p"}, {From: "p", Port: PortNext, To: "r"}, {From: "r", Port: PortAgain, To: "p"}},
	})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	for i := 0; i < 30; i++ {
		settle(h)
	}
	if r := h.onlyRun(f); r.Status != dbpkg.RunFailed || !strings.Contains(r.Error, "agent or AI turns") {
		t.Fatalf("run: %s %q", r.Status, r.Error)
	}
}

func TestPluginNodesInTheValidator(t *testing.T) {
	cases := []struct {
		name    string
		graph   Graph
		missing bool
		want    string
	}{
		{"never in a tool flow", Graph{
			Nodes: []Node{
				{ID: "t", Type: TypeToolRequest},
				{ID: "p", Type: testNode, Config: cfg("prompt", "x")},
				{ID: "d", Type: TypeToolDecision, Config: cfg("decision", ToolAsk)},
			},
			Edges: []Edge{{From: "t", Port: PortNext, To: "p"}, {From: "p", Port: PortNext, To: "d"}},
		}, false, "cannot be used with Tool requested"},
		{"its trigger neither", Graph{
			Nodes: []Node{
				{ID: "t", Type: TypeToolRequest},
				{ID: "e", Type: TypePluginEvent, Config: cfg("plugin", "acme.hello", "event", "x")},
				{ID: "d", Type: TypeToolDecision, Config: cfg("decision", ToolAsk)},
			},
			Edges: []Edge{{From: "t", Port: PortNext, To: "d"}},
		}, false, "cannot share a flow"},
		{"a node of a plugin that is gone", pluginGraph(), true, "the plugin acme.hello, which is not installed"},
		{"a trigger of a plugin that is gone", Graph{
			Nodes: []Node{{ID: "e", Type: TypePluginEvent, Config: cfg("plugin", "acme.gone", "event", "x")}},
		}, false, "the plugin acme.gone, which is not installed"},
		{"a port it does not have", Graph{
			Nodes: []Node{
				{ID: "t", Type: TypeCardMoved},
				{ID: "p", Type: testNode, Config: cfg("prompt", "x")},
				{ID: "c", Type: TypeComment, Config: cfg("text", "y")},
			},
			Edges: []Edge{{From: "t", Port: PortNext, To: "p"}, {From: "p", Port: "nope", To: "c"}},
		}, false, `has no output "nope"`},
		{"its required field", Graph{
			Nodes: []Node{{ID: "t", Type: TypeCardMoved}, {ID: "p", Type: testNode}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "p"}},
		}, false, "Prompt is required"},
		{"a bad template in its field", Graph{
			Nodes: []Node{{ID: "t", Type: TypeCardMoved}, {ID: "p", Type: testNode, Config: cfg("prompt", "{{ card. }}")}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "p"}},
		}, false, "Prompt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := usePlugins(t)
			p.missing = c.missing
			found := false
			var all []string
			for _, problem := range Validate(c.graph) {
				all = append(all, problem.Message)
				found = found || strings.Contains(problem.Message, c.want)
			}
			if !found {
				t.Fatalf("want %q in %v", c.want, all)
			}
		})
	}
	// a disabled plugin's nodes stay known, flagged, so the editor keeps them
	p := usePlugins(t)
	p.nodes[0].Disabled = true
	if def, ok := NodeTypeOf(testNode); !ok || !def.Disabled {
		t.Fatalf("%+v %v", def, ok)
	}
	listed := false
	for _, n := range NodeTypes() {
		listed = listed || (n.Type == testNode && n.PluginName == "Hello")
	}
	if !listed {
		t.Fatal("a plugin's node should be in the list the editor reads")
	}
	if problems := Validate(pluginGraph()); len(problems) != 0 {
		t.Fatalf("a disabled plugin does not make a graph invalid: %+v", problems)
	}
}

// lucode.flows.emit fires the trigger, only for that plugin and event, and continues the chain.
func TestPluginEventTrigger(t *testing.T) {
	usePlugins(t)
	h := newHarness(t)
	graph := func(plugin, event string) Graph {
		return Graph{
			Nodes: []Node{
				{ID: "e", Type: TypePluginEvent, Config: cfg("plugin", plugin, "event", event)},
				{ID: "c", Type: TypeComment, Config: cfg("text", "got {{event.payload.x}}")},
			},
			Edges: []Edge{{From: "e", Port: PortNext, To: "c"}},
		}
	}
	match := h.publish(graph("acme.hello", "done"))
	other := h.publish(graph("acme.hello", "other"))
	emit := func(depth int) {
		if err := dbpkg.RecordPluginEvent(h.e.db, "p1", "c1", "", map[string]any{
			"plugin": "acme.hello", "event": "done", "payload": map[string]any{"x": "y"},
		}, dbpkg.EventCause{Source: "plugin:acme.hello", Depth: depth}); err != nil {
			t.Fatal(err)
		}
		h.e.Drain()
	}
	emit(1)
	if r := h.onlyRun(match); r.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", r.Status, r.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "got y" {
		t.Fatalf("comments: %v", h.host.comments)
	}
	if len(h.runs(other)) != 0 {
		t.Fatal("another event started a flow")
	}
	// plugins and flows firing each other stop at the limit
	emit(MaxDepth)
	runs := h.runs(match)
	if runs[0].Status != dbpkg.RunFailed || !strings.Contains(runs[0].Error, "loop") {
		t.Fatalf("run at the limit: %+v", runs[0])
	}
}

// A locked flow cannot be changed by any road that saves a draft.
func TestLockedFlows(t *testing.T) {
	usePlugins(t)
	h := newHarness(t)
	raw := []byte(`{"nodes":[{"id":"t","type":"trigger.manual"}],"edges":[]}`)
	f, err := dbpkg.UpsertLockedFlow(h.e.db, "p1", "acme.hello", "bp", "Blueprint", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Locked || f.Enabled || f.PublishedVersion != 1 || f.PluginID != "acme.hello" || f.PluginBlueprint != "bp" {
		t.Fatalf("a new locked flow is published and off: %+v", f)
	}
	if err := dbpkg.UpdateFlowDraft(h.e.db, f.ID, "x", raw); !errors.Is(err, dbpkg.ErrFlowLocked) {
		t.Fatalf("UpdateFlowDraft: %v", err)
	}
	if err := dbpkg.SaveFlowDraftByAgent(h.e.db, f.ID, "x", raw, "agent"); !errors.Is(err, dbpkg.ErrFlowLocked) {
		t.Fatalf("SaveFlowDraftByAgent: %v", err)
	}
	if _, err := dbpkg.PublishFlow(h.e.db, f.ID, raw); !errors.Is(err, dbpkg.ErrFlowLocked) {
		t.Fatalf("PublishFlow: %v", err)
	}
	// on, and upgraded: a new version, and it stays on
	if err := dbpkg.SetFlowEnabled(h.e.db, f.ID, true); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"nodes":[{"id":"t2","type":"trigger.manual"}],"edges":[]}`)
	up, err := dbpkg.UpsertLockedFlow(h.e.db, "p1", "acme.hello", "bp", "Blueprint 2", newer)
	if err != nil {
		t.Fatal(err)
	}
	if up.ID != f.ID || up.PublishedVersion != 2 || !up.Enabled || up.Name != "Blueprint 2" {
		t.Fatalf("upgrade: %+v", up)
	}
	same, err := dbpkg.UpsertLockedFlow(h.e.db, "p1", "acme.hello", "bp", "Blueprint 2", newer)
	if err != nil || same.PublishedVersion != 2 {
		t.Fatalf("the same graph is no new version: %+v %v", same, err)
	}
	// the fork is an ordinary draft
	fork, err := dbpkg.ForkFlow(h.e.db, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.Locked || fork.Enabled || fork.PublishedVersion != 0 || fork.ForkedFrom != f.ID || fork.Name != "Blueprint 2 (copy)" {
		t.Fatalf("fork: %+v", fork)
	}
	if err := dbpkg.UpdateFlowDraft(h.e.db, fork.ID, "mine", raw); err != nil {
		t.Fatalf("a fork can be edited: %v", err)
	}
	if _, err := dbpkg.ForkFlow(h.e.db, fork.ID); err == nil {
		t.Fatal("only a locked flow is forked this way")
	}
}
