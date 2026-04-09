package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
)

type Handler struct {
	db         *sql.DB
	bus        *natspkg.Bus
	manager    *sessionpkg.Manager
	workingDir string
	ollamaHost string
}

func New(db *sql.DB, bus *natspkg.Bus, manager *sessionpkg.Manager, workingDir string) *Handler {
	return &Handler{db: db, bus: bus, manager: manager, workingDir: workingDir, ollamaHost: "http://localhost:11434"}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", h.health)

	mux.HandleFunc("POST /api/projects", h.createProject)
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("DELETE /api/projects/{id}", h.deleteProject)

	mux.HandleFunc("POST /api/projects/{pid}/sessions", h.createSession)
	mux.HandleFunc("GET /api/projects/{pid}/sessions", h.listSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.deleteSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", h.updateSessionName)

	mux.HandleFunc("POST /api/sessions/{id}/turn", h.sendTurn)
	mux.HandleFunc("POST /api/sessions/{id}/fork", h.forkSession)

	mux.HandleFunc("GET /api/sessions/{id}/messages", h.listMessages)
	mux.HandleFunc("GET /api/messages/{id}/context", h.getMessageContext)
	mux.HandleFunc("PATCH /api/messages/{id}/star", h.toggleStarMessage)
	mux.HandleFunc("GET /api/sessions/{id}/starred", h.listStarredMessages)

	mux.HandleFunc("GET /api/search", h.search)

	mux.HandleFunc("GET /api/ideas", h.listIdeas)
	mux.HandleFunc("POST /api/ideas", h.createIdea)
	mux.HandleFunc("PATCH /api/ideas/{id}", h.updateIdea)
	mux.HandleFunc("DELETE /api/ideas/{id}", h.deleteIdea)

	mux.HandleFunc("GET /api/config", h.getConfig)
	mux.HandleFunc("GET /api/fs/dirs", h.listDirs)
	mux.HandleFunc("PATCH /api/projects/{id}", h.updateProject)

	mux.HandleFunc("GET /api/ollama/models", h.listOllamaModels)

	mux.HandleFunc("GET /", h.globalWS)
}

// ---- middleware ----

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PATCH, OPTIONS")
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

// WrapWithSPA is like Wrap but also serves a Single-Page Application from
// spaFS for all routes that are not API or WebSocket routes.
// spaFS should be rooted at the directory containing index.html (i.e. dist/).
func WrapWithSPA(h *Handler, spaFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("/", spaHandler(spaFS))
	return cors(mux)
}

// spaHandler serves static files from spaFS and falls back to index.html for
// any path that does not match a real file (client-side routing support).
func spaHandler(spaFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(spaFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip leading slash to get the fs-relative path.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "."
		}
		f, err := spaFS.Open(path)
		if err != nil {
			// File not found → serve index.html so the SPA router handles it.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
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
		Name     string `json:"name"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
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
	_, model = resolveProvider(body.Provider, model)

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

func (h *Handler) updateSessionName(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := dbpkg.UpdateSessionName(h.db, id, body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) sendTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Message  string `json:"message"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Effort   string `json:"effort"`
		Thinking string `json:"thinking"`
		Image    struct {
			Base64    string `json:"base64"`
			MediaType string `json:"mediaType"`
		} `json:"image"`
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
	provider, model := resolveProvider(body.Provider, model)

	opts := sessionpkg.TurnOptions{
		SessionID:       id,
		ProjectID:       sess.ProjectID,
		Message:         body.Message,
		Model:           model,
		Provider:        provider,
		WorkDirs:        proj.WorkDirs,
		Effort:          body.Effort,
		Thinking:        body.Thinking,
		ClaudeConfigDir: proj.ClaudeConfigDir,
		Resume:          sess.MessageCount > 0,
		ImageBase64:     body.Image.Base64,
		ImageMediaType:  body.Image.MediaType,
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

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	var beforeSeq *int
	if b := r.URL.Query().Get("before_seq"); b != "" {
		if n, err := strconv.Atoi(b); err == nil {
			beforeSeq = &n
		}
	}

	messages, hasMore, err := dbpkg.ListMessagesPaginated(h.db, id, limit, beforeSeq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []dbpkg.Message{}
	}

	oldestSeq := -1
	if len(messages) > 0 {
		oldestSeq = messages[0].Seq
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"messages":   messages,
		"has_more":   hasMore,
		"oldest_seq": oldestSeq,
	})
}

func (h *Handler) getMessageContext(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("id")

	// Get the message to find its seq and session_id
	message, err := dbpkg.GetMessage(h.db, messageID)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get context: 25 messages before and ~25 after
	// Use before_seq = seq + 50, limit = 50
	beforeSeq := message.Seq + 50
	messages, _, err := dbpkg.ListMessagesPaginated(h.db, message.SessionID, 50, &beforeSeq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []dbpkg.Message{}
	}

	jsonResp(w, http.StatusOK, messages)
}

func (h *Handler) toggleStarMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	starred, err := dbpkg.ToggleStarMessage(h.db, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{"starred": starred})
}

func (h *Handler) listStarredMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	messages, err := dbpkg.ListStarredMessages(h.db, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []dbpkg.Message{}
	}

	jsonResp(w, http.StatusOK, messages)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 3 {
		jsonResp(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}

	results, err := dbpkg.Search(h.db, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []dbpkg.SearchResult{}
	}

	jsonResp(w, http.StatusOK, map[string]any{"results": results})
}

func (h *Handler) listIdeas(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		http.Error(w, "scope required", http.StatusBadRequest)
		return
	}
	ideas, err := dbpkg.ListIdeas(h.db, scope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ideas == nil {
		ideas = []dbpkg.Idea{}
	}
	jsonResp(w, http.StatusOK, ideas)
}

func (h *Handler) createIdea(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Scope == "" || body.Text == "" {
		http.Error(w, "scope and text required", http.StatusBadRequest)
		return
	}
	idea := dbpkg.Idea{
		ID:    uuid.New().String(),
		Scope: body.Scope,
		Text:  body.Text,
	}
	if err := dbpkg.InsertIdea(h.db, idea); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, idea)
}

