// Package api exposes the aiapi HTTP surface. After the Claude → MiniMax
// pivot the surface is intentionally small: health, turn submission, turn
// streaming, and turn snapshot. There is no account CRUD — the API key is a
// process-level singleton configured at startup.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-orchestrator/internal/aiapi/executor"
	"claude-orchestrator/internal/aiapi/executor/checkpoint"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// Server holds the HTTP mux and the in-memory turn store. It also caches the
// default model name to apply when a turn request omits one.
type Server struct {
	store        *TurnStore
	mux          *http.ServeMux
	defaultModel string
	nc           *nats.Conn // optional: when set, tools require orchestrator approval
}

// SetNATSConn wires the orchestrator permission gate. When nc is non-nil,
// every tool call is gated through a NATS request/reply to the orchestrator
// before execution. Call this after NewServer if NATS is configured.
func (s *Server) SetNATSConn(nc *nats.Conn) { s.nc = nc }

// lazyPermNC is a package-level cached NATS connection used when the server
// was not started with --nats-url but the orchestrator passes nats_url in
// the turn request body. Created on first use; reused across requests.
var (
	lazyPermNC   *nats.Conn
	lazyPermNCMu sync.Mutex
)

// getOrConnectNATS returns an existing NATS connection or connects to url.
// Returns nil if the connection fails (tools will always-allow as fallback).
func getOrConnectNATS(url string) *nats.Conn {
	if url == "" {
		return nil
	}
	lazyPermNCMu.Lock()
	defer lazyPermNCMu.Unlock()
	if lazyPermNC != nil && lazyPermNC.IsConnected() {
		return lazyPermNC
	}
	nc, err := nats.Connect(url,
		nats.Timeout(5*time.Second),
		nats.RetryOnFailedConnect(false),
	)
	if err != nil {
		log.Printf("[api] lazy NATS connect %s failed: %v — tools will run without permission gate", url, err)
		return nil
	}
	lazyPermNC = nc
	log.Printf("[api] lazy NATS connected — %s", url)
	return nc
}

