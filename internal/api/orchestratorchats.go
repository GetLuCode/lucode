package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
)

// orchestratorChatView is a stored orchestrator conversation as the history lists it.
type orchestratorChatView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`    // what the user called it ("" until renamed)
	Preview      string `json:"preview"` // the first thing the user wrote
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	Model        string `json:"model"`
	MessageCount int    `json:"message_count"`
	LastActive   string `json:"last_active"`
	Current      bool   `json:"current"` // the chat its project's panel shows now
}

// firstText pulls the readable text out of a stored user message (plain text or content blocks).
func firstText(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "[") {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(content), &blocks) == nil {
			content = ""
			for _, b := range blocks {
				if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
					content = b.Text
					break
				}
			}
		}
	}
	content = strings.Join(strings.Fields(content), " ")
	if r := []rune(content); len(r) > 160 {
		content = string(r[:160]) + "…"
	}
	return content
}

// listOrchestratorChats is the history of the orchestrator conversations, to find one again:
// ?project=<id> narrows it to a project, ?q= filters by name, first message or project.
func (h *Handler) listOrchestratorChats(w http.ResponseWriter, r *http.Request) {
	chats, err := dbpkg.ListOrchestratorChats(h.db, r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	currents := map[string]string{}
	out := make([]orchestratorChatView, 0, len(chats))
	for _, c := range chats {
		name := c.Name
		if name == "Orchestrator" {
			name = ""
		}
		v := orchestratorChatView{
			ID: c.ID, Name: name, Preview: firstText(c.FirstUser), ProjectID: c.ProjectID, ProjectName: c.ProjectName,
			Model: c.Model, MessageCount: c.MessageCount, LastActive: c.LastActive.Format("2006-01-02T15:04:05Z07:00"),
		}
		if q != "" && !strings.Contains(strings.ToLower(v.Name+"\n"+v.Preview+"\n"+v.ProjectName), q) {
			continue
		}
		cur, ok := currents[c.ProjectID]
		if !ok {
			if s, err := dbpkg.GetOrchestratorSession(h.db, c.ProjectID); err == nil {
				cur = s.ID
			}
			currents[c.ProjectID] = cur
		}
		v.Current = cur == c.ID
		out = append(out, v)
	}
	jsonResp(w, http.StatusOK, out)
}

// updateOrchestratorChat renames a conversation and/or assigns it to another project.
func (h *Handler) updateOrchestratorChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := dbpkg.GetSession(h.db, id)
	if err != nil || sess.Kind != dbpkg.SessionKindOrchestrator {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	var body struct {
		Name      *string `json:"name"`
		ProjectID *string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.ProjectID != nil && *body.ProjectID != sess.ProjectID {
		if _, err := dbpkg.GetProject(h.db, *body.ProjectID); err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		// a turn running in the chat that is about to change project would write into the old one
		if h.manager != nil && h.manager.TurnActive(id) {
			http.Error(w, "stop the running turn before moving the conversation", http.StatusConflict)
			return
		}
		if err := dbpkg.MoveOrchestratorChat(h.db, id, *body.ProjectID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			name = "Orchestrator" // back to untitled
		}
		if err := dbpkg.UpdateSessionName(h.db, id, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	sess, _ = dbpkg.GetSession(h.db, id)
	jsonResp(w, http.StatusOK, sess)
}

// openOrchestratorChat makes an older conversation the one its project's panel shows (the one
// it replaces stops), and returns it.
func (h *Handler) openOrchestratorChat(w http.ResponseWriter, r *http.Request) {
	sess, err := dbpkg.GetSession(h.db, r.PathValue("id"))
	if err != nil || sess.Kind != dbpkg.SessionKindOrchestrator {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	cur, err := dbpkg.GetOrchestratorSession(h.db, sess.ProjectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err == nil && cur.ID != sess.ID && h.manager != nil && !h.manager.TurnActive(sess.ID) {
		h.manager.CancelTurn(cur.ID)
		h.manager.CloseLive(cur.ID)
	}
	if err := dbpkg.MakeOrchestratorCurrent(h.db, sess.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, _ = dbpkg.GetSession(h.db, sess.ID)
	jsonResp(w, http.StatusOK, sess)
}