func (h *Handler) updateIdea(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Done bool `json:"done"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := dbpkg.UpdateIdeaDone(h.db, id, body.Done); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteIdea(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteIdea(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, map[string]any{
		"working_dir": h.workingDir,
	})
}

func (h *Handler) listDirs(w http.ResponseWriter, r *http.Request) {
	pathParam := r.URL.Query().Get("path")

	// Determine the effective root (workingDir or home)
	effectiveRoot := h.workingDir
	if effectiveRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "cannot determine home dir", http.StatusInternalServerError)
			return
		}
		effectiveRoot = home
	}

	if pathParam == "" {
		pathParam = effectiveRoot
	}

	// Security: don't allow navigation above effectiveRoot
	absPath, err := filepath.Abs(pathParam)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	absEffectiveRoot, err := filepath.Abs(effectiveRoot)
	if err != nil {
		http.Error(w, "invalid root dir", http.StatusInternalServerError)
		return
	}
	// Check if absPath is under or equal to effectiveRoot
	if absPath != absEffectiveRoot && !strings.HasPrefix(absPath, absEffectiveRoot+string(os.PathSeparator)) {
		http.Error(w, "path outside root directory", http.StatusBadRequest)
		return
	}

	// List subdirectories
	entries, err := os.ReadDir(pathParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			dirs = append(dirs, entry.Name())
		}
	}

	// Calculate parent: can't go above effectiveRoot
	parent := ""
	if pathParam != effectiveRoot {
		parent = filepath.Dir(pathParam)
		// Ensure parent doesn't go above effectiveRoot
		if parent != effectiveRoot && !strings.HasPrefix(parent, effectiveRoot+string(os.PathSeparator)) {
			parent = ""
		}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"path":   pathParam,
		"parent": parent,
		"root":   effectiveRoot,
		"dirs":   dirs,
	})
}

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := dbpkg.UpdateProjectName(h.db, id, body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listOllamaModels proxies GET /api/ollama/models to the Ollama API and
// returns the list of locally installed models.
func (h *Handler) listOllamaModels(w http.ResponseWriter, r *http.Request) {
	url := strings.TrimRight(h.ollamaHost, "/") + "/api/tags"
	ctx := r.Context()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "ollama: build request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Ollama not running — return empty list so the frontend degrades gracefully.
		jsonResp(w, http.StatusOK, map[string]any{"models": []any{}})
		return
	}
	defer resp.Body.Close()

	// Ollama returns: {"models":[{"name":"gemma4:e2b","size":...,...},...]}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		http.Error(w, "ollama: decode: "+err.Error(), http.StatusBadGateway)
		return
	}

	type modelEntry struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	out := make([]modelEntry, 0, len(payload.Models))
	for _, m := range payload.Models {
		out = append(out, modelEntry{Name: m.Name, Label: m.Name})
	}
	jsonResp(w, http.StatusOK, map[string]any{"models": out})
}

// ---- helpers ----

// resolveProvider determines the AI backend and model name from the request.
//
// The frontend can select Ollama in two ways:
//   - Explicit field:  provider="ollama", model="gemma4:e2b"
//   - Prefixed model:  model="ollama:gemma4:e2b"  (provider field optional)
//
// Everything else defaults to the Claude CLI backend.
func resolveProvider(providerField, model string) (sessionpkg.Provider, string) {
	// Explicit provider field takes precedence.
	if providerField == string(sessionpkg.ProviderOllama) {
		return sessionpkg.ProviderOllama, model
	}
	// Allow "ollama:modelname" shorthand in the model field.
	if after, ok := strings.CutPrefix(model, "ollama:"); ok {
		return sessionpkg.ProviderOllama, after
	}
	return sessionpkg.ProviderClaude, model
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