// NewServer builds a server with the given fallback model name (used when a
// /turns POST doesn't specify a model).
func NewServer(defaultModel string) *Server {
	if defaultModel == "" {
		defaultModel = "MiniMax-M2.7"
	}
	s := &Server{
		store:        NewTurnStore(),
		mux:          http.NewServeMux(),
		defaultModel: defaultModel,
	}
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /turns", s.handlePostTurn)
	s.mux.HandleFunc("GET /turns/{id}/stream", s.handleStream)
	s.mux.HandleFunc("GET /turns/{id}", s.handleGetTurn)
	s.mux.HandleFunc("GET /models", s.handleListModels)
	// Checkpoint API for the lucode UI: list, get, diff, revert, blob fetch.
	// All under /api/versioning/ so Vite can proxy the whole namespace to 8091.
	s.mux.HandleFunc("GET /api/versioning/checkpoints", s.handleListCheckpoints)
	s.mux.HandleFunc("GET /api/versioning/checkpoints/{id}", s.handleGetCheckpoint)
	s.mux.HandleFunc("GET /api/versioning/diff", s.handleDiff)
	s.mux.HandleFunc("GET /api/versioning/diff/file", s.handleDiffFile)
	s.mux.HandleFunc("POST /api/versioning/revert/{id}", s.handleRevert)
	s.mux.HandleFunc("GET /api/versioning/blob/{hash}", s.handleBlob)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

// postTurnRequest is the JSON body of POST /turns. Kept slim: anything
// provider-specific (Claude config dir, account id, etc.) is dropped.
type postTurnRequest struct {
	SessionID      string                    `json:"session_id"`
	Message        string                    `json:"message"`
	Model          string                    `json:"model"`
	WorkDirs       []string                  `json:"work_dirs"`
	History        []executor.HistoryMessage `json:"history"`
	ImageBase64    string                    `json:"image_base64"`
	ImageMediaType string                    `json:"image_media_type"`
	Thinking       string                    `json:"thinking"`
	// NATSUrl is optional: when set and the server has no pre-configured NATS
	// connection, aiapi lazily connects here for the permission gate.
	NATSUrl string `json:"nats_url"`
}

// handlePostTurn validates the request, creates a turn in the in-memory
// store, and kicks off execution in a goroutine. The response is 202 with
// the turn id and the SSE stream URL.
func (s *Server) handlePostTurn(w http.ResponseWriter, r *http.Request) {
	var req postTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = s.defaultModel
	}
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}

	turn := s.store.New(req.SessionID)
	log.Printf("[turns] received session=%s turn=%s model=%s", req.SessionID, turn.ID, req.Model)

	turnReq := executor.TurnRequest{
		SessionID:      req.SessionID,
		Message:        req.Message,
		Model:          req.Model,
		WorkDirs:       req.WorkDirs,
		History:        req.History,
		ImageBase64:    req.ImageBase64,
		ImageMediaType: req.ImageMediaType,
		Thinking:       req.Thinking,
	}

	permNC := s.nc
	if permNC == nil && req.NATSUrl != "" {
		permNC = getOrConnectNATS(req.NATSUrl)
	}
	var perm *executor.PermissionClient
	if permNC != nil {
		perm = &executor.PermissionClient{NC: permNC, SessionID: req.SessionID}
	}

	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[api] turn %s panic: %v", turn.ID, p)
				turn.Publish(map[string]any{
					"type":   "session_status",
					"status": "error",
					"error":  fmt.Sprintf("panic: %v", p),
				})
			}
		}()
		executor.Run(context.Background(), turnReq, turn.Publish, perm)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"turn_id":    turn.ID,
		"session_id": req.SessionID,
		"stream_url": "/turns/" + turn.ID + "/stream",
	})
}

// handleStream subscribes the HTTP client to the turn's event stream and
// relays each event as an SSE `data: {...}` frame until the turn finishes.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turn, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, _ := w.(http.Flusher)

	turn.Stream(r.Context(), func(ev map[string]any) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// handleGetTurn returns a non-streaming snapshot of the turn: its id, session
// id, done flag, and accumulated events.
func (s *Server) handleGetTurn(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/turns/")
	turn, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return
	}

	events, _, done := turn.Subscribe(0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"turn_id":    turn.ID,
		"session_id": turn.SessionID,
		"done":       done,
		"events":     events,
	})
}

// handleListModels is a hardcoded catalog of known MiniMax models. The UI
// uses this to populate the model picker. Order = display order.
func (s *Server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	models := []map[string]any{
		{"id": "haiku", "label": "Haiku 5.5", "description": "Claude — fast and light."},
		{"id": "sonnet", "label": "Sonnet 5.5", "description": "Claude — balanced."},
		{"id": "opus", "label": "Opus 5.5", "description": "Claude — most capable."},
		{"id": "MiniMax-M3", "label": "M3", "description": "Top of line — strongest reasoning."},
		{"id": "MiniMax-M2.7", "label": "M2.7", "description": "Balanced default."},
		{"id": "MiniMax-M2.7-highspeed", "label": "M2.7 Highspeed", "description": "Faster, lower cost."},
		{"id": "MiniMax-M2.5", "label": "M2.5", "description": "Previous generation."},
		{"id": "MiniMax-M2.5-highspeed", "label": "M2.5 Highspeed", "description": "Previous gen highspeed."},
		{"id": "MiniMax-M2.1", "label": "M2.1", "description": "Older model."},
		{"id": "MiniMax-M2.1-highspeed", "label": "M2.1 Highspeed", "description": "Older highspeed."},
		{"id": "MiniMax-M2", "label": "M2", "description": "Earliest M-series."},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
}

// --- Checkpoint API (consumed by the lucode UI) ---

// workDirFromQuery extracts the work_dir query parameter, returning a
// 400 error if it's missing. All checkpoint endpoints require a work dir
// because the store is per-directory.
func workDirFromQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	wd := r.URL.Query().Get("work_dir")
	if wd == "" {
		http.Error(w, "work_dir query parameter required", http.StatusBadRequest)
		return "", false
	}
	return wd, true
}

