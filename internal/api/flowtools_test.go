package api

import (
	"encoding/json"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	"claude-orchestrator/internal/taskmcp"
)

// flowToolRig is a project with an agent that has the flow tools, working on a card.
type flowToolRig struct {
	*toolGateRig
}

func newFlowToolRig(t *testing.T) flowToolRig {
	t.Helper()
	r := newToolGateRig(t)
	if err := dbpkg.EnsureDefaultKanbanLists(r.h.db, "p1"); err != nil {
		t.Fatal(err)
	}
	todo, err := dbpkg.FindKanbanListByKind(r.h.db, "p1", dbpkg.KanbanTodo)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []dbpkg.Agent{
		{ID: "editor", ProjectID: "p1", Name: "editor", Model: "haiku", Executor: "claude", Tools: []string{"Read"}, LucodeTools: taskmcp.FlowNames()},
		{ID: "plain", ProjectID: "p1", Name: "plain", Model: "haiku", Executor: "claude", Tools: []string{"Read"}, LucodeTools: []string{"task_done"}},
	} {
		if err := dbpkg.InsertAgent(r.h.db, a); err != nil {
			t.Fatal(err)
		}
		card := dbpkg.KanbanCard{ID: "c-" + a.ID, ProjectID: "p1", ListID: todo.ID, Title: a.ID, CardType: "task", Tags: []string{}, DependsOn: []string{}, AgentID: a.ID}
		if err := dbpkg.InsertKanbanCard(r.h.db, card); err != nil {
			t.Fatal(err)
		}
		if err := dbpkg.InsertSession(r.h.db, dbpkg.Session{ID: "s-" + a.ID, ProjectID: "p1", Name: a.ID, Model: "haiku", Status: "running", CardID: card.ID, AgentID: a.ID}); err != nil {
			t.Fatal(err)
		}
	}
	return flowToolRig{r}
}

func (r flowToolRig) call(session, tool string, args any) (string, error) {
	r.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.h.HandleTaskTool(session, "mcp__lucode__"+tool, raw)
}

