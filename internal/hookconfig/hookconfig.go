// Package hookconfig writes the Claude settings.json used by lucode.
// It does NOT modify the user's ~/.claude/settings.json.
package hookconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type permissions struct {
	DefaultMode string `json:"defaultMode"`
}

type settings struct {
	Permissions permissions            `json:"permissions"`
	Hooks       map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookDef `json:"hooks"`
}

// hookDef is a hook entry — either a command or an HTTP hook.
type hookDef struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"` // for type=command
	URL     string `json:"url,omitempty"`     // for type=http
}

// httpHookGroup returns a hookGroup that POSTs to the lucode HTTP hook endpoint.
func httpHookGroup(backendBaseURL, eventName string) hookGroup {
	return hookGroup{
		Matcher: "",
		Hooks:   []hookDef{{Type: "http", URL: backendBaseURL + "/api/hooks/" + eventName}},
	}
}

// buildSettings constructs the settings struct for the given hook binary and backend URL.
func buildSettings(hookBinaryPath, backendBaseURL string) settings {
	return settings{
		Permissions: permissions{DefaultMode: "bypassPermissions"},
		Hooks: map[string][]hookGroup{
			// PermissionRequest: command mode — lucodehook handles interactive approval via NATS.
			"PermissionRequest": {
				{
					Matcher: "",
					Hooks:   []hookDef{{Type: "command", Command: hookBinaryPath}},
				},
			},
			// All other hooks: HTTP mode — lucode backend logs events and always approves.
			"PreToolUse":      {httpHookGroup(backendBaseURL, "PreToolUse")},
			"PostToolUse":     {httpHookGroup(backendBaseURL, "PostToolUse")},
			"SessionStart":    {httpHookGroup(backendBaseURL, "SessionStart")},
			"SessionEnd":      {httpHookGroup(backendBaseURL, "SessionEnd")},
			"UserPromptSubmit": {httpHookGroup(backendBaseURL, "UserPromptSubmit")},
			"Stop":            {httpHookGroup(backendBaseURL, "Stop")},
			"SubagentStop":    {httpHookGroup(backendBaseURL, "SubagentStop")},
		},
	}
}

// BuildJSON returns the Claude settings JSON string for the given hook binary path and backend base URL.
// This can be passed inline via --settings='{...}' to each claude process.
func BuildJSON(hookBinaryPath, backendBaseURL string) (string, error) {
	s := buildSettings(hookBinaryPath, backendBaseURL)
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(data), nil
}

// WriteSettings writes (or overwrites) settingsPath with a Claude settings file
// that registers the lucodehook for PermissionRequest and HTTP hooks for all other events.
func WriteSettings(settingsPath, hookBinaryPath, backendBaseURL string) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	s := buildSettings(hookBinaryPath, backendBaseURL)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	return os.WriteFile(settingsPath, append(data, '\n'), 0644)
}
