// Package hookconfig manages the Claude CLI settings.json to ensure the
// lucodehook binary is registered as a PreToolUse hook.
package hookconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookGroup struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

// Ensure reads {claudeConfigDir}/settings.json and adds a PreToolUse hook
// pointing to hookBinaryPath if one is not already present.
// The file is created if it does not exist. All existing settings are preserved.
func Ensure(claudeConfigDir, hookBinaryPath string) error {
	settingsPath := filepath.Join(claudeConfigDir, "settings.json")

	// Read existing settings (or start with empty object).
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read settings: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse settings: %w", err)
		}
	}

	// Parse the "hooks" section.
	hooks := map[string][]hookGroup{}
	if hRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hRaw, &hooks); err != nil {
			return fmt.Errorf("parse hooks: %w", err)
		}
	}

	// Check if lucodehook is already registered in PreToolUse.
	for _, group := range hooks["PreToolUse"] {
		for _, h := range group.Hooks {
			if h.Type == "command" && strings.Contains(h.Command, "lucodehook") {
				// Already installed.
				return nil
			}
		}
	}

	// Append a new hook group for lucodehook.
	hooks["PreToolUse"] = append(hooks["PreToolUse"], hookGroup{
		Matcher: "",
		Hooks: []hookCmd{
			{Type: "command", Command: hookBinaryPath},
		},
	})

	// Marshal hooks back and merge into the raw settings map.
	hBytes, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	raw["hooks"] = json.RawMessage(hBytes)

	// Write with indentation to keep the file human-readable.
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.MkdirAll(claudeConfigDir, 0755); err != nil {
		return fmt.Errorf("mkdir claude config: %w", err)
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	return nil
}
