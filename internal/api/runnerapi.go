package api

import (
	"database/sql"
	"net/http"

	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
)

// NewRunnerHandler is the whole API of a runner-only instance (lucode-runner). A hub
// reaches it over the NATS RPC bridge for the few things it needs from the machine it
// sends turns to: browse its folders to pick a project dir, and manage its
// preconfigured AIs. Nothing else of lucode (projects, kanban, sessions, the web UI)
// exists there, so nothing else can be called.
func NewRunnerHandler(db *sql.DB, bus *natspkg.Bus, manager *sessionpkg.Manager, workingDir string) http.Handler {
	h := New(db, bus, manager, workingDir)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/version", h.version("runner"))
	mux.HandleFunc("GET /api/commands", h.listCommands)
	mux.HandleFunc("GET /api/fs/dirs", h.listDirs)
	mux.HandleFunc("GET /api/worktrees/stale", h.staleWorktreesRPC)
	mux.HandleFunc("POST /api/review/status", h.reviewStatusRPC)
	mux.HandleFunc("POST /api/review/merge", h.reviewMergeRPC)
	mux.HandleFunc("POST /api/review/repo", h.reviewRepoRPC)
	mux.HandleFunc("GET /api/ai-profiles", h.listAIProfiles)
	mux.HandleFunc("POST /api/ai-profiles", h.createAIProfile)
	mux.HandleFunc("POST /api/ai-profiles/{id}/refresh", h.refreshAIProfile)
	mux.HandleFunc("DELETE /api/ai-profiles/{id}", h.deleteAIProfile)
	mux.HandleFunc("GET /api/ai-profiles/{id}/usage", h.aiProfileUsage)
	return recoverMiddleware(mux)
}
