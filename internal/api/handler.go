package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	authclient "claude-orchestrator/internal/authclient"
	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/permissions"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
)

// toolNameRe matches valid Anthropic tool names: ^[a-zA-Z0-9_-]{1,128}$
var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type cachedEntry struct {
	body      json.RawMessage
	expiresAt time.Time
}

type Handler struct {
	db         *sql.DB
	bus        *natspkg.Bus
	manager    *sessionpkg.Manager
	workingDir string
	ollamaHost string
	auth       *authclient.Client
	aiAPIURL   string
	minimaxSubKey string

	cacheMu      sync.Mutex
	profileCache map[string]cachedEntry
	usageCache   map[string]cachedEntry
}

func New(db *sql.DB, bus *natspkg.Bus, manager *sessionpkg.Manager, workingDir string) *Handler {
	return &Handler{
		db:           db,
		bus:          bus,
		manager:      manager,
		workingDir:   workingDir,
		ollamaHost:   "http://localhost:11434",
		auth:         authclient.New("http://localhost:8092"),
		profileCache: make(map[string]cachedEntry),
		usageCache:   make(map[string]cachedEntry),
	}
}

func (h *Handler) SetAuthURL(url string) {
	h.auth = authclient.New(url)
}

func (h *Handler) SetAIAPIURL(url string) {
	h.aiAPIURL = url
}

func (h *Handler) SetMinimaxSubscriptionKey(key string) {
	h.minimaxSubKey = key
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
	mux.HandleFunc("DELETE /api/sessions/{id}/turn", h.cancelTurn)
	mux.HandleFunc("POST /api/sessions/{id}/fork", h.forkSession)

	mux.HandleFunc("GET /api/sessions/{id}/messages", h.listMessages)
	mux.HandleFunc("GET /api/sessions/{id}/export", h.exportSession)
	mux.HandleFunc("POST /api/projects/{pid}/sessions/import", h.importSession)
	mux.HandleFunc("GET /api/messages/{id}/context", h.getMessageContext)
	mux.HandleFunc("PATCH /api/messages/{id}/star", h.toggleStarMessage)
	mux.HandleFunc("GET /api/sessions/{id}/starred", h.listStarredMessages)
	mux.HandleFunc("GET /api/sessions/{id}/raw-events", h.listRawEventsHandler)

	mux.HandleFunc("GET /api/search", h.search)

	mux.HandleFunc("GET /api/ideas", h.listIdeas)
	mux.HandleFunc("POST /api/ideas", h.createIdea)
	mux.HandleFunc("PATCH /api/ideas/{id}", h.updateIdea)
	mux.HandleFunc("DELETE /api/ideas/{id}", h.deleteIdea)

	mux.HandleFunc("GET /api/config", h.getConfig)
	mux.HandleFunc("GET /api/fs/dirs", h.listDirs)
	mux.HandleFunc("GET /api/fs/list", h.listFiles)
	mux.HandleFunc("GET /api/fs/file", h.readFile)
	mux.HandleFunc("PATCH /api/projects/{id}", h.updateProject)

	mux.HandleFunc("GET /api/ollama/models", h.listOllamaModels)

	// Claude accounts: removed in the MiniMax pivot. The endpoints stay
	// registered so the frontend (which still has the AccountFooter in this
	// build) gets a clean 410 Gone instead of a confusing 404. The frontend
	// cleanup happens in a separate step.
	mux.HandleFunc("GET /api/accounts", h.removedAccounts)
	mux.HandleFunc("POST /api/accounts", h.removedAccounts)
	mux.HandleFunc("PUT /api/accounts/{id}", h.removedAccounts)
	mux.HandleFunc("DELETE /api/accounts/{id}", h.removedAccounts)
	mux.HandleFunc("POST /api/accounts/validate-dir", h.removedAccounts)
	mux.HandleFunc("GET /api/accounts/{id}/profile", h.removedAccounts)
	mux.HandleFunc("GET /api/accounts/{id}/usage", h.removedAccounts)
	mux.HandleFunc("GET /api/accounts/{id}/stats", h.removedAccounts)

	// Auth + API key routes — proxied to authapi
	authProxy := h.newAuthProxy()
	mux.Handle("/api/auth/", authProxy)
	mux.Handle("/api/apikeys", authProxy)
	mux.Handle("/api/apikeys/", authProxy)

	mux.HandleFunc("GET /api/fs/home-dirs", h.listHomeDirs)

	// User tools (voice command detection)
	mux.HandleFunc("GET /api/tools", h.listUserTools)
	mux.HandleFunc("POST /api/tools", h.createUserTool)
	mux.HandleFunc("GET /api/tools/{id}", h.getUserTool)
	mux.HandleFunc("PATCH /api/tools/{id}", h.updateUserTool)
	mux.HandleFunc("DELETE /api/tools/{id}", h.deleteUserTool)
	mux.HandleFunc("POST /api/tools/{id}/execute", h.executeUserTool)
	mux.HandleFunc("POST /api/command-detect", h.detectCommand)

	// Kanban
	mux.HandleFunc("GET /api/projects/{pid}/kanban/lists", h.listKanbanLists)
	mux.HandleFunc("POST /api/projects/{pid}/kanban/lists", h.createKanbanList)
	mux.HandleFunc("PUT /api/kanban/lists/{id}", h.updateKanbanList)
	mux.HandleFunc("DELETE /api/kanban/lists/{id}", h.deleteKanbanList)
	mux.HandleFunc("GET /api/projects/{pid}/kanban/cards", h.listKanbanCards)
	mux.HandleFunc("POST /api/projects/{pid}/kanban/cards", h.createKanbanCard)
	mux.HandleFunc("PUT /api/kanban/cards/{id}", h.updateKanbanCard)
	mux.HandleFunc("DELETE /api/kanban/cards/{id}", h.deleteKanbanCard)

	// Agents
	mux.HandleFunc("GET /api/projects/{pid}/agents", h.listAgents)
	mux.HandleFunc("POST /api/projects/{pid}/agents", h.createAgent)
	mux.HandleFunc("GET /api/agents/{id}", h.getAgent)
	mux.HandleFunc("PUT /api/agents/{id}", h.updateAgent)
	mux.HandleFunc("DELETE /api/agents/{id}", h.deleteAgent)
	mux.HandleFunc("GET /api/agents/{id}/sessions", h.listAgentSessions)
	mux.HandleFunc("POST /api/cards/{id}/session", h.cardSession)
	mux.HandleFunc("GET /api/cards/{id}/comments", h.listCardComments)

	// Permissions (hierarchical settings.json + session overrides)
	mux.HandleFunc("GET /api/sessions/{id}/permissions", h.listSessionPermissions)
	mux.HandleFunc("POST /api/sessions/{id}/permissions", h.createSessionPermission)
	mux.HandleFunc("DELETE /api/sessions/{id}/permissions/{ruleId}", h.deleteSessionPermission)
	mux.HandleFunc("GET /api/permissions/preview", h.previewPermission)
	mux.HandleFunc("GET /api/permissions/files", h.permissionFiles)

	// Usage / metrics endpoints
	mux.HandleFunc("GET /api/usage/daily", h.usageDaily)
	mux.HandleFunc("GET /api/usage/models", h.usageModels)
	mux.HandleFunc("GET /api/usage/sessions", h.usageSessions)

	mux.HandleFunc("GET /ws", h.globalWS)

	// Discovery + remote spaces
	mux.HandleFunc("GET /api/instances", h.listInstances)
	mux.HandleFunc("PATCH /api/projects/{id}/aiapi", h.setProjectAIAPI)

	// Process task manager (proxied to aiapi)
	mux.HandleFunc("GET /api/processes", h.listProcesses)
	mux.HandleFunc("POST /api/processes/{id}/kill", h.killProcess)

	// MiniMax token plan
	mux.HandleFunc("GET /api/minimax/token-plan", h.minimaxTokenPlan)
}

