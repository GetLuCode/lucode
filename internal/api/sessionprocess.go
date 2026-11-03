package api

import "net/http"

// getSessionProcess says whether the session has a persistent claude process alive.
func (h *Handler) getSessionProcess(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, h.manager.ProcessInfoOf(r.PathValue("id")))
}

// closeSessionProcess closes the session's persistent claude process. The next message
// starts a new one and resumes the conversation, so nothing is lost; it is refused while
// a turn is running (cancel the turn first).
func (h *Handler) closeSessionProcess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.manager.TurnActive(id) {
		http.Error(w, "a turn is running: stop it first", http.StatusConflict)
		return
	}
	h.manager.CloseLive(id)
	w.WriteHeader(http.StatusNoContent)
}
