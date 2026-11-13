// Package config manages ~/.aicontrol/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds global aiapi settings persisted to disk.
//
// MiniMax fields:
//   - MiniMaxAPIKey: the raw API key. Empty if not set.
//   - MiniMaxAPIKeyFile: path to a file containing the API key. If set and
//     MiniMaxAPIKey is empty, Load() reads the file at this path and trims
//     whitespace. The file should be mode 0600.
//   - MiniMaxBaseURL: API root, default https://api.minimax.io/v1.
//   - MiniMaxDefaultModel: model used when a turn request doesn't specify one.
type Config struct {
	Addr      string `json:"addr,omitempty"`
	NATSUrl   string `json:"natsUrl,omitempty"`
	NATSCreds string `json:"natsCreds,omitempty"`

	MiniMaxAPIKey       string `json:"minimaxApiKey,omitempty"`
	MiniMaxAPIKeyFile   string `json:"minimaxApiKeyFile,omitempty"`
	MiniMaxBaseURL      string `json:"minimaxBaseUrl,omitempty"`
	MiniMaxDefaultModel string `json:"minimaxDefaultModel,omitempty"`
	// ClaudeCredentialsPath is the Claude Code .credentials.json (or its dir) used for
	// OAuth. Defaults to $CLAUDE_CONFIG_DIR, then ~/.claude.
	ClaudeCredentialsPath string `json:"claudeCredentialsPath,omitempty"`
}

func configPath() string {
	base := os.Getenv("HOME")
	if runtime.GOOS == "windows" {
		base = os.Getenv("USERPROFILE")
	}
	return filepath.Join(base, ".aicontrol", "config.json")
}

// Load reads config.json. Returns zero-value Config if the file does not
// exist. After loading, if MiniMaxAPIKey is empty and MiniMaxAPIKeyFile
// points to a readable file, the key is read from disk.
func Load() (Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.MiniMaxAPIKey == "" && c.MiniMaxAPIKeyFile != "" {
		key, err := os.ReadFile(c.MiniMaxAPIKeyFile)
		if err != nil {
			return c, fmt.Errorf("read minimax api key from %s: %w", c.MiniMaxAPIKeyFile, err)
		}
		c.MiniMaxAPIKey = trim(string(key))
	}
	return c, nil
}

// Save writes cfg to config.json, creating the directory if needed.
func Save(cfg Config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create .aicontrol dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Path returns the resolved path to config.json (for display purposes).
func Path() string { return configPath() }

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == '\n' || s[0] == '\r' || s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