// ---- middleware ----

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[http] %s %s", r.Method, r.URL.Path)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PATCH, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newAuthProxy returns an HTTP handler that reverse-proxies to authapi.
// It strips the leading /api prefix from the path.
func (h *Handler) newAuthProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := h.auth.BaseURL() + strings.TrimPrefix(r.URL.Path, "/api")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		log.Printf("[authProxy] %s %s → %s", r.Method, r.URL.Path, target)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			log.Printf("[authProxy] build request error: %v", err)
			http.Error(w, "proxy error", http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[authProxy] upstream error: %v", err)
			http.Error(w, "auth service unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		log.Printf("[authProxy] upstream responded %d", resp.StatusCode)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

// authMiddleware protects all API routes except auth/* and hooks/*.
func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Auth routes are proxied to authapi — no local auth check needed
		if strings.HasPrefix(path, "/api/auth/") || path == "/api/apikeys" || strings.HasPrefix(path, "/api/apikeys/") {
			next.ServeHTTP(w, r)
			return
		}

		if path == "/api/health" || strings.HasPrefix(path, "/api/hooks/") {
			next.ServeHTTP(w, r)
			return
		}

		if token := extractBearerToken(r); token != "" {
			if _, err := h.auth.ValidateToken(token); err == nil {
				next.ServeHTTP(w, r)
				return
			}
		}

		if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
			if _, err := h.auth.ValidateAPIKey(apiKey); err == nil {
				next.ServeHTTP(w, r)
				return
			}
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func Wrap(h *Handler) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	return recoverMiddleware(cors(h.authMiddleware(h.remoteProxy(mux))))
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rc := recover(); rc != nil {
				log.Printf("[http] PANIC %s %s: %v", r.Method, r.URL.Path, rc)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		log.Printf("[http] >> %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// WrapWithSPA is like Wrap but also serves a Single-Page Application from
// spaFS for all routes that are not API or WebSocket routes.
// spaFS should be rooted at the directory containing index.html (i.e. dist/).
func WrapWithSPA(h *Handler, spaFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("/", spaHandler(spaFS))
	return recoverMiddleware(cors(h.authMiddleware(h.remoteProxy(mux))))
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
	provider, resolvedModel := resolveProvider(body.Provider, model)
	storedModel := resolvedModel
	if provider == sessionpkg.ProviderOllama && !strings.HasPrefix(storedModel, "ollama:") {
		storedModel = "ollama:" + storedModel
	}

	sess := dbpkg.Session{
		ID:        uuid.New().String(),
		ProjectID: pid,
		Name:      body.Name,
		Model:     storedModel,
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
	log.Printf("[sendTurn] session=%s account_id=%q from db", id, sess.AccountID)
	proj, err := dbpkg.GetProject(h.db, sess.ProjectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	model := body.Model
	if model == "" {
		model = sess.Model
	}
	// Task sessions run on their assigned agent: its model, personality and
	// executor (claude -p CLI or aiapi) override whatever the client sent.
	var agent *dbpkg.Agent
	if sess.AgentID != "" {
		if a, err := dbpkg.GetAgent(h.db, sess.AgentID); err == nil {
			agent = &a
			model = a.Model
		}
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

	if agent != nil {
		opts.Executor = agent.Executor
		opts.AgentName = agent.Name
		opts.Tools = agent.Tools
		if card, err := dbpkg.GetKanbanCard(h.db, sess.CardID); err == nil {
			opts.SystemPrompt = agentSystemPrompt(*agent, card)
		} else {
			opts.SystemPrompt = agent.Personality
		}
	}

	// If the session has an account, pass it to aiapi (which will resolve it to the claude_config_dir)
	if sess.AccountID != "" {
		log.Printf("[sendTurn] session has account_id=%q, passing to aiapi", sess.AccountID)
		opts.AccountID = sess.AccountID
	} else {
		log.Printf("[sendTurn] no account_id in session, using default config")
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

func (h *Handler) cancelTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	log.Printf("[cancelTurn] session=%s", id)

	ps := h.manager.CancelTurn(id)
	if ps == nil {
		http.Error(w, "no active turn", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) exportSession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAuth(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	sess, err := dbpkg.GetSession(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	messages, err := dbpkg.ListMessages(h.db, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	payload := map[string]any{
		"version":     1,
		"session":     sess,
		"messages":    messages,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
	}
	safe := regexp.MustCompile(`[^a-zA-Z0-9_\-]`).ReplaceAllString(sess.Name, "_")
	if safe == "" {
		safe = "session"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, safe))
	json.NewEncoder(w).Encode(payload)
}

func (h *Handler) importSession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAuth(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pid := r.PathValue("pid")
	var payload struct {
		Session  dbpkg.Session   `json:"session"`
		Messages []dbpkg.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	newSess := dbpkg.Session{
		ID:        uuid.New().String(),
		ProjectID: pid,
		Name:      payload.Session.Name + " (imported)",
		Model:     payload.Session.Model,
		Status:    "idle",
	}
	if err := dbpkg.InsertSession(h.db, newSess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, msg := range payload.Messages {
		msg.ID = uuid.New().String()
		msg.SessionID = newSess.ID
		dbpkg.InsertMessageWithTime(h.db, msg)
	}
	// Update message_count in one shot
	h.db.Exec(`UPDATE sessions SET message_count=? WHERE id=?`, len(payload.Messages), newSess.ID)
	jsonResp(w, http.StatusCreated, newSess)
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

func (h *Handler) listRawEventsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	events, err := dbpkg.ListRawEvents(h.db, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []dbpkg.RawEvent{}
	}

	jsonResp(w, http.StatusOK, events)
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

// listFiles returns both subdirectories and files for the file browser drawer.
// `project_id` (optional): if set, browsing is constrained to the project's
// first work_dir. Otherwise it falls back to the workingDir / home root, like
// listDirs.
func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	effectiveRoot, err := h.resolveFsRoot(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pathParam := r.URL.Query().Get("path")
	if pathParam == "" {
		pathParam = effectiveRoot
	}

	absPath, absRoot, err := h.resolveWithinRoot(pathParam, effectiveRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	type entry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	var out []entry
	for _, e := range entries {
		// Skip hidden files (.git, .DS_Store, etc) but keep regular files
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var size int64
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				continue
			}
			size = info.Size()
		}
		out = append(out, entry{
			Name:  e.Name(),
			Path:  filepath.Join(absPath, e.Name()),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}

	parent := ""
	if absPath != absRoot {
		parent = filepath.Dir(absPath)
		if parent != absRoot && !strings.HasPrefix(parent, absRoot+string(os.PathSeparator)) {
			parent = ""
		}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"path":   absPath,
		"parent": parent,
		"root":   absRoot,
		"entries": out,
	})
}

// readFile returns the textual content of a single file (≤1MB) inside the
// project's work_dir. Returns an error for binary or oversized files —
// the file browser only previews text, the file preview panel decides how
// to render.
func (h *Handler) readFile(w http.ResponseWriter, r *http.Request) {
	effectiveRoot, err := h.resolveFsRoot(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pathParam := r.URL.Query().Get("path")
	if pathParam == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	absPath, _, err := h.resolveWithinRoot(pathParam, effectiveRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	if info.Size() > 1<<20 {
		http.Error(w, "file too large (>1MB)", http.StatusBadRequest)
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Quick binary sniff: if the first 512 bytes contain a NUL byte, treat
	// as binary so the frontend doesn't try to render it as text.
	binary := false
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	for _, b := range head {
		if b == 0 {
			binary = true
			break
		}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"path":    absPath,
		"content": string(data),
		"size":    info.Size(),
		"binary":  binary,
	})
}

// resolveFsRoot determines the directory the file browser is allowed to
// navigate, preferring the project's work_dir if project_id is provided.
func (h *Handler) resolveFsRoot(r *http.Request) (string, error) {
	if pid := r.URL.Query().Get("project_id"); pid != "" && h.db != nil {
		proj, err := dbpkg.GetProject(h.db, pid)
		if err != nil {
			return "", fmt.Errorf("project not found: %w", err)
		}
		if len(proj.WorkDirs) == 0 || proj.WorkDirs[0] == "" {
			return "", fmt.Errorf("project has no working directory")
		}
		return proj.WorkDirs[0], nil
	}
	if h.workingDir != "" {
		return h.workingDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine root: %w", err)
	}
	return home, nil
}

// resolveWithinRoot normalizes a path and ensures it stays inside the root.
func (h *Handler) resolveWithinRoot(pathParam, root string) (absPath, absRoot string, err error) {
	absPath, err = filepath.Abs(pathParam)
	if err != nil {
		return "", "", fmt.Errorf("invalid path")
	}
	absRoot, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("invalid root")
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("path outside root directory")
	}
	return absPath, absRoot, nil
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
	if providerField == string(sessionpkg.ProviderAnthropic) {
		return sessionpkg.ProviderAnthropic, model
	}
	// Allow "ollama:modelname" shorthand in the model field.
	if after, ok := strings.CutPrefix(model, "ollama:"); ok {
		return sessionpkg.ProviderOllama, after
	}
	return sessionpkg.ProviderClaude, model
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	type accountResp struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		ClaudeConfigDir string `json:"claude_config_dir,omitempty"`
		HasToken        bool   `json:"has_token"`
		CreatedAt       string `json:"created_at"`
	}

	aiapiURL := h.aiAPIURL
	if aiapiURL == "" {
		aiapiURL = "http://localhost:8091"
	}

	resp, err := http.Get(aiapiURL + "/accounts")
	if err != nil {
		http.Error(w, "aiapi unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "aiapi accounts error", resp.StatusCode)
		return
	}

	var raw []struct {
		AccountName     string `json:"accountName"`
		ClaudeConfigDir string `json:"claudeConfigDir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		http.Error(w, "aiapi decode error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]accountResp, 0, len(raw))
	for _, a := range raw {
		out = append(out, accountResp{
			ID:              a.AccountName,
			Name:            a.AccountName,
			ClaudeConfigDir: a.ClaudeConfigDir,
			HasToken:        a.ClaudeConfigDir != "",
			CreatedAt:       "",
		})
	}
	jsonResp(w, http.StatusOK, out)
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name"`
		ClaudeConfigDir string `json:"claude_config_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	aiapiURL := h.aiAPIURL
	if aiapiURL == "" {
		aiapiURL = "http://localhost:8091"
	}
	payload, _ := json.Marshal(map[string]string{
		"accountName":     body.Name,
		"claudeConfigDir": body.ClaudeConfigDir,
	})
	req, _ := http.NewRequest(http.MethodPost, aiapiURL+"/accounts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "aiapi unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	jsonResp(w, http.StatusCreated, struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		ClaudeConfigDir string `json:"claude_config_dir,omitempty"`
		HasToken        bool   `json:"has_token"`
		CreatedAt       string `json:"created_at"`
	}{
		ID:              body.Name,
		Name:            body.Name,
		ClaudeConfigDir: body.ClaudeConfigDir,
		HasToken:        body.ClaudeConfigDir != "",
		CreatedAt:       "",
	})
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name            string `json:"name"`
		ClaudeConfigDir string `json:"claude_config_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.proxyJSONToAIAPI(w, "/accounts/"+id, map[string]string{
		"accountName":     body.Name,
		"claudeConfigDir": body.ClaudeConfigDir,
	}, http.StatusNoContent)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	aiapiURL := h.aiAPIURL
	if aiapiURL == "" {
		aiapiURL = "http://localhost:8091"
	}
	req, _ := http.NewRequest(http.MethodDelete, aiapiURL+"/accounts/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "aiapi unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
}

// validateAccountDir checks whether {dir}/.credentials.json is accessible on this machine.
// This endpoint is intentionally NOT proxied to aiapi so that the NATS remote proxy routes
// it to whichever lucode instance (space/computer) the client is currently targeting.
func (h *Handler) validateAccountDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Dir == "" {
		http.Error(w, "dir is required", http.StatusBadRequest)
		return
	}
	credPath := filepath.Join(body.Dir, ".credentials.json")
	if _, err := os.Stat(credPath); err != nil {
		if os.IsNotExist(err) {
			jsonResp(w, http.StatusUnprocessableEntity, map[string]string{
				"error": fmt.Sprintf(".credentials.json not found in %s", body.Dir),
			})
			return
		}
		jsonResp(w, http.StatusUnprocessableEntity, map[string]string{
			"error": fmt.Sprintf("cannot access directory: %v", err),
		})
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// proxyJSONToAIAPI POSTs/PUTs a JSON body to aiapi and forwards the response.
func (h *Handler) proxyJSONToAIAPI(w http.ResponseWriter, path string, payload any, successStatus int) {
	aiapiURL := h.aiAPIURL
	if aiapiURL == "" {
		aiapiURL = "http://localhost:8091"
	}
	data, _ := json.Marshal(payload)
	method := http.MethodPost
	if successStatus == http.StatusNoContent {
		method = http.MethodPut
	}
	req, _ := http.NewRequest(method, aiapiURL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "aiapi unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *Handler) listHomeDirs(w http.ResponseWriter, r *http.Request) {
	pathParam := r.URL.Query().Get("path")

	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "cannot determine home dir", http.StatusInternalServerError)
		return
	}

	if pathParam == "" {
		pathParam = home
	}

	// Security: path must be under home dir
	absPath, err := filepath.Abs(pathParam)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	absHome, _ := filepath.Abs(home)
	if absPath != absHome && !strings.HasPrefix(absPath, absHome+string(os.PathSeparator)) {
		http.Error(w, "path outside home directory", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(pathParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}

	// Check if .credentials.json exists in the current directory
	_, credErr := os.Stat(filepath.Join(pathParam, ".credentials.json"))
	hasCredentials := credErr == nil

	parent := ""
	if absPath != absHome {
		parent = filepath.Dir(pathParam)
		parentAbs, _ := filepath.Abs(parent)
		if parentAbs != absHome && !strings.HasPrefix(parentAbs, absHome+string(os.PathSeparator)) {
			parent = ""
		}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"path":            pathParam,
		"parent":          parent,
		"root":            home,
		"dirs":            dirs,
		"has_credentials": hasCredentials,
	})
}

func (h *Handler) accountProfile(w http.ResponseWriter, r *http.Request) {
	h.proxyToAIAPI(w, r, "/accounts/"+r.PathValue("id")+"/profile")
}

func (h *Handler) accountUsage(w http.ResponseWriter, r *http.Request) {
	h.proxyToAIAPI(w, r, "/accounts/"+r.PathValue("id")+"/usage"+queryString(r))
}

// proxyToAIAPI forwards the request to aiapi and streams the response back.
func (h *Handler) proxyToAIAPI(w http.ResponseWriter, r *http.Request, path string) {
	aiapiURL := h.aiAPIURL
	if aiapiURL == "" {
		aiapiURL = "http://localhost:8091"
	}
	resp, err := http.Get(aiapiURL + path)
	if err != nil {
		http.Error(w, "aiapi unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func queryString(r *http.Request) string {
	if q := r.URL.RawQuery; q != "" {
		return "?" + q
	}
	return ""
}

func (h *Handler) accountStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acct, err := dbpkg.GetClaudeAccount(h.db, id)
	if err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	statsPath := filepath.Join(acct.ClaudeConfigDir, "stats-cache.json")
	data, err := os.ReadFile(statsPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "stats-cache.json not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// fetchAnthropicRaw calls an Anthropic API endpoint using the account's credentials
// and returns the raw JSON body. Uses stored access_token; falls back to .credentials.json
// only if no token is stored but a claude_config_dir is set.
func (h *Handler) fetchAnthropicRaw(accountID, url string, oauthBeta bool) (json.RawMessage, error) {
	acct, err := dbpkg.GetClaudeAccount(h.db, accountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	var accessToken string
	if acct.AccessToken != "" {
		accessToken = acct.AccessToken
	} else if acct.ClaudeConfigDir != "" {
		_, accessToken, _, _, err = dbpkg.ReadCredentials(acct.ClaudeConfigDir)
		if err != nil {
			return nil, fmt.Errorf("read credentials from dir: %w", err)
		}
	}

	if accessToken == "" {
		return nil, fmt.Errorf("no access token available for account %q", acct.Name)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "claude-code/2.x")
	if oauthBeta {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

// ---- Permission Handlers ----

// sessionWorkDir returns the work_dir for a session. Used by the
// permission endpoints to build a PermissionStore. Falls back to the
// project's first work_dir, then the handler's workingDir.
func (h *Handler) sessionWorkDir(sessID string) string {
	sess, err := dbpkg.GetSession(h.db, sessID)
	if err == nil && sess.ProjectID != "" {
		proj, perr := dbpkg.GetProject(h.db, sess.ProjectID)
		if perr == nil && len(proj.WorkDirs) > 0 {
			return proj.WorkDirs[0]
		}
	}
	return h.workingDir
}

func (h *Handler) listSessionPermissions(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	workDir := h.sessionWorkDir(sessID)
	store, err := permissions.LoadForSession(workDir, h.permissionsAdapter(sessID), sessID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rules, err := permissions.ListSessionRules(h.permissionsAdapter(sessID), sessID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Tag each rule with its source.
	type out struct {
		permissions.Rule
		Source permissions.RuleSource `json:"source"`
	}
	res := make([]out, 0, len(rules))
	for _, rule := range rules {
		res = append(res, out{Rule: rule, Source: permissions.SourceSession})
	}
	_ = store // included in `LoadForSession` to surface file-level paths to the UI
	jsonResp(w, http.StatusOK, map[string]any{
		"session_id":   sessID,
		"user_file":    permissions.DefaultUserSettingsPath(),
		"project_file": permissions.ProjectSettingsPath(workDir),
		"session_rules": res,
	})
}

func (h *Handler) createSessionPermission(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	var body struct {
		Tool       string            `json:"tool"`
		InputRegex map[string]string `json:"input_regex"`
		Scope      string            `json:"scope"` // "allow" | "deny" | "ask"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Tool == "" {
		http.Error(w, "tool required", http.StatusBadRequest)
		return
	}
	rule, err := permissions.AddSessionRule(h.permissionsAdapter(sessID), sessID, body.Scope, permissions.Rule{
		Tool:       body.Tool,
		InputRegex: body.InputRegex,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, http.StatusCreated, rule)
}

func (h *Handler) deleteSessionPermission(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	ruleID := r.PathValue("ruleId")
	if err := permissions.RemoveSessionRule(h.permissionsAdapter(sessID), sessID, ruleID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) previewPermission(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	workDir := q.Get("workdir")
	tool := q.Get("tool")
	input := q.Get("input")
	if tool == "" {
		http.Error(w, "tool query param required", http.StatusBadRequest)
		return
	}
	store, err := permissions.LoadForSession(workDir, nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var inputRaw json.RawMessage
	if input != "" {
		inputRaw = json.RawMessage(input)
	}
	match, err := store.Match(tool, inputRaw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"decision": match.Decision.String(),
		"source":   match.Source,
		"path":     match.Path,
		"rule":     match.Rule,
	})
}

func (h *Handler) permissionFiles(w http.ResponseWriter, r *http.Request) {
	workDir := r.URL.Query().Get("workdir")
	jsonResp(w, http.StatusOK, map[string]any{
		"user_file":    permissions.DefaultUserSettingsPath(),
		"project_file": permissions.ProjectSettingsPath(workDir),
	})
}

// permissionsAdapter returns a SessionStore scoped to one session so
// the per-session CRUD only ever sees its own rows.
func (h *Handler) permissionsAdapter(sessID string) permissions.SessionStore {
	return permAdapter{db: h.db, sessID: sessID}
}

type permAdapter struct {
	db      *sql.DB
	sessID  string
}

func (a permAdapter) InsertSessionRule(rule permissions.SessionRuleRow) error {
	rule.SessionID = a.sessID
	return dbpkg.InsertSessionRule(a.db, dbpkg.SessionRuleRow{
		ID:         rule.ID,
		SessionID:  rule.SessionID,
		Tool:       rule.Tool,
		InputRegex: rule.InputRegex,
		Scope:      rule.Scope,
		CreatedAt:  rule.CreatedAt,
	})
}

func (a permAdapter) ListSessionRules(sessID string) ([]permissions.SessionRuleRow, error) {
	rows, err := dbpkg.ListSessionRules(a.db, a.sessID)
	if err != nil {
		return nil, err
	}
	out := make([]permissions.SessionRuleRow, len(rows))
	for i, r := range rows {
		out[i] = permissions.SessionRuleRow{
			ID:         r.ID,
			SessionID:  r.SessionID,
			Tool:       r.Tool,
			InputRegex: r.InputRegex,
			Scope:      r.Scope,
			CreatedAt:  r.CreatedAt,
		}
	}
	return out, nil
}

func (a permAdapter) DeleteSessionRule(sessID, ruleID string) error {
	return dbpkg.DeleteSessionRule(a.db, a.sessID, ruleID)
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---- User Tools CRUD ----

func (h *Handler) listUserTools(w http.ResponseWriter, r *http.Request) {
	tools, err := dbpkg.ListUserTools(h.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tools == nil {
		tools = []dbpkg.UserTool{}
	}
	jsonResp(w, http.StatusOK, tools)
}

func (h *Handler) createUserTool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		InputSchema  string `json:"input_schema"`
		Code         string `json:"code"`
		ExecutionEnv string `json:"execution_env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if !toolNameRe.MatchString(body.Name) {
		http.Error(w, "name must match ^[a-zA-Z0-9_-]{1,128}$ (no spaces or special characters)", http.StatusBadRequest)
		return
	}
	if body.ExecutionEnv == "" {
		body.ExecutionEnv = "browser"
	}
	if body.InputSchema == "" {
		body.InputSchema = `{"type":"object","properties":{}}`
	}

	// Validate JSON schema is valid JSON
	var schemaCheck json.RawMessage
	if err := json.Unmarshal([]byte(body.InputSchema), &schemaCheck); err != nil {
		http.Error(w, "input_schema must be valid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	tool := dbpkg.UserTool{
		ID:           uuid.New().String(),
		Name:         body.Name,
		Description:  body.Description,
		InputSchema:  body.InputSchema,
		Code:         body.Code,
		ExecutionEnv: body.ExecutionEnv,
	}
	if err := dbpkg.InsertUserTool(h.db, tool); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "tool with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Re-read to get timestamps
	saved, _ := dbpkg.GetUserTool(h.db, tool.ID)
	jsonResp(w, http.StatusCreated, saved)
}

func (h *Handler) getUserTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tool, err := dbpkg.GetUserTool(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	jsonResp(w, http.StatusOK, tool)
}

func (h *Handler) updateUserTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := dbpkg.GetUserTool(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		InputSchema  *string `json:"input_schema"`
		Code         *string `json:"code"`
		ExecutionEnv *string `json:"execution_env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if body.Name != nil {
		if !toolNameRe.MatchString(*body.Name) {
			http.Error(w, "name must match ^[a-zA-Z0-9_-]{1,128}$ (no spaces or special characters)", http.StatusBadRequest)
			return
		}
		existing.Name = *body.Name
	}
	if body.Description != nil {
		existing.Description = *body.Description
	}
	if body.InputSchema != nil {
		var check json.RawMessage
		if err := json.Unmarshal([]byte(*body.InputSchema), &check); err != nil {
			http.Error(w, "input_schema must be valid JSON", http.StatusBadRequest)
			return
		}
		existing.InputSchema = *body.InputSchema
	}
	if body.Code != nil {
		existing.Code = *body.Code
	}
	if body.ExecutionEnv != nil {
		existing.ExecutionEnv = *body.ExecutionEnv
	}

	if err := dbpkg.UpdateUserTool(h.db, existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	saved, _ := dbpkg.GetUserTool(h.db, id)
	jsonResp(w, http.StatusOK, saved)
}

func (h *Handler) deleteUserTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteUserTool(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Command Detection ----

func (h *Handler) detectCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Transcript string `json:"transcript"`
		AccountID  string `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Transcript == "" {
		http.Error(w, "transcript required", http.StatusBadRequest)
		return
	}

	// Load all user tools to build tool definitions
	tools, err := dbpkg.ListUserTools(h.db)
	if err != nil {
		http.Error(w, "failed to load tools: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(tools) == 0 {
		jsonResp(w, http.StatusOK, map[string]any{"tool_call": nil, "reason": "no tools defined"})
		return
	}

	// Resolve account credentials
	configDir := ""
	if body.AccountID != "" {
		acct, err := dbpkg.GetClaudeAccount(h.db, body.AccountID)
		if err == nil {
			configDir = acct.ClaudeConfigDir
		}
	}
	if configDir == "" {
		http.Error(w, "account_id required with valid credentials", http.StatusBadRequest)
		return
	}

	result, err := h.manager.DetectCommand(body.Transcript, tools, configDir)
	if err != nil {
		log.Printf("[command-detect] error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, result)
}

// ---- Tool Execution (server-side) ----

func (h *Handler) executeUserTool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tool, err := dbpkg.GetUserTool(h.db, id)
	if err != nil {
		http.Error(w, "tool not found", http.StatusNotFound)
		return
	}
	if tool.ExecutionEnv != "orchestrator" {
		http.Error(w, "tool is configured for browser execution", http.StatusBadRequest)
		return
	}

	var body struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := h.manager.ExecuteUserTool(tool, body.Arguments)
	if err != nil {
		jsonResp(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

// ---- HTTP Hook Endpoint ----

// ---- Auth Handlers ----

// authStatus returns whether signup is currently open (only when no users exist).
// ---- Account Credentials ----

func (h *Handler) updateAccountCredentials(w http.ResponseWriter, r *http.Request) {
	// API key authentication required
	_, err := h.requireAPIKey(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Verify account exists and belongs to user
	acct, err := dbpkg.GetClaudeAccount(h.db, id)
	if err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	// Simple ownership check: account must not be owned by anyone else
	// In the new model, we could add a user_id field to claude_accounts
	// For now, we trust API key auth and the endpoint availability

	// Update credentials
	if err := dbpkg.UpdateClaudeAccountCredentials(h.db, id, body.AccessToken, body.RefreshToken, body.ExpiresAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear cache
	h.cacheMu.Lock()
	delete(h.profileCache, id)
	delete(h.usageCache, id)
	h.cacheMu.Unlock()

	jsonResp(w, http.StatusOK, acct)
}

// ---- Auth Helpers ----

// extractBearerToken extracts token from Authorization: Bearer <token> header
func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		return authHeader[7:]
	}
	return ""
}

// requireAuth validates session token via authapi and returns user ID.
func (h *Handler) requireAuth(r *http.Request) (string, error) {
	token := extractBearerToken(r)
	if token == "" {
		return "", fmt.Errorf("missing token")
	}
	res, err := h.auth.ValidateToken(token)
	if err != nil {
		return "", err
	}
	return res.UserID, nil
}

// requireAPIKey validates API key via authapi and returns user ID.
func (h *Handler) requireAPIKey(r *http.Request) (string, error) {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		return "", fmt.Errorf("missing api key")
	}
	res, err := h.auth.ValidateAPIKey(apiKey)
	if err != nil {
		return "", err
	}
	return res.UserID, nil
}

// ---- Usage / Metrics ----

// parseRangeParam maps a "range" query param (7d|30d|90d|all) to a since time.Time.
func parseRangeParam(r *http.Request) time.Time {
	switch r.URL.Query().Get("range") {
	case "7d":
		return time.Now().AddDate(0, 0, -7)
	case "30d":
		return time.Now().AddDate(0, 0, -30)
	case "90d":
		return time.Now().AddDate(0, 0, -90)
	default: // "all" or empty
		return time.Time{} // zero time = no filter
	}
}

func (h *Handler) usageDaily(w http.ResponseWriter, r *http.Request) {
	since := parseRangeParam(r)
	rows, err := dbpkg.AggregateDailyUsage(h.db, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []dbpkg.DailyUsageRow{}
	}
	jsonResp(w, http.StatusOK, rows)
}

func (h *Handler) usageModels(w http.ResponseWriter, r *http.Request) {
	since := parseRangeParam(r)
	rows, err := dbpkg.AggregateModelUsage(h.db, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []dbpkg.ModelUsageRow{}
	}
	jsonResp(w, http.StatusOK, rows)
}

func (h *Handler) usageSessions(w http.ResponseWriter, r *http.Request) {
	since := parseRangeParam(r)
	projectID := r.URL.Query().Get("project_id")
	rows, err := dbpkg.AggregateSessionUsage(h.db, since, projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []dbpkg.SessionUsageRow{}
	}
	jsonResp(w, http.StatusOK, rows)
}

// remoteProxy intercepts requests with X-Lucode-Remote header and forwards
// them to the target lucode instance via NATS. Other requests pass through.
func (h *Handler) remoteProxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetID := r.Header.Get("X-Lucode-Remote")
		if targetID == "" {
			next.ServeHTTP(w, r)
			return
		}
		resp, err := h.bus.RPCRequest(targetID, r)
		if err != nil {
			log.Printf("[remoteProxy] rpc %s: %v", targetID, err)
			http.Error(w, "remote instance unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

// listInstances returns self + all discovered peers (lucode + aiapi).
func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	self := h.bus.SelfInfo()
	peers := h.bus.Peers()
	jsonResp(w, http.StatusOK, map[string]any{
		"self":  self,
		"peers": peers,
	})
}

// setProjectAIAPI updates the aiapi_instance_id for a project.
func (h *Handler) setProjectAIAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		AIAPIInstanceID string `json:"aiapi_instance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := dbpkg.UpdateProjectAIAPIInstance(h.db, id, body.AIAPIInstanceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listProcesses(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "removed: claude process list no longer supported", http.StatusGone)
}

func (h *Handler) killProcess(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "removed: claude process kill no longer supported", http.StatusGone)
}

// removedAccounts is the 410 Gone stub wired into every /api/accounts/*
// route after the Claude → MiniMax pivot.
func (h *Handler) removedAccounts(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "removed: claude accounts are not supported in the MiniMax build", http.StatusGone)
}

// ---- Agents ----

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	agents, err := dbpkg.ListAgents(h.db, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []dbpkg.Agent{}
	}
	json.NewEncoder(w).Encode(agents)
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		Name        string   `json:"name"`
		Model       string   `json:"model"`
		AccountID   string   `json:"account_id"`
		Personality string   `json:"personality"`
		Tools       []string `json:"tools"`
		Executor    string   `json:"executor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Executor != "claude" {
		body.Executor = "aiapi"
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if body.Model == "" {
		body.Model = "sonnet"
	}
	if len(body.Tools) == 0 {
		body.Tools = []string{"Bash", "Edit", "Read", "Write"}
	}
	a := dbpkg.Agent{
		ID:          uuid.New().String(),
		ProjectID:   pid,
		Name:        body.Name,
		Model:       body.Model,
		Executor:    body.Executor,
		AccountID:   body.AccountID,
		Personality: body.Personality,
		Tools:       body.Tools,
	}
	if err := dbpkg.InsertAgent(h.db, a); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, _ = dbpkg.GetAgent(h.db, a.ID)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(a)
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := dbpkg.GetAgent(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(a)
}

func (h *Handler) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := dbpkg.GetAgent(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Name        *string  `json:"name"`
		Model       *string  `json:"model"`
		AccountID   *string  `json:"account_id"`
		Personality *string  `json:"personality"`
		Tools       []string `json:"tools"`
		Executor    *string  `json:"executor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Executor != nil {
		if *body.Executor == "claude" {
			a.Executor = "claude"
		} else {
			a.Executor = "aiapi"
		}
	}
	if body.Name != nil {
		a.Name = *body.Name
	}
	if body.Model != nil {
		a.Model = *body.Model
	}
	if body.AccountID != nil {
		a.AccountID = *body.AccountID
	}
	if body.Personality != nil {
		a.Personality = *body.Personality
	}
	if body.Tools != nil {
		a.Tools = body.Tools
	}
	if err := dbpkg.UpdateAgent(h.db, a); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, _ = dbpkg.GetAgent(h.db, id)
	json.NewEncoder(w).Encode(a)
}

func (h *Handler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteAgent(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Kanban Lists ----

func (h *Handler) listKanbanLists(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if err := dbpkg.EnsureDefaultKanbanLists(h.db, pid); err != nil {
		log.Printf("kanban: default lists for %s: %v", pid, err)
	}
	lists, err := dbpkg.ListKanbanLists(h.db, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lists == nil {
		lists = []dbpkg.KanbanList{}
	}
	json.NewEncoder(w).Encode(lists)
}

func (h *Handler) createKanbanList(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	l := dbpkg.KanbanList{
		ID:        uuid.New().String(),
		ProjectID: pid,
		Name:      body.Name,
		Position:  body.Position,
	}
	if err := dbpkg.InsertKanbanList(h.db, l); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	l, _ = dbpkg.GetKanbanList(h.db, l.ID)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(l)
}

func (h *Handler) updateKanbanList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	l, err := dbpkg.GetKanbanList(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Position *int    `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		l.Name = *body.Name
	}
	if body.Position != nil {
		l.Position = *body.Position
	}
	if err := dbpkg.UpdateKanbanList(h.db, l.ID, l.Name, l.Position); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	l, _ = dbpkg.GetKanbanList(h.db, id)
	json.NewEncoder(w).Encode(l)
}

func (h *Handler) deleteKanbanList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteKanbanList(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Kanban Cards ----

func (h *Handler) listKanbanCards(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	includeArchived := r.URL.Query().Get("archived") == "true"
	cards, err := dbpkg.ListKanbanCards(h.db, pid, includeArchived)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cards == nil {
		cards = []dbpkg.KanbanCard{}
	}
	json.NewEncoder(w).Encode(cards)
}

func (h *Handler) createKanbanCard(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	var body struct {
		ListID      string   `json:"list_id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		CardType    string   `json:"card_type"`
		Tags        []string `json:"tags"`
		AgentID     string   `json:"agent_id"`
		DependsOn   []string `json:"depends_on"`
		StartDate   string   `json:"start_date"`
		EndDate     string   `json:"end_date"`
		Position    int      `json:"position"`
		ParentID    string   `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Title == "" || body.ListID == "" {
		http.Error(w, "title and list_id required", http.StatusBadRequest)
		return
	}
	if body.CardType == "" {
		body.CardType = "task"
	}
	c := dbpkg.KanbanCard{
		ID:          uuid.New().String(),
		ListID:      body.ListID,
		ProjectID:   pid,
		Title:       body.Title,
		Description: body.Description,
		CardType:    body.CardType,
		Tags:        body.Tags,
		AgentID:     body.AgentID,
		DependsOn:   body.DependsOn,
		StartDate:   body.StartDate,
		EndDate:     body.EndDate,
		Position:    body.Position,
		ParentID:    body.ParentID,
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	if c.DependsOn == nil {
		c.DependsOn = []string{}
	}
	if err := dbpkg.InsertKanbanCard(h.db, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, _ = dbpkg.GetKanbanCard(h.db, c.ID)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(c)
}

func (h *Handler) updateKanbanCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := dbpkg.GetKanbanCard(h.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		ListID      *string  `json:"list_id"`
		Title       *string  `json:"title"`
		Description *string  `json:"description"`
		CardType    *string  `json:"card_type"`
		Tags        []string `json:"tags"`
		AgentID     *string  `json:"agent_id"`
		DependsOn   []string `json:"depends_on"`
		StartDate   *string  `json:"start_date"`
		EndDate     *string  `json:"end_date"`
		Position    *int     `json:"position"`
		Archived    *bool    `json:"archived"`
		ParentID    *string  `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	oldListID := c.ListID
	if body.ListID != nil      { c.ListID = *body.ListID }
	if body.Title != nil       { c.Title = *body.Title }
	if body.Description != nil { c.Description = *body.Description }
	if body.CardType != nil    { c.CardType = *body.CardType }
	if body.Tags != nil        { c.Tags = body.Tags }
	if body.AgentID != nil     { c.AgentID = *body.AgentID }
	if body.DependsOn != nil   { c.DependsOn = body.DependsOn }
	if body.StartDate != nil   { c.StartDate = *body.StartDate }
	if body.EndDate != nil     { c.EndDate = *body.EndDate }
	if body.Position != nil    { c.Position = *body.Position }
	if body.Archived != nil    { c.Archived = *body.Archived }
	if body.ParentID != nil    { c.ParentID = *body.ParentID }
	if err := dbpkg.UpdateKanbanCard(h.db, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, _ = dbpkg.GetKanbanCard(h.db, id)
	// Moving a card between lists drives the workflow: In Progress runs it
	// (or parks it in Blocked), Done releases cards that depended on it.
	if c.ListID != oldListID {
		c = h.afterCardSaved(c)
	}
	json.NewEncoder(w).Encode(c)
}

func (h *Handler) deleteKanbanCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := dbpkg.DeleteKanbanCard(h.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- MiniMax token plan ----

func (h *Handler) minimaxTokenPlan(w http.ResponseWriter, r *http.Request) {
	key := h.minimaxSubKey
	if key == "" {
		key = os.Getenv("MINIMAX_SUBSCRIPTION_KEY")
	}
	if key == "" {
		http.Error(w, `{"error":"MINIMAX_SUBSCRIPTION_KEY not configured"}`, http.StatusServiceUnavailable)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.minimax.io/v1/token_plan/remains", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}


// ---- Tasks (kanban cards) as agent sessions ----

// cardSession returns the chat session of a task, creating it on first use.
// The session runs on the card's assigned agent; reassigning the card moves the
// same conversation to the new agent.
func (h *Handler) cardSession(w http.ResponseWriter, r *http.Request) {
	card, err := dbpkg.GetKanbanCard(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if card.AgentID == "" {
		http.Error(w, "assign an agent to this task first", http.StatusBadRequest)
		return
	}
	agent, err := dbpkg.GetAgent(h.db, card.AgentID)
	if err != nil {
		http.Error(w, "assigned agent not found", http.StatusBadRequest)
		return
	}

	sess, created, err := h.ensureCardSession(card, agent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	jsonResp(w, status, sess)
}

// listAgentSessions lists the task sessions an agent has run, so they can be inspected.
func (h *Handler) listAgentSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := dbpkg.ListSessionsByAgent(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, sessions)
}
