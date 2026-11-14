package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func supervisorOf(t *testing.T, h *Handler, pid string) dbpkg.Agent {
	t.Helper()
	if err := dbpkg.SeedDefaults(h.db); err != nil {
		t.Fatal(err)
	}
	sup, ok := h.supervisorAgent(pid)
	if !ok {
		t.Fatal("no Supervisor agent after seeding")
	}
	return sup
}

func TestSupervisorAgentConfiguresThePanel(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	sup := supervisorOf(t, h, pid)
	sup.Model, sup.RunnerID, sup.AIProfileID = "opus", "kali", "ai-1"
	sup.Personality, sup.AutoMode = "Be terse.", true
	sup.Tools = []string{"Read"}
	if err := dbpkg.UpdateAgent(h.db, sup); err != nil {
		t.Fatal(err)
	}
	// nothing picked in the panel: the agent's model, runner, AI, tools, mode and personality
	cfg := h.supervisorConfig(pid)
	if cfg.Model != "opus" || cfg.RunnerID != "kali" || cfg.AIProfileID != "ai-1" || !cfg.Auto || len(cfg.Builtin) != 1 {
		t.Fatalf("%+v", cfg)
	}
	if got := supervisorPersonalityPrompt(cfg.Personality); !strings.Contains(got, "Be terse.") {
		t.Fatalf("personality missing: %q", got)
	}
}

func TestSupervisorIsNotAssignableButCanBeReportedTo(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	sup := supervisorOf(t, h, pid)
	if _, err := h.agentByName(pid, "supervisor"); err == nil {
		t.Fatal("the Supervisor must not be assignable by name")
	}
	if _, err := h.agentByName(pid, sup.ID); err == nil {
		t.Fatal("the Supervisor must not be assignable by id")
	}
	if err := h.checkAssignable(sup.ID); err == nil {
		t.Fatal("checkAssignable accepted the Supervisor")
	}
	// but another agent may report to it
	if err := h.validateReportsTo("", pid, sup.ID); err != nil {
		t.Fatalf("reports to the Supervisor: %v", err)
	}
}

func TestSupervisorAgentIsIsolatedOnUpdate(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	sup := supervisorOf(t, h, pid)
	body := `{"name":"Boss","scope":"project","project_id":"` + pid + `","executor":"aiapi","persistent":false,"worktree":true,"reports_to":"x","model":"haiku","personality":"hi"}`
	r := httptest.NewRequest(http.MethodPut, "/api/agents/"+sup.ID, strings.NewReader(body))
	r.SetPathValue("id", sup.ID)
	w := httptest.NewRecorder()
	h.updateAgent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got dbpkg.Agent
	json.NewDecoder(w.Body).Decode(&got)
	if got.Name != "Supervisor" || got.ProjectID != "" || got.Executor != "claude" || !got.Persistent || got.Worktree || got.ReportsTo != "" {
		t.Fatalf("isolation broken: %+v", got)
	}
	if got.Model != "haiku" || got.Personality != "hi" {
		t.Fatalf("model and personality must be editable: %+v", got)
	}

	// nobody else may take the name
	r = httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/agents", strings.NewReader(`{"name":"supervisor"}`))
	r.SetPathValue("pid", pid)
	w = httptest.NewRecorder()
	h.createAgent(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("creating a second Supervisor: %d", w.Code)
	}
}
