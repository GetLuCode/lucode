package api

import (
	"net/http"
	"strconv"

	dbpkg "claude-orchestrator/internal/db"
)

// notARunner answers for the hub itself when a runner-only action is asked of it: lucode
// orchestrates, only runners execute claude and have a history of what they launched.
func notARunner(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "this instance is the hub, not a runner: it launches no claude commands", http.StatusNotFound)
}

// listCommands is the history of claude commands started on THIS instance (newest
// first). A runner serves it over the RPC bridge, so the hub can show what each
// runner launched; the hub reaches it with /api/runners/{rid}/commands.
func (h *Handler) listCommands(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cmds, err := dbpkg.ListClaudeCommands(h.db, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, cmds)
}

// getSessionCommand is the command the session's claude was last started with,
// wherever it ran (a runner tells the hub what it launched).
func (h *Handler) getSessionCommand(w http.ResponseWriter, r *http.Request) {
	c, err := dbpkg.LatestClaudeCommand(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "no command recorded for this session yet", http.StatusNotFound)
		return
	}
	jsonResp(w, http.StatusOK, c)
}
