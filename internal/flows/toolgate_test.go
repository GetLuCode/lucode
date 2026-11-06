package flows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/permissions"
)

func toolReq(tool, input string) ToolRequest {
	return ToolRequest{
		ProjectID: "p1", CardID: "c1", SessionID: "s1", ToolUseID: "toolu_1", ToolName: tool,
		WorkDir: "/work", Task: "Fix it", Input: json.RawMessage(input),
	}
}

func startEngine(t *testing.T, h *harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.e.WaitForEvaluations() })
	h.e.Start(ctx)
}

func decide(t *testing.T, h *harness, req ToolRequest) ToolVerdict {
	t.Helper()
	return h.e.DecideTool(context.Background(), req)
}

// allowIf allows what a condition says and leaves the rest to a person.
func allowIf(condition string) Graph {
	return Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeToolRequest},
			{ID: "if", Type: TypeIf, Config: map[string]any{"condition": condition}},
			{ID: "yes", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow, "reason": "{{event.tool_name}} is fine"}},
			{ID: "no", Type: TypeToolDecision, Config: map[string]any{"decision": ToolDeny, "reason": "not {{event.tool_name}}"}},
		},
		Edges: []Edge{{From: "t", Port: PortNext, To: "if"}, {From: "if", Port: PortTrue, To: "yes"}, {From: "if", Port: PortFalse, To: "no"}},
	}
}

func TestToolWithoutAFlowGoesToAPerson(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	if v := decide(t, h, toolReq("Bash", `{}`)); v.Decision != ToolAsk {
		t.Fatalf("got %+v", v)
	}
}

func TestToolFlowAllowsAndDenies(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	f := h.publish(allowIf(`event.tool_input.command.startsWith("go test")`))

	v := decide(t, h, toolReq("Bash", `{"command":"go test ./..."}`))
	if v.Decision != ToolAllow || !strings.Contains(v.Reason, "Bash is fine") || !strings.Contains(v.Reason, `"flow"`) {
		t.Errorf("allow: %+v", v)
	}
	v = decide(t, h, toolReq("Bash", `{"command":"rm -rf /"}`))
	if v.Decision != ToolDeny || !strings.Contains(v.Reason, "not Bash") {
		t.Errorf("deny: %+v", v)
	}
	if runs := h.runs(f); len(runs) != 2 || runs[0].Status != dbpkg.RunSucceeded || runs[1].Status != dbpkg.RunSucceeded {
		t.Errorf("runs: %+v", runs)
	}
}

