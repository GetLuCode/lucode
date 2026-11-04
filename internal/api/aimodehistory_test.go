package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func TestAIModeHistory(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	store := aimodeStore{db: h.db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{pid}/orchestrator", h.getOrchestrator)
	mux.HandleFunc("POST /api/projects/{pid}/orchestrator/new", h.newOrchestrator)
	mux.HandleFunc("GET /api/projects/{pid}/aimode/conversations", h.listAIModeConversations)
	mux.HandleFunc("POST /api/aimode/conversations/{id}/continue", h.continueAIModeConversation)
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

	var first dbpkg.Session
	do("GET", "/api/projects/"+pid+"/orchestrator", &first)
	if u, _ := store.ConversationURL(first.ID); u != "" {
		t.Fatal("a session without a conversation has no url")
	}
	// the local model saves where the conversation is after each answer; the title stays the first question
	store.SaveConversation(first.ID, "https://www.google.com/search?q=a&x=1", "primera pregunta", 2)
	store.SaveConversation(first.ID, "https://www.google.com/search?q=a&x=2", "otra", 4)
	if got, turns := store.ConversationURL(first.ID); got != "https://www.google.com/search?q=a&x=2" || turns != 4 {
		t.Errorf("the url and the turn count must follow the conversation, got %q %d", got, turns)
	}

	// a new orchestrator chat takes over the panel, the old conversation stays in the history
	var second dbpkg.Session
	do("POST", "/api/projects/"+pid+"/orchestrator/new", &second)
	var list []aimodeConversationView
	do("GET", "/api/projects/"+pid+"/aimode/conversations", &list)
	if len(list) != 1 || list[0].Title != "primera pregunta" || list[0].SessionID != first.ID || list[0].Current || !list[0].SessionExists {
		t.Fatalf("history = %+v", list)
	}

	// continuing it brings its chat back as the current one
	var back dbpkg.Session
	if code := do("POST", "/api/aimode/conversations/"+list[0].ID+"/continue", &back); code != http.StatusOK || back.ID != first.ID {
		t.Fatalf("continue: %d %+v", code, back)
	}
	var cur dbpkg.Session
	do("GET", "/api/projects/"+pid+"/orchestrator", &cur)
	if cur.ID != first.ID {
		t.Errorf("the continued chat must be the current one, got %s want %s", cur.ID, first.ID)
	}

	// if its chat was deleted, a new orchestrator chat takes the conversation over
	if err := dbpkg.DeleteSession(h.db, first.ID); err != nil {
		t.Fatal(err)
	}
	var taken dbpkg.Session
	if code := do("POST", "/api/aimode/conversations/"+list[0].ID+"/continue", &taken); code != http.StatusOK || taken.ID == first.ID || taken.Kind != dbpkg.SessionKindOrchestrator {
		t.Fatalf("continue after delete: %d %+v", code, taken)
	}
	if got, _ := store.ConversationURL(taken.ID); got != "https://www.google.com/search?q=a&x=2" {
		t.Errorf("the new chat must reopen the same url, got %q", got)
	}
	if code := do("POST", "/api/aimode/conversations/nope/continue", nil); code != http.StatusNotFound {
		t.Errorf("unknown conversation: %d", code)
	}
}