// openStore is a convenience that opens a checkpoint Store for a work dir
// and writes a 500 if it fails. Returns (store, ok).
func openStore(w http.ResponseWriter, workDir string) (*checkpoint.Store, bool) {
	s, err := checkpoint.NewStore(workDir)
	if err != nil {
		http.Error(w, "open store: "+err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	return s, true
}

// handleListCheckpoints returns all checkpoints for the work_dir, oldest
// first. Used by the UI's "Checkpoints" panel.
func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"work_dir":    wd,
		"head":        store.Head(),
		"checkpoints": store.List(),
	})
}

// handleGetCheckpoint returns a single checkpoint with its full tree.
// The tree is the file list at the time of the snapshot; useful for the
// UI to display "what was in this checkpoint" without re-reading the disk.
func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "checkpoint id required", http.StatusBadRequest)
		return
	}
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	cp, err := store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	tree, err := store.LoadTree(id)
	if err != nil {
		http.Error(w, "load tree: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"checkpoint": cp,
		"tree":       tree,
	})
}

// handleDiff returns the changes between two checkpoints. If to_id is
// empty, the target is the current workDir state (computed on the fly).
// Used by the UI's "Revert" preview.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	from := r.URL.Query().Get("from")
	if from == "" {
		http.Error(w, "from query parameter required", http.StatusBadRequest)
		return
	}
	to := r.URL.Query().Get("to") // optional; empty = current
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	changes, err := store.Diff(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"from":    from,
		"to":      to,
		"changes": changes,
	})
}

// handleRevert restores the workDir to the state at the given checkpoint.
// The revert itself becomes a new checkpoint in the index. Idempotent: a
// no-op revert still appends a checkpoint so the user explicitly sees the
// state confirmation in history.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "checkpoint id required", http.StatusBadRequest)
		return
	}
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	// Refuse early if the checkpoint is pruned — its tree is gone.
	if cp, err := store.Get(id); err == nil && cp.Pruned {
		http.Error(w, "checkpoint is pruned (tree object was deleted by retention policy); cannot revert", http.StatusGone)
		return
	}
	cp, err := store.Revert(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[api] revert %s in %s → new checkpoint %s", id, wd, cp.Tree)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"checkpoint": cp,
	})
}

// handleBlob returns the raw content of a blob (file content at a given
// hash). The UI uses this to render diff previews. Returns the content
// as text/plain for now — proper mime detection is a v2 concern.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	hash := r.PathValue("hash")
	if hash == "" {
		http.Error(w, "blob hash required", http.StatusBadRequest)
		return
	}
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	content, err := store.LoadContent(hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(content)
}

// handleDiffFile returns the unified diff for a single path between two
// blob hashes. Used by the UI's per-file diff view (after listing the
// change set via /api/diff, the UI calls this for each modified file).
//
// Query params:
//   work_dir  required
//   path      required  the relative path of the file
//   from      optional  blob hash in the source tree (empty = file didn't exist)
//   to        optional  blob hash in the target tree (empty = file was removed)
//
// At least one of from/to must be set.
func (s *Server) handleDiffFile(w http.ResponseWriter, r *http.Request) {
	wd, ok := workDirFromQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}
	from := q.Get("from")
	to := q.Get("to")
	if from == "" && to == "" {
		http.Error(w, "at least one of from/to must be set", http.StatusBadRequest)
		return
	}
	store, ok := openStore(w, wd)
	if !ok {
		return
	}
	fd, err := store.DiffFile(path, from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fd)
}