// Nothing that goes wrong in a flow can let a tool through.
func TestToolFlowProblemsAskAPerson(t *testing.T) {
	cases := map[string]struct {
		graph Graph
		input string
	}{
		"expression fails": {allowIf(`event.tool_input.missing == "x"`), `{}`},
		"no decision on this path": {Graph{
			Nodes: []Node{
				{ID: "t", Type: TypeToolRequest},
				{ID: "if", Type: TypeIf, Config: map[string]any{"condition": `event.tool_name == "Read"`}},
				{ID: "yes", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow}},
			},
			Edges: []Edge{{From: "t", Port: PortNext, To: "if"}, {From: "if", Port: PortTrue, To: "yes"}},
		}, `{}`},
		"stop before deciding": {Graph{
			Nodes: []Node{
				{ID: "t", Type: TypeToolRequest},
				{ID: "if", Type: TypeIf, Config: map[string]any{"condition": `event.tool_name == "Read"`}},
				{ID: "yes", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow}},
				{ID: "s", Type: TypeStop},
			},
			Edges: []Edge{{From: "t", Port: PortNext, To: "if"}, {From: "if", Port: PortTrue, To: "yes"}, {From: "if", Port: PortFalse, To: "s"}},
		}, `{}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			startEngine(t, h)
			h.publish(c.graph)
			if v := decide(t, h, toolReq("Bash", c.input)); v.Decision != ToolAsk || v.Reason == "" {
				t.Fatalf("got %+v", v)
			}
		})
	}
}

// allowMatch allows what a Match tool node takes and leaves the rest to a person.
func allowMatch(tool, inputRegex string) Graph {
	return Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeToolRequest},
			{ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": tool, "input_regex": inputRegex}},
			{ID: "yes", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow}},
			{ID: "no", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAsk}},
		},
		Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "yes"}, {From: "m", Port: PortNoMatch, To: "no"}},
	}
}

func TestMatchToolReadsTheToolAndItsParameters(t *testing.T) {
	type call struct {
		tool, input, want string
	}
	for _, c := range []struct {
		name, tool, regex string
		calls             []call
	}{
		{"a name is the whole name", "Read", "", []call{
			{"Read", `{}`, ToolAllow}, {"ReadX", `{}`, ToolAsk}, {"Bash", `{}`, ToolAsk},
		}},
		{"tools by regex", "Read|mcp__github__.*", "", []call{
			{"Read", `{}`, ToolAllow}, {"mcp__github__get_issue", `{}`, ToolAllow}, {"mcp__slack__post", `{}`, ToolAsk},
		}},
		{"a parameter", "Bash", `command: ^go test ./[^;&|]*$`, []call{
			{"Bash", `{"command":"go test ./internal/..."}`, ToolAllow},
			{"Bash", `{"command":"go test ./x && rm -rf /"}`, ToolAsk},
			{"Bash", `{"description":"go test ./x"}`, ToolAsk}, // the parameter is not there
			{"Read", `{"command":"go test ./x"}`, ToolAsk},
		}},
		{"every line has to match", "*", "file_path: ^/work/\ncontent: TODO", []call{
			{"Write", `{"file_path":"/work/a","content":"a TODO here"}`, ToolAllow},
			{"Write", `{"file_path":"/etc/a","content":"a TODO here"}`, ToolAsk},
			{"Write", `{"file_path":"/work/a","content":"nothing"}`, ToolAsk},
		}},
		{"a colon inside the regex", "*", `url: ^https://example\.com/`, []call{
			{"WebFetch", `{"url":"https://example.com/a"}`, ToolAllow},
			{"WebFetch", `{"url":"https://other.com/a"}`, ToolAsk},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			startEngine(t, h)
			h.publish(allowMatch(c.tool, c.regex))
			for _, call := range c.calls {
				if v := decide(t, h, toolReq(call.tool, call.input)); v.Decision != call.want {
					t.Errorf("%s %s: got %+v, want %s", call.tool, call.input, v, call.want)
				}
			}
		})
	}
}

