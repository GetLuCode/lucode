package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/taskmcp"

	"github.com/google/uuid"
)

func TestDefaultLucodeToolsMatchTheMCPServer(t *testing.T) {
	a, b := dbpkg.DefaultLucodeTools, taskmcp.Names()
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("db.DefaultLucodeTools %v must list the same tools as taskmcp.Names() %v", a, b)
	}
}

func TestSanitizeLucodeTools(t *testing.T) {
	if sanitizeLucodeTools(nil) != nil {
		t.Error("nil stays nil: it means every tool")
	}
	if got := sanitizeLucodeTools([]string{}); got == nil || len(got) != 0 {
		t.Errorf("an empty list stays empty, got %v", got)
	}
	got := sanitizeLucodeTools([]string{"task_done", "rm_rf", "task_done", "task_info"})
	if strings.Join(got, ",") != "task_done,task_info" {
		t.Errorf("unknown and repeated tools are dropped, got %v", got)
	}
}

func TestGlobalAgentIsATeammateInAnyProject(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")

	// a global reviewer with a task in progress in THIS project
	g := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "Reviewer", Model: "haiku", Executor: "claude", Tools: []string{"Read"}}
	if err := dbpkg.InsertAgent(h.db, g); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, h, pid, lists["inprogress"], "Review PR")
	card.AgentID = g.ID
	dbpkg.UpdateKanbanCard(h.db, card)
	sess := taskSession(t, h, card)
	dbpkg.UpdateSessionAgent(h.db, sess.ID, g.ID, "haiku")

	c := &capture{}
	h.startTurnFn = c.fn

	list, err := h.HandleTaskTool(alice.sess.ID, "agent_list", []byte(`{}`))
	if err != nil || !strings.Contains(list, "Reviewer") {
		t.Fatalf("a global agent must be listed as a teammate, got %q %v", list, err)
	}
	if _, err := h.HandleTaskTool(alice.sess.ID, "send_to_agent", []byte(`{"to":"Reviewer","message":"please look"}`)); err != nil {
		t.Fatalf("messaging a global agent must work: %v", err)
	}
	if len(c.opts) != 1 || c.opts[0].SessionID != sess.ID {
		t.Fatalf("the message should land in the global agent's task of this project, got %+v", c.opts)
	}
}

