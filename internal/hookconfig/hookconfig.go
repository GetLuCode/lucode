// Package hookconfig writes the Claude settings.json used by lucode.
// It does NOT modify the user's ~/.claude/settings.json.
package hookconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type settings struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// WriteSettings writes (or overwrites) settingsPath with a minimal Claude
// settings file that registers hookBinaryPath as a PreToolUse hook.
func WriteSettings(settingsPath, hookBinaryPath string) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	s := settings{
		Hooks: map[string][]hookGroup{
			"PreToolUse": {
				{
					Matcher: "",
					Hooks:   []hookCmd{{Type: "command", Command: hookBinaryPath}},
				},
			},
		},
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	return os.WriteFile(settingsPath, append(data, '\n'), 0644)
}