// A Match tool node and a permission rule have to mean the same.
func TestMatchToolAgreesWithPermissionRules(t *testing.T) {
	rule := permissions.Rule{Tool: "Bash|Write", InputRegex: map[string]string{"command": "^ls"}}
	m, err := permissions.NewMatcher(rule)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &permissions.PermissionsConfig{Allow: []permissions.Rule{rule}}
	if err := permissions.WriteSettingsFile(permissions.ProjectSettingsPath(dir), cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USERPROFILE", t.TempDir())
	store, err := permissions.LoadForSession(dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tool, input string }{
		{"Bash", `{"command":"ls -l"}`}, {"Write", `{"command":"ls"}`}, {"Bash", `{"command":"rm"}`},
		{"Read", `{"command":"ls"}`}, {"Bash", `{}`},
	} {
		viaNode, err := m.Matches(c.tool, json.RawMessage(c.input))
		if err != nil {
			t.Fatal(err)
		}
		res, err := store.Match(c.tool, json.RawMessage(c.input))
		if err != nil {
			t.Fatal(err)
		}
		if viaNode != (res.Decision == permissions.Allow) {
			t.Errorf("%s %s: node %v, rule %v", c.tool, c.input, viaNode, res.Decision)
		}
	}
}

func TestStrictestFlowWins(t *testing.T) {
	allow := allowMatch("Bash", "")
	other := func(decision string) Graph {
		return Graph{
			Nodes: []Node{{ID: "t", Type: TypeToolRequest}, {ID: "d", Type: TypeToolDecision, Config: map[string]any{"decision": decision}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "d"}},
		}
	}
	for _, want := range []string{ToolDeny, ToolAsk} {
		h := newHarness(t)
		startEngine(t, h)
		h.publish(allow)
		h.publish(other(want))
		if v := decide(t, h, toolReq("Bash", `{}`)); v.Decision != want {
			t.Errorf("want %s, got %+v", want, v)
		}
	}
}

// A slow API must not leave the tool waiting: a person gets it, and the run stops.
func TestToolFlowTimeoutAsksAPersonAndStopsTheRun(t *testing.T) {
	h := newHarness(t)
	h.e.ToolTimeout = 200 * time.Millisecond
	h.host.aiHold = make(chan struct{})
	defer close(h.host.aiHold)
	startEngine(t, h)
	f := h.publish(aiToolFlow())

	v := decide(t, h, toolReq("Bash", `{"command":"ls"}`))
	if v.Decision != ToolAsk || !strings.Contains(v.Reason, "in time") {
		t.Fatalf("got %+v", v)
	}
	if run := h.onlyRun(f); run.Status != dbpkg.RunCancelled {
		t.Errorf("the run should have stopped: %+v", run)
	}
}

func TestLongToolInputGoesToAPerson(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	f := h.publish(allowIf(`true`))
	big := `{"content":"` + strings.Repeat("a", maxToolInput) + `"}`
	if v := decide(t, h, toolReq("Write", big)); v.Decision != ToolAsk {
		t.Fatalf("got %+v", v)
	}
	if runs := h.runs(f); len(runs) != 0 {
		t.Errorf("no run should start: %+v", runs)
	}
}

func TestPausedProjectAsksAPerson(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	h.publish(allowIf(`true`))
	if err := dbpkg.SetFlowsPaused(h.e.db, "p1", true); err != nil {
		t.Fatal(err)
	}
	if v := decide(t, h, toolReq("Bash", `{}`)); v.Decision != ToolAsk {
		t.Fatalf("got %+v", v)
	}
}

// Two tools at once on one card: each has someone waiting, so neither queues behind the other.
func TestParallelToolRequestsDoNotQueue(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	h.publish(allowIf(`true`))
	const n = 5
	got := make(chan ToolVerdict, n)
	for i := 0; i < n; i++ {
		go func() { got <- h.e.DecideTool(context.Background(), toolReq("Bash", `{}`)) }()
	}
	for i := 0; i < n; i++ {
		select {
		case v := <-got:
			if v.Decision != ToolAllow {
				t.Errorf("got %+v", v)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a parallel request got no answer")
		}
	}
}

// A card deep in a chain of flows keeps its approvals.
func TestToolRequestIgnoresTheCardsFlowDepth(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	h.publish(allowIf(`true`))
	if err := dbpkg.RecordFlowChain(h.e.db, "p1", "c1", dbpkg.EventCause{Source: "flow", Depth: MaxDepth + 1}); err != nil {
		t.Fatal(err)
	}
	if v := decide(t, h, toolReq("Bash", `{}`)); v.Decision != ToolAllow {
		t.Fatalf("got %+v", v)
	}
}

func TestRestartClosesToolRuns(t *testing.T) {
	h := newHarness(t)
	f := h.publish(allowIf(`true`))
	event, _ := json.Marshal(map[string]any{"type": dbpkg.EventToolRequested, "request_id": "gone"})
	run, err := dbpkg.CreateFlowRun(h.e.db, dbpkg.FlowRun{FlowID: f.ID, Version: 1, ProjectID: "p1", Event: event, Status: dbpkg.RunRunning}, []string{"t"})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Recover()
	if run, _ = dbpkg.GetFlowRun(h.e.db, run.ID); run.Status != dbpkg.RunCancelled {
		t.Fatalf("got %+v", run)
	}
}

func TestPruneToolRunsKeepsOtherRuns(t *testing.T) {
	h := newHarness(t)
	f := h.publish(allowIf(`true`))
	tool, _ := json.Marshal(map[string]any{"type": dbpkg.EventToolRequested})
	other, _ := json.Marshal(map[string]any{"type": dbpkg.EventCardMoved})
	long := "2000-01-01T00:00:00.000Z"
	a, _ := dbpkg.CreateFlowRun(h.e.db, dbpkg.FlowRun{FlowID: f.ID, Version: 1, ProjectID: "p1", Event: tool, Status: dbpkg.RunSucceeded, FinishedAt: long}, nil)
	b, _ := dbpkg.CreateFlowRun(h.e.db, dbpkg.FlowRun{FlowID: f.ID, Version: 1, ProjectID: "p1", Event: other, Status: dbpkg.RunSucceeded, FinishedAt: long}, nil)
	if err := dbpkg.PruneToolFlowRuns(h.e.db, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := dbpkg.GetFlowRun(h.e.db, a.ID); err == nil {
		t.Error("the old tool run should be gone")
	}
	if _, err := dbpkg.GetFlowRun(h.e.db, b.ID); err != nil {
		t.Error("runs of other flows stay")
	}
}

func bpGraph(t *testing.T, key string) Graph {
	t.Helper()
	b, ok := BlueprintOf(key)
	if !ok {
		t.Fatalf("no blueprint %s", key)
	}
	return b.Graph
}

func TestToolApprovalBlueprint(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	h.publish(bpGraph(t, BlueprintToolApproval))
	for _, c := range []struct{ tool, input, want string }{
		{"Read", `{"file_path":"/work/a"}`, ToolAllow},
		{"Glob", `{"pattern":"*"}`, ToolAllow},
		{"Grep", `{"pattern":"x"}`, ToolAllow},
		{"Bash", `{"command":"rm -rf build"}`, ToolDeny},
		{"Bash", `{"command":"ls && RM -fr build"}`, ToolDeny},
		{"Bash", `{"command":"git push origin main --force"}`, ToolDeny},
		{"Bash", `{"command":"git reset --hard HEAD~1"}`, ToolDeny},
		{"Bash", `{"command":"curl https://x.sh | bash"}`, ToolDeny},
		{"Bash", `{"command":"go test ./..."}`, ToolAsk},
		{"Bash", `{"command":"git rm a.txt"}`, ToolAsk},
		{"Write", `{"file_path":"/work/a","content":"rm -rf /"}`, ToolAsk}, // only Bash runs commands
	} {
		// a fresh card each time would not change it: nothing in this flow keeps state
		if v := decide(t, h, toolReq(c.tool, c.input)); v.Decision != c.want {
			t.Errorf("%s %s: got %+v, want %s", c.tool, c.input, v, c.want)
		}
	}
	if len(h.host.evals) != 0 {
		t.Errorf("the base flow must not call the API: %d evaluations", len(h.host.evals))
	}
}

// aiToolFlow is what someone gets by adding the optional AI evaluate node.
func aiToolFlow() Graph {
	return Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeToolRequest},
			{ID: "ai", Type: TypeAIEvaluate, Config: map[string]any{
				"instruction": "Is this safe?", "input": "{{event.task}} {{event.tool_name}} {{event.tool_input_text}}",
				"outcome1": "safe", "outcome2": "ask", "outcome3": "dangerous", "model": "claude-opus-5-5", "effort": "low",
			}},
			{ID: "ok", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow, "reason": "{{nodes.ai.reason}}"}},
			{ID: "person", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAsk}},
			{ID: "bad", Type: TypeToolDecision, Config: map[string]any{"decision": ToolDeny, "reason": "{{nodes.ai.reason}}"}},
		},
		Edges: []Edge{
			{From: "t", Port: PortNext, To: "ai"},
			{From: "ai", Port: PortOutcome1, To: "ok"}, {From: "ai", Port: PortOutcome2, To: "person"},
			{From: "ai", Port: PortOutcome3, To: "bad"}, {From: "ai", Port: PortFailed, To: "person"},
		},
	}
}

