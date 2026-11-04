package api

import (
	"database/sql"
	"net/http"

	"claude-orchestrator/internal/aimode"
	dbpkg "claude-orchestrator/internal/db"
)

// aimodeStore keeps the Google AI Mode conversations in the database, so a session can reopen
// its conversation by url after the tab, the browser or lucode went away.
type aimodeStore struct{ db *sql.DB }

var _ aimode.Store = aimodeStore{}

func (s aimodeStore) ConversationURL(sessID string) (string, int) {
	c, err := dbpkg.GetAIModeConversationBySession(s.db, sessID)
	if err != nil {
		return "", 0
	}
	return c.URL, c.Turns
}

func (s aimodeStore) SaveConversation(sessID, url, title string, turns int) {
	sess, err := dbpkg.GetSession(s.db, sessID)
	if err != nil {
		return
	}
	_ = dbpkg.SaveAIModeConversation(s.db, sessID, sess.ProjectID, url, title, turns)
}

// aimodeConversationView is a history entry as the UI lists it.
type aimodeConversationView struct {
	dbpkg.AIModeConversation
	SessionExists bool   `json:"session_exists"`
	SessionKind   string `json:"session_kind,omitempty"` // "orchestrator" for the panel's chat
	MessageCount  int    `json:"message_count"`
	Current       bool   `json:"current"` // the chat the orchestrator panel shows now
}

// listAIModeConversations is the history of a project: its Google AI Mode conversations,
// the latest first, each with the url that reopens it.
func (h *Handler) listAIModeConversations(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	convs, err := dbpkg.ListAIModeConversations(h.db, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, _ := dbpkg.GetOrchestratorSession(h.db, pid)
	out := make([]aimodeConversationView, 0, len(convs))
	for _, c := range convs {
		v := aimodeConversationView{AIModeConversation: c}
		if s, err := dbpkg.GetSession(h.db, c.SessionID); err == nil {
			v.SessionExists, v.SessionKind, v.MessageCount = true, s.Kind, s.MessageCount
			v.Current = s.ID == current.ID
		}
		out = append(out, v)
	}
	jsonResp(w, http.StatusOK, out)
}

// continueAIModeConversation brings a conversation back to work on. Its chat is shown again
// (the orchestrator's becomes the current one); if the chat was deleted, a new orchestrator
// chat takes the conversation over. The next message goes to the same Google page.
func (h *Handler) continueAIModeConversation(w http.ResponseWriter, r *http.Request) {
	c, err := dbpkg.GetAIModeConversation(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	sess, err := dbpkg.GetSession(h.db, c.SessionID)
	switch {
	case err == nil && sess.Kind == dbpkg.SessionKindOrchestrator:
		if h.manager != nil && !h.manager.TurnActive(sess.ID) {
			// the panel is about to show it: a running turn of the chat it replaces is stopped
			if cur, err := dbpkg.GetOrchestratorSession(h.db, sess.ProjectID); err == nil && cur.ID != sess.ID {
				h.manager.CancelTurn(cur.ID)
				h.manager.CloseLive(cur.ID)
			}
		}
		if err := dbpkg.MakeOrchestratorCurrent(h.db, sess.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess, _ = dbpkg.GetSession(h.db, sess.ID)
	case err == nil: // an ordinary chat: the UI opens it
	default: // the chat is gone: a new orchestrator chat continues the conversation
		if sess, err = h.createOrchestrator(c.ProjectID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := dbpkg.BindAIModeConversation(h.db, c.ID, sess.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	jsonResp(w, http.StatusOK, sess)
}
