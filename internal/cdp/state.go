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

// statePath is the state file of a browser: edge-state.json for the main one,
// edge-state-<name>.json for a named one.
func statePath(name string) string {
	file := "edge-state.json"
	if name != "" {
		file = "edge-state-" + name + ".json"
	}
	if p := os.Getenv("LUCODE_EDGE_STATE"); p != "" { // tests point it at a temp file
		if name != "" {
			return p + "." + name
		}
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", file)
}

func loadState(name string) (state, bool) {
	data, err := os.ReadFile(statePath(name))
	if err != nil {
		return state{}, false
	}
	var s state
	if json.Unmarshal(data, &s) != nil || s.Port == 0 {
		return state{}, false
	}
	return s, true
}

func saveState(name string, s state) {
	if err := os.MkdirAll(filepath.Dir(statePath(name)), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(statePath(name), data, 0o644)
	}
}

func clearState(name string) { _ = os.Remove(statePath(name)) }
