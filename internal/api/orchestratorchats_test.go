package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func TestSupervisorHistory(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{pid}/orchestrator", h.getOrchestrator)
	mux.HandleFunc("GET /api/orchestrator/chats", h.listOrchestratorChats)
	mux.HandleFunc("POST /api/orchestrator/chats/{id}/open", h.openOrchestratorChat)
	do := func(method, path string, into any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if into != nil && rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code
	}

	chat := func(id, first string) {
		t.Helper()
		s := dbpkg.Session{ID: id, ProjectID: pid, Name: "Orchestrator", Model: "sonnet", Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
		if err := dbpkg.InsertSession(h.db, s); err != nil {
			t.Fatal(err)
		}
		if first == "" {
			return
		}
		if err := dbpkg.InsertMessage(h.db, dbpkg.Message{ID: id + "-m", SessionID: id, Role: "user", Content: first, Seq: 1}); err != nil {
			t.Fatal(err)
		}
	}
	claudeTurn := func(id string) {
		t.Helper()
		if err := dbpkg.InsertTurnUsage(h.db, dbpkg.TurnUsage{ID: id + "-u", SessionID: id, ProjectID: pid, Model: "sonnet"}); err != nil {
			t.Fatal(err)
		}
	}
	googleTurn := func(id string) {
		t.Helper()
		if _, err := dbpkg.SavePluginConversation(h.db, dbpkg.PluginConversation{PluginID: "lucode.gemini", SessionID: id, ProjectID: pid, URL: "https://www.google.com/search?q=" + id, Title: id, Turns: 2}); err != nil {
			t.Fatal(err)
		}
	}

	chat("claude", "plan the release")
	claudeTurn("claude")
	chat("google", "only google answered")
	googleTurn("google")
	chat("both", "claude, then google")
	claudeTurn("both")
	googleTurn("both")
	chat("noanswer", "cancelled before any answer") // no usage and no google page: still a supervisor chat
	chat("empty", "")                               // nothing written: not in any history

	var list []orchestratorChatView
	if code := do("GET", "/api/orchestrator/chats?project="+pid+"&mode=supervisor", &list); code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	got := map[string]bool{}
	for _, c := range list {
		got[c.ID] = true
	}
	if len(got) != 3 || !got["claude"] || !got["both"] || !got["noanswer"] {
		t.Errorf("the supervisor history must hold every chat claude could resume, got %v", got)
	}

	// without the mode every conversation that has messages is listed
	list = nil
	do("GET", "/api/orchestrator/chats?project="+pid, &list)
	if len(list) != 4 {
		t.Errorf("all chats = %d, want 4", len(list))
	}

	// opening an old chat makes it the one the panel shows, also after a reload
	var opened dbpkg.Session
	if code := do("POST", "/api/orchestrator/chats/claude/open", &opened); code != http.StatusOK || opened.ID != "claude" {
		t.Fatalf("open: %d %+v", code, opened)
	}
	var cur dbpkg.Session
	do("GET", "/api/projects/"+pid+"/orchestrator", &cur)
	if cur.ID != "claude" {
		t.Errorf("current = %s, want the opened chat", cur.ID)
	}
	list = nil
	do("GET", "/api/orchestrator/chats?project="+pid+"&mode=supervisor", &list)
	for _, c := range list {
		if c.Current != (c.ID == "claude") {
			t.Errorf("chat %s: current = %v", c.ID, c.Current)
		}
	}
}
