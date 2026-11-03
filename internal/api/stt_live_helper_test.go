package api

import "net/http"

func httptestHandler(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/stt", h.sttSocket)
	return mux
}
