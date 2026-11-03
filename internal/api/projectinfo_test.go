package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func TestProjectInfoTellsWhereTheSessionIs(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	newCard(t, h, pid, lists["todo"], "one")
	newCard(t, h, pid, lists["todo"], "two")
	if err := dbpkg.InsertAgent(h.db, dbpkg.Agent{ID: "a1", ProjectID: pid, Name: "Dev"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.projectInfo(dbpkg.Session{ProjectID: pid})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Project: p", "Runner: this instance", "Work dir: .", "Default model: haiku", "Tasks: 2", "Agents (1): Dev"} {
		if !strings.Contains(got, want) {
			t.Errorf("project_info is missing %q:\n%s", want, got)
		}
	}
}

func TestNewOrchestratorSessionReplacesTheCurrentOne(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	call := func(method, path string) dbpkg.Session {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/projects/{pid}/orchestrator", h.getOrchestrator)
		mux.HandleFunc("POST /api/projects/{pid}/orchestrator/new", h.newOrchestrator)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var s dbpkg.Session
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := call("GET", "/api/projects/"+pid+"/orchestrator")
	if again := call("GET", "/api/projects/"+pid+"/orchestrator"); again.ID != first.ID {
		t.Fatal("asking twice must give the same session")
	}
	second := call("POST", "/api/projects/"+pid+"/orchestrator/new")
	if second.ID == first.ID || second.Kind != dbpkg.SessionKindOrchestrator || second.MessageCount != 0 {
		t.Fatalf("new session = %+v (old %s)", second, first.ID)
	}
	if cur := call("GET", "/api/projects/"+pid+"/orchestrator"); cur.ID != second.ID {
		t.Errorf("the newest session must be the current one, got %s want %s", cur.ID, second.ID)
	}
	// the old conversation stays stored but is not listed with the ordinary chats
	if list, _ := dbpkg.ListSessions(h.db, pid); len(list) != 0 {
		t.Errorf("orchestrator sessions must not show in the session list: %+v", list)
	}
}
