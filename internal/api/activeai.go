package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// The active AI: the account picked in the usage container of the header. Orchestrator
// chats and agents that do not choose an AI of their own ("Project's AI" / "default")
// run with it, as long as it belongs to the runner they run on. Kept in
// ~/.lucode/active-ai.json.

type activeAI struct {
	RunnerID    string `json:"runner_id"`     // runner that owns the AI ("" = none selected)
	AIProfileID string `json:"ai_profile_id"` // preconfigured AI of that runner
}

var activeAIMu sync.Mutex

func activeAIPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", "active-ai.json")
}

func loadActiveAI() activeAI {
	activeAIMu.Lock()
	defer activeAIMu.Unlock()
	var a activeAI
	if data, err := os.ReadFile(activeAIPath()); err == nil {
		_ = json.Unmarshal(data, &a)
	}
	return a
}

func (h *Handler) getActiveAI(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, loadActiveAI())
}

func (h *Handler) putActiveAI(w http.ResponseWriter, r *http.Request) {
	var body activeAI
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.AIProfileID == "" {
		body = activeAI{}
	}
	activeAIMu.Lock()
	defer activeAIMu.Unlock()
	data, _ := json.Marshal(body)
	if err := os.MkdirAll(filepath.Dir(activeAIPath()), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(activeAIPath(), data, 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, body)
}
