package cdp

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// state is what survives a restart of lucode about the Edge it started, so the next
// browser_launch (or any browser tool) finds it again even on a custom port and can close it.
// The websocket url is not kept: it changes on every start and /json/version gives it back.
type state struct {
	Port     int    `json:"port"`
	PID      int    `json:"pid"`
	Headless bool   `json:"headless"`
	Profile  string `json:"profile"`
}

func statePath() string {
	if p := os.Getenv("LUCODE_EDGE_STATE"); p != "" { // tests point it at a temp file
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", "edge-state.json")
}

func loadState() (state, bool) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return state{}, false
	}
	var s state
	if json.Unmarshal(data, &s) != nil || s.Port == 0 {
		return state{}, false
	}
	return s, true
}

func saveState(s state) {
	if err := os.MkdirAll(filepath.Dir(statePath()), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(statePath(), data, 0o644)
	}
}

func clearState() { _ = os.Remove(statePath()) }