func TestAIEvaluateIsOptionalInAToolFlow(t *testing.T) {
	for _, c := range []struct {
		name, outcome string
		aiErr         bool
		want          string
	}{
		{"safe", "safe", false, ToolAllow},
		{"unsure", "ask", false, ToolAsk},
		{"dangerous", "dangerous", false, ToolDeny},
		{"not one of the answers", "sure thing", false, ToolAsk},
		{"API down", "", true, ToolAsk},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.host.aiOutcome = c.outcome
			if c.aiErr {
				h.host.aiErr = context.DeadlineExceeded
			}
			startEngine(t, h)
			h.publish(aiToolFlow())
			if v := decide(t, h, toolReq("Bash", `{"command":"ls"}`)); v.Decision != c.want {
				t.Errorf("got %+v, want %s", v, c.want)
			}
			if len(h.host.evals) != 1 || !strings.Contains(h.host.evals[0].Input, `"command":"ls"`) || !strings.Contains(h.host.evals[0].Input, "Fix it") {
				t.Errorf("the model does not see the request: %+v", h.host.evals)
			}
		})
	}
}

// Not seeded: it lets tools run with nobody watching.
func TestToolApprovalBlueprintIsOptIn(t *testing.T) {
	if b, _ := BlueprintOf(BlueprintToolApproval); b.Seeded {
		t.Error("a project has to choose this flow")
	}
}