func (r flowToolRig) must(session, tool string, args any) string {
	r.t.Helper()
	out, err := r.call(session, tool, args)
	if err != nil {
		r.t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func (r flowToolRig) onlyFlow() dbpkg.Flow {
	r.t.Helper()
	list, err := dbpkg.ListFlows(r.h.db, "p1")
	if err != nil {
		r.t.Fatal(err)
	}
	if len(list) != 1 {
		r.t.Fatalf("want 1 flow, got %d", len(list))
	}
	return list[0]
}

var validManualGraph = map[string]any{
	"nodes": []map[string]any{
		{"id": "n1", "type": flows.TypeManual, "x": 40, "y": 100, "config": map[string]any{}},
		{"id": "n2", "type": flows.TypeComment, "x": 340, "y": 100, "config": map[string]any{"text": "hello"}},
	},
	"edges": []map[string]any{{"from": "n1", "port": "next", "to": "n2"}},
}

func TestAgentPreparesAFlowAndAPersonPublishesIt(t *testing.T) {
	r := newFlowToolRig(t)

	out := r.must("s-editor", "flow_create", map[string]any{"name": "Rules", "blueprint": flows.BlueprintToolApproval})
	if !strings.Contains(out, "runs nothing until the user publishes") {
		t.Errorf("the agent should be told it cannot publish: %s", out)
	}
	f := r.onlyFlow()
	if f.PublishedVersion != 0 || f.Enabled || f.DraftAgent != "editor" {
		t.Fatalf("a new flow is an unpublished draft marked with its author: %+v", f)
	}

	if out := r.must("s-editor", "flow_get", map[string]any{"flow": "rules"}); !strings.Contains(out, flows.TypeMatchTool) {
		t.Errorf("flow_get finds it by name and shows the draft: %s", out)
	}
	if out := r.must("s-editor", "flow_list", map[string]any{}); !strings.Contains(out, "draft edited by editor") {
		t.Errorf("flow_list: %s", out)
	}
	if out := r.must("s-editor", "flow_catalog", map[string]any{}); !strings.Contains(out, flows.TypeMatchTool) || !strings.Contains(out, flows.BlueprintToolApproval) {
		t.Errorf("flow_catalog: %s", out)
	}

	// a person publishes it, which takes the mark away
	if _, err := dbpkg.PublishFlow(r.h.db, f.ID, f.Draft); err != nil {
		t.Fatal(err)
	}
	if f = r.onlyFlow(); f.DraftAgent != "" || f.PublishedVersion != 1 {
		t.Fatalf("after publishing: %+v", f)
	}

	// a change afterwards touches only the draft: what runs stays as a person published it
	published, err := dbpkg.GetFlowVersion(r.h.db, f.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.must("s-editor", "flow_update", map[string]any{"flow": f.ID, "graph": validManualGraph})
	f = r.onlyFlow()
	if f.PublishedVersion != 1 || !f.Enabled || f.DraftAgent != "editor" || !strings.Contains(string(f.Draft), flows.TypeComment) {
		t.Fatalf("after flow_update: %+v", f)
	}
	if now, _ := dbpkg.GetFlowVersion(r.h.db, f.ID, 1); string(now) != string(published) {
		t.Error("the published version must not change")
	}
}

// A draft edited by an agent keeps its mark through a person's autosave, which holds that change.
func TestAPersonsSaveKeepsTheAgentsMark(t *testing.T) {
	r := newFlowToolRig(t)
	r.must("s-editor", "flow_create", map[string]any{"name": "Rules", "graph": validManualGraph})
	f := r.onlyFlow()
	if err := dbpkg.UpdateFlowDraft(r.h.db, f.ID, f.Name, f.Draft); err != nil {
		t.Fatal(err)
	}
	if f = r.onlyFlow(); f.DraftAgent != "editor" {
		t.Errorf("mark lost: %+v", f)
	}
}

func TestFlowToolsRefuseWhatWouldBreakADraft(t *testing.T) {
	r := newFlowToolRig(t)
	r.must("s-editor", "flow_create", map[string]any{"name": "Rules", "graph": validManualGraph})
	before := string(r.onlyFlow().Draft)

	for name, args := range map[string]map[string]any{
		"unknown node":   {"flow": "Rules", "graph": map[string]any{"nodes": []map[string]any{{"id": "n1", "type": "action.nope"}}}},
		"no nodes":       {"flow": "Rules", "graph": map[string]any{"edges": []map[string]any{}}},
		"nothing to do":  {"flow": "Rules"},
		"no flow":        {"graph": validManualGraph},
		"not this flow":  {"flow": "Other", "graph": validManualGraph},
	} {
		if _, err := r.call("s-editor", "flow_update", args); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	if got := string(r.onlyFlow().Draft); got != before {
		t.Errorf("a refused change must not touch the draft: %s", got)
	}

	// unfinished is fine: it just cannot be published yet, and the agent is told why
	out := r.must("s-editor", "flow_update", map[string]any{"flow": "Rules", "graph": map[string]any{"nodes": []map[string]any{{"id": "n1", "type": flows.TypeManual, "x": 40, "y": 100}, {"id": "n2", "type": flows.TypeComment, "x": 340, "y": 100}}}})
	if !strings.Contains(out, "Publishing would fail") {
		t.Errorf("the agent should see the problems: %s", out)
	}
}

func TestFlowToolsStayInTheAgentsProject(t *testing.T) {
	r := newFlowToolRig(t)
	if err := dbpkg.InsertProject(r.h.db, dbpkg.Project{ID: "p2", Name: "p2", DefaultModel: "haiku"}); err != nil {
		t.Fatal(err)
	}
	other, err := dbpkg.InsertFlow(r.h.db, dbpkg.Flow{ProjectID: "p2", Name: "Theirs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{other.ID, "Theirs"} {
		if _, err := r.call("s-editor", "flow_get", map[string]any{"flow": ref}); err == nil {
			t.Errorf("%s: a flow of another project must not be reachable", ref)
		}
		if _, err := r.call("s-editor", "flow_update", map[string]any{"flow": ref, "graph": validManualGraph}); err == nil {
			t.Errorf("%s: a flow of another project must not be editable", ref)
		}
	}
}

func TestFlowToolsAreGivenByName(t *testing.T) {
	r := newFlowToolRig(t)
	if _, err := r.call("s-plain", "flow_list", map[string]any{}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("an agent without them: %v", err)
	}
	// the default list of a new agent does not include them
	for _, n := range dbpkg.DefaultLucodeTools {
		if taskmcp.IsFlowTool(n) {
			t.Errorf("%s must not be on by default", n)
		}
	}

	// a creator gets a new agent these tools only by asking, and only if it has them
	editor, _ := dbpkg.GetAgent(r.h.db, "editor")
	if _, err := r.h.agentCreate(editor, json.RawMessage(`{"name":"child"}`)); err != nil {
		t.Fatal(err)
	}
	agents, _ := dbpkg.ListAgents(r.h.db, "p1")
	for _, a := range agents {
		if a.Name == "child" {
			for _, n := range a.LucodeTools {
				if taskmcp.IsFlowTool(n) {
					t.Errorf("not handed down by default, but child has %s", n)
				}
			}
		}
	}
	plain, _ := dbpkg.GetAgent(r.h.db, "plain")
	plain.LucodeTools = []string{"task_done"}
	if _, err := r.h.agentCreate(plain, json.RawMessage(`{"name":"sneaky","lucode_tools":["flow_update"]}`)); err == nil {
		t.Error("an agent cannot give what it does not have")
	}
	if got := sanitizeLucodeTools([]string{"flow_update", "flow_publish"}); len(got) != 1 || got[0] != "flow_update" {
		t.Errorf("flow_publish does not exist, got %v", got)
	}
}

func TestSupervisorHasTheFlowTools(t *testing.T) {
	r := newFlowToolRig(t)
	if err := dbpkg.InsertSession(r.h.db, dbpkg.Session{ID: "orch", ProjectID: "p1", Name: "orch", Model: "haiku", Status: "running", Kind: dbpkg.SessionKindOrchestrator}); err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, n := range orchestratorTools() {
		has[n] = true
	}
	for _, n := range taskmcp.FlowNames() {
		if !has[n] {
			t.Errorf("the Supervisor should have %s", n)
		}
	}
	r.must("orch", "flow_create", map[string]any{"name": "Rules", "graph": validManualGraph})
	if f := r.onlyFlow(); f.DraftAgent != "Supervisor" {
		t.Errorf("mark: %+v", f)
	}
}

// Editing drafts together cannot ask for approval on each change, and it runs nothing.
func TestFlowToolsNeedNoApproval(t *testing.T) {
	r := newFlowToolRig(t)
	got := r.runHook("s-editor", "t1", "mcp__lucode__flow_update", `{"flow":"x"}`)
	if got.Decision != "allow" {
		t.Fatalf("got %+v", got)
	}
	if _, err := dbpkg.GetPendingHook(r.h.db, "s-editor", "t1"); err == nil {
		t.Error("nobody should be asked")
	}
	// anything else from the same server still goes through approval
	r.answerAsAPerson("s-editor", "t2", "deny")
	if got := r.runHook("s-editor", "t2", "mcp__lucode__flow_publish", `{"flow":"x"}`); got.Decision != "deny" {
		t.Errorf("an unknown lucode tool: %+v", got)
	}
}
