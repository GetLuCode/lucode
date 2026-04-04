package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"
	wspkg "claude-orchestrator/internal/ws"

	"github.com/google/uuid"
)

type Handler struct {
	db      *sql.DB
	hub     *wspkg.Hub
	manager *sessionpkg.Manager
}

func New(db *sql.DB, hub *wspkg.Hub, manager *sessionpkg.Manager) *Handler {
	return &Handler{db: db, hub: hub, manager: manager}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", h.health)

	mux.HandleFunc("POST /api/projects", h.createProject)
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("DELETE /api/projects/{id}", h.deleteProject)

	mux.HandleFunc("POST /api/projects/{pid}/sessions", h.createSession)
	mux.HandleFunc("GET /api/projects/{pid}/sessions", h.listSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.deleteSession)

	mux.HandleFunc("POST /api/sessions/{id}/turn", h.sendTurn)
	mux.HandleFunc("POST /api/sessions/{id}/fork", h.forkSession)

	mux.HandleFunc("GET /api/sessions/{id}/messages", h.listMessages)

	mux.HandleFunc("GET /ws", h.wsUpgrade)
}

// ---- middleware ----

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Wrap(h *Handler) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	return cors(mux)
}

// ---- handlers ----

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string   `json:"name"`
		WorkDirs        []string `json:"work_dirs"`
		ClaudeConfigDir string   `json:"claude_config_dir"`
		DefaultModel    string   `json:"default_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.DefaultModel == "" {
		body.DefaultModel = "haiku"
	}
	p := dbpkg.Project{
		ID:              uuid.New().String(),
		Name:            body.Name,
		WorkDirs:        body.WorkDirs,
		ClaudeConfigDir: body.ClaudeConfigDir,
		DefaultModel:    body.DefaultModel,
	}
	if err := dbpkg.InsertProject(h.db, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, p)
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := dbpkg.ListProjects(h.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []dbpkg.Project{}
	}
	jsonResp(w, http.StatusOK, projects)
}

func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteProject(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		Message string `json:"message"`
		Name    string `json:"name"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	proj, err := dbpkg.GetProject(h.db, pid)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	model := body.Model
	if model == "" {
		model = proj.DefaultModel
	}

	sess := dbpkg.Session{
		ID:        uuid.New().String(),
		ProjectID: pid,
		Name:      body.Name,
		Model:     model,
		Status:    string(sessionpkg.StatusIdle),
	}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Fire first turn if message provided
	if body.Message != "" {
		opts := sessionpkg.TurnOptions{
			SessionID:       sess.ID,
			ProjectID:       pid,
			Message:         body.Message,
			Model:           model,
			Name:            body.Name,
			WorkDirs:        proj.WorkDirs,
			ClaudeConfigDir: proj.ClaudeConfigDir,
		}
		if err := h.manager.StartTurn(opts); err != nil {
			log.Printf("api: start turn: %v", err)
		}
	}

	jsonResp(w, http.StatusCreated, sess)
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	sessions, err := dbpkg.ListSessions(h.db, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []dbpkg.Session{}
	}
	jsonResp(w, http.StatusOK, sessions)
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteSession(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) sendTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Message string `json:"message"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess, err := dbpkg.GetSession(h.db, id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	proj, err := dbpkg.GetProject(h.db, sess.ProjectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	model := body.Model
	if model == "" {
		model = sess.Model
	}

	opts := sessionpkg.TurnOptions{
		SessionID:       id,
		ProjectID:       sess.ProjectID,
		Message:         body.Message,
		Model:           model,
		WorkDirs:        proj.WorkDirs,
		ClaudeConfigDir: proj.ClaudeConfigDir,
		Resume:          sess.MessageCount > 0, // first turn uses --session-id, subsequent use --resume
	}

	if err := h.manager.StartTurn(opts); err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			http.Error(w, "session is busy", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) forkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	src, err := dbpkg.GetSession(h.db, id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	proj, err := dbpkg.GetProject(h.db, src.ProjectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	newSess := dbpkg.Session{
		ID:        uuid.New().String(),
		ProjectID: src.ProjectID,
		Name:      body.Name,
		Model:     src.Model,
		Status:    string(sessionpkg.StatusIdle),
		ParentID:  id,
	}
	if err := dbpkg.InsertSession(h.db, newSess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if body.Message != "" {
		opts := sessionpkg.TurnOptions{
			SessionID:       newSess.ID,
			ProjectID:       src.ProjectID,
			Message:         body.Message,
			Model:           src.Model,
			Name:            body.Name,
			WorkDirs:        proj.WorkDirs,
			ClaudeConfigDir: proj.ClaudeConfigDir,
			ForkFrom:        id,
		}
		if err := h.manager.StartTurn(opts); err != nil {
			log.Printf("api: fork turn: %v", err)
		}
	}

	jsonResp(w, http.StatusCreated, newSess)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	messages, err := dbpkg.ListMessages(h.db, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []dbpkg.Message{}
	}
	jsonResp(w, http.StatusOK, messages)
}

func (h *Handler) wsUpgrade(w http.ResponseWriter, r *http.Request) {
	sessID := r.URL.Query().Get("session_id")
	roleStr := r.URL.Query().Get("role")

	if sessID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	role := wspkg.RoleClient
	if roleStr == "hook" {
		role = wspkg.RoleHook
	}

	client, err := h.hub.Upgrade(w, r, sessID, role)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	// On client connect, re-send any pending hook so the modal shows up after reconnect
	if role == wspkg.RoleClient {
		if ph, ok, _ := dbpkg.GetPendingHook(h.db, sessID); ok {
			h.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
				"type":       "hook_pending",
				"session_id": sessID,
				"tool_name":  ph.ToolName,
				"tool_input": json.RawMessage(ph.ToolInput),
			})
		}
	}

	_ = client
}

// ---- helpers ----

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