func TestToolFlowSeesAutoMode(t *testing.T) {
	h := newHarness(t)
	startEngine(t, h)
	h.publish(allowIf(`event.auto_mode`))
	req := toolReq("Bash", `{}`)
	if v := decide(t, h, req); v.Decision != ToolDeny {
		t.Errorf("not in auto mode: %+v", v)
	}
	req.AutoMode = true
	if v := decide(t, h, req); v.Decision != ToolAllow {
		t.Errorf("in auto mode: %+v", v)
	}
}

func TestToolFlowValidation(t *testing.T) {
	trigger := Node{ID: "t", Type: TypeToolRequest}
	allow := Node{ID: "d", Type: TypeToolDecision, Config: map[string]any{"decision": ToolAllow}}
	toD := Edge{From: "t", Port: PortNext, To: "d"}
	cond := Node{ID: "if", Type: TypeIf, Config: map[string]any{"condition": `true`}}
	for _, c := range []struct {
		name  string
		g     Graph
		wants string
	}{
		{"decision without its trigger", Graph{
			Nodes: []Node{{ID: "m", Type: TypeManual}, {ID: "d", Type: TypeToolDecision, Config: map[string]any{"decision": ToolDeny}}},
			Edges: []Edge{{From: "m", Port: PortNext, To: "d"}},
		}, "only works in a flow started by Tool requested"},
		{"another trigger", Graph{
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeManual}, cond},
			Edges: []Edge{{From: "t", Port: PortNext, To: "if"}, {From: "if", Port: PortTrue, To: "d"}, {From: "m", Port: PortNext, To: "if"}},
		}, "cannot share a flow"},
		{"a slow node", Graph{
			Nodes: []Node{trigger, {ID: "w", Type: TypeRunAgent, Config: map[string]any{"prompt": "x"}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "w"}},
		}, "cannot be used with Tool requested"},
		{"a person inside the flow", Graph{
			Nodes: []Node{trigger, {ID: "w", Type: TypeApproval, Config: map[string]any{"question": "x"}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "w"}},
		}, "cannot be used with Tool requested"},
		{"no decision", Graph{
			Nodes: []Node{trigger, {ID: "s", Type: TypeStop}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "s"}},
		}, "has no Tool decision"},
		{"allows everything", Graph{Nodes: []Node{trigger, allow}, Edges: []Edge{toD}}, "allows every tool"},
		{"a match of everything", Graph{
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "*"}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "d"}},
		}, "matches everything"},
		{"a bad tool regex", Graph{
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "Read("}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "d"}},
		}, "invalid tool regex"},
		{"a bad parameter regex", Graph{
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "Bash", "input_regex": "command: ("}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "d"}},
		}, "invalid regex"},
		{"a line without a parameter", Graph{
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "Bash", "input_regex": "just a regex"}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "d"}},
		}, "is not parameter: regex"},
		{"a match without its trigger", Graph{
			Nodes: []Node{{ID: "x", Type: TypeManual}, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "Bash"}}},
			Edges: []Edge{{From: "x", Port: PortNext, To: "m"}},
		}, "only works in a flow started by Tool requested"},
	} {
		t.Run(c.name, func(t *testing.T) {
			found := false
			for _, p := range Validate(c.g) {
				found = found || strings.Contains(p.Message, c.wants)
			}
			if !found {
				t.Errorf("want a problem with %q, got %+v", c.wants, Validate(c.g))
			}
		})
	}

	for name, ok := range map[string]Graph{
		"a match first": {
			Nodes: []Node{trigger, allow, {ID: "m", Type: TypeMatchTool, Config: map[string]any{"tool": "Read"}}},
			Edges: []Edge{{From: "t", Port: PortNext, To: "m"}, {From: "m", Port: PortMatch, To: "d"}},
		},
		"a condition": {
			Nodes: []Node{trigger, cond, allow},
			Edges: []Edge{{From: "t", Port: PortNext, To: "if"}, {From: "if", Port: PortTrue, To: "d"}},
		},
		"deny everything": {
			Nodes: []Node{trigger, {ID: "d", Type: TypeToolDecision, Config: map[string]any{"decision": ToolDeny}}},
			Edges: []Edge{toD},
		},
	} {
		if p := Validate(ok); len(p) > 0 {
			t.Errorf("%s should publish: %+v", name, p)
		}
	}
}