func TestGlobalAgentBusyElsewhereIsNotReachableHere(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")

	g := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "Reviewer", Model: "haiku", Executor: "claude"}
	dbpkg.InsertAgent(h.db, g)

	// its task in progress belongs to ANOTHER project
	other := dbpkg.Project{ID: uuid.New().String(), Name: "other", WorkDirs: []string{"."}, DefaultModel: "haiku"}
	dbpkg.InsertProject(h.db, other)
	dbpkg.EnsureDefaultKanbanLists(h.db, other.ID)
	otherLists, _ := dbpkg.ListKanbanLists(h.db, other.ID)
	var inprogress dbpkg.KanbanList
	for _, l := range otherLists {
		if l.Kind == dbpkg.KanbanInProgress {
			inprogress = l
		}
	}
	card := newCard(t, h, other.ID, inprogress, "Elsewhere")
	sess := taskSession(t, h, card)
	dbpkg.UpdateSessionAgent(h.db, sess.ID, g.ID, "haiku")
	h.startTurnFn = (&capture{}).fn

	_, err := h.HandleTaskTool(alice.sess.ID, "send_to_agent", []byte(`{"to":"Reviewer","message":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "no task in progress") {
		t.Errorf("a message must not leak into the agent's task of another project, got %v", err)
	}
}

func TestReportsToRespectsScope(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	local := addMember(t, h, pid, lists, "Local", "", "todo")
	g1 := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "GlobalLead", Model: "haiku"}
	g2 := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "GlobalDev", Model: "haiku"}
	dbpkg.InsertAgent(h.db, g1)
	dbpkg.InsertAgent(h.db, g2)

	if err := h.validateReportsTo(local.agent.ID, pid, g1.ID); err != nil {
		t.Errorf("a project agent can report to a global one: %v", err)
	}
	if err := h.validateReportsTo(g2.ID, "", g1.ID); err != nil {
		t.Errorf("a global agent can report to another global one: %v", err)
	}
	if err := h.validateReportsTo(g2.ID, "", local.agent.ID); err == nil {
		t.Error("a global agent cannot report to a project agent: it would have no supervisor in the other projects")
	}
	if err := h.validateReportsTo("", "some-other-project", local.agent.ID); err == nil {
		t.Error("a project agent cannot report to an agent of a different project")
	}
}

// update drives the real updateAgent handler.
func update(t *testing.T, h *Handler, id, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/agents/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.updateAgent(rec, req)
	return rec.Code, rec.Body.String()
}

func TestChangingAnAgentsScope(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	a := addMember(t, h, pid, lists, "Alice", "", "todo")

	if code, body := update(t, h, a.agent.ID, `{"scope":"global"}`); code != http.StatusOK {
		t.Fatalf("making an agent global should work, got %d %s", code, body)
	}
	got, _ := dbpkg.GetAgent(h.db, a.agent.ID)
	if got.ProjectID != "" || got.Scope != "global" {
		t.Errorf("the agent should now be global, got %+v", got)
	}

	if code, _ := update(t, h, a.agent.ID, `{"scope":"project"}`); code != http.StatusBadRequest {
		t.Errorf("going back to a project needs to say which one, got %d", code)
	}
	if code, body := update(t, h, a.agent.ID, `{"scope":"project","project_id":"`+pid+`"}`); code != http.StatusOK {
		t.Fatalf("going back to a project with project_id should work, got %d %s", code, body)
	}
	got, _ = dbpkg.GetAgent(h.db, a.agent.ID)
	if got.ProjectID != pid || got.Scope != "project" {
		t.Errorf("the agent should be back in the project, got %+v", got)
	}

	if code, _ := update(t, h, a.agent.ID, `{"scope":"everywhere"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown scope is refused, got %d", code)
	}
}

func TestGlobalSubordinatesBlockMakingTheirSupervisorProjectOnly(t *testing.T) {
	h, _, _ := newTaskTestHandler(t)
	lead := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "Lead", Model: "haiku"}
	dev := dbpkg.Agent{ID: uuid.New().String(), ProjectID: "", Name: "Dev", Model: "haiku", ReportsTo: lead.ID}
	dbpkg.InsertAgent(h.db, lead)
	dbpkg.InsertAgent(h.db, dev)

	code, body := update(t, h, lead.ID, `{"scope":"project","project_id":"p1"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "report to this one") {
		t.Errorf("a global agent that reports to Lead must stop Lead from becoming project-only, got %d %s", code, body)
	}
}

func TestLucodeToolsAreEnforcedAndEditable(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	dev := addMember(t, h, pid, lists, "Dev", "", "inprogress")

	if code, body := update(t, h, dev.agent.ID, `{"lucode_tools":["task_comment","task_info","bogus"]}`); code != http.StatusOK {
		t.Fatalf("lucode_tools should be editable, got %d %s", code, body)
	}
	got, _ := dbpkg.GetAgent(h.db, dev.agent.ID)
	if strings.Join(got.LucodeTools, ",") != "task_comment,task_info" {
		t.Errorf("unknown tools are dropped, got %v", got.LucodeTools)
	}

	if _, err := h.HandleTaskTool(dev.sess.ID, "task_comment", []byte(`{"text":"hi"}`)); err != nil {
		t.Errorf("an enabled tool works: %v", err)
	}
	for _, tool := range []string{"task_done", "task_blocked", "agent_list", "send_to_agent"} {
		_, err := h.HandleTaskTool(dev.sess.ID, tool, []byte(`{"summary":"x","reason":"x","to":"x","message":"x"}`))
		if err == nil || !strings.Contains(err.Error(), "disabled for this agent") {
			t.Errorf("%s is disabled for this agent and must be refused, got %v", tool, err)
		}
	}
	if c, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID); c.ListID != lists["inprogress"].ID {
		t.Error("a refused task_done must not move the card")
	}

	// [] disables every lucode tool; the field absent leaves it alone
	update(t, h, dev.agent.ID, `{"lucode_tools":[]}`)
	got, _ = dbpkg.GetAgent(h.db, dev.agent.ID)
	if len(got.LucodeTools) != 0 {
		t.Errorf("an empty list means no lucode tools, got %v", got.LucodeTools)
	}
	update(t, h, dev.agent.ID, `{"name":"Dev2"}`)
	got, _ = dbpkg.GetAgent(h.db, dev.agent.ID)
	if len(got.LucodeTools) != 0 || got.Name != "Dev2" {
		t.Errorf("an update without lucode_tools must not touch them, got %+v", got)
	}
}

func TestSystemPromptOnlyDescribesTheToolsTheAgentHas(t *testing.T) {
	full := agentSystemPrompt(dbpkg.Agent{Name: "A"}, dbpkg.KanbanCard{ID: "T", Title: "t"}, "")
	for _, want := range []string{"task_comment", "task_done", "task_blocked", "agent_list", "send_to_agent", "task_info"} {
		if !strings.Contains(full, want) {
			t.Errorf("an agent with every tool should be told about %s", want)
		}
	}

	only := dbpkg.Agent{Name: "A", LucodeTools: []string{"task_comment"}}
	p := agentSystemPrompt(only, dbpkg.KanbanCard{ID: "T", Title: "t"}, "Lead")
	if !strings.Contains(p, "task_comment") {
		t.Error("the enabled tool must be described")
	}
	for _, banned := range []string{"task_done(", "task_blocked(", "agent_list(", "send_to_agent(", "task_info(", "Never say the task is finished"} {
		if strings.Contains(p, banned) {
			t.Errorf("a tool the agent does not have must not be described (%q found)", banned)
		}
	}
	if !strings.Contains(p, "You cannot send messages") {
		t.Error("an agent that cannot send messages is told so")
	}

	none := dbpkg.Agent{Name: "A", LucodeTools: []string{}}
	p = agentSystemPrompt(none, dbpkg.KanbanCard{ID: "T", Title: "t"}, "")
	if strings.Contains(p, "task_comment") || strings.Contains(p, "The task only changes state") {
		t.Errorf("with no lucode tools there is nothing to describe:\n%s", p)
	}
}

func TestSystemPromptAsksForAWorktree(t *testing.T) {
	card := dbpkg.KanbanCard{ID: "T", Title: "t"}
	p := agentSystemPrompt(dbpkg.Agent{Name: "A"}, card, "")
	if !strings.Contains(p, "Call EnterWorktree") || !strings.Contains(p, "ExitWorktree") {
		t.Errorf("an unrestricted agent should be told to use EnterWorktree:\n%s", p)
	}
	p = agentSystemPrompt(dbpkg.Agent{Name: "A", Tools: []string{"Bash", "Edit"}}, card, "")
	if !strings.Contains(p, "git worktree add") || strings.Contains(p, "EnterWorktree") {
		t.Errorf("without EnterWorktree it should use git worktree add:\n%s", p)
	}
	p = agentSystemPrompt(dbpkg.Agent{Name: "A", Tools: []string{"Read"}}, card, "")
	if strings.Contains(p, "worktree") {
		t.Errorf("an agent that can neither enter nor create a worktree is not told about it:\n%s", p)
	}
}

func TestCreateAgentRequestShape(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	post := func(body string) (int, dbpkg.Agent) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/agents", strings.NewReader(body))
		req.SetPathValue("pid", pid)
		rec := httptest.NewRecorder()
		h.createAgent(rec, req)
		var a dbpkg.Agent
		json.Unmarshal(rec.Body.Bytes(), &a)
		return rec.Code, a
	}

	code, local := post(`{"name":"Local","model":"haiku"}`)
	if code != http.StatusCreated || local.ProjectID != pid || local.Scope != "project" {
		t.Errorf("by default an agent belongs to the project, got %d %+v", code, local)
	}
	if len(local.LucodeTools) != len(dbpkg.DefaultLucodeTools) {
		t.Errorf("a new agent has every lucode tool, got %v", local.LucodeTools)
	}

	code, g := post(`{"name":"Shared","model":"haiku","scope":"global","lucode_tools":["task_done"]}`)
	if code != http.StatusCreated || g.ProjectID != "" || g.Scope != "global" || len(g.LucodeTools) != 1 {
		t.Errorf("scope=global creates a global agent with the chosen tools, got %d %+v", code, g)
	}
}
