// Package permissions implements Claude-Code-style hierarchical permission
// rules for tool execution. Rules live in three layers (lowest precedence
// first): user-level (~/.lucode/settings.json), project-level
// (<workdir>/.lucode/settings.json), and session-level (per-session DB
// overrides). Match precedence is deny > allow > ask > miss.
package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RuleSource identifies where a rule came from. Useful for the UI to
// disambiguate user vs. project vs. session-scope rules.
type RuleSource string

const (
	SourceUserFile    RuleSource = "user"
	SourceProjectFile RuleSource = "project"
	SourceSession     RuleSource = "session"
)

// Decision is the result of a permission check.
type Decision int

const (
	// Miss means no rule matched; the caller should prompt the user.
	Miss Decision = iota
	// Allow means the rule matched with explicit allow.
	Allow
	// Deny means the rule matched with explicit deny.
	Deny
	// Ask means the rule explicitly says "always prompt the user", even
	// though a broader allow rule might also exist. Ask wins when both
	// are configured.
	Ask
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	default:
		return "miss"
	}
}

// DecisionFromString parses a decision name (as used in the wire protocol
// with aiapi). Unknown values map to Miss.
func DecisionFromString(s string) Decision {
	switch s {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "ask":
		return Ask
	}
	return Miss
}

// Rule is a single permission entry. InputRegex is a map of field-name to
// regex pattern, both stored as strings and compiled at load time. A nil
// InputRegex matches any input of the named tool.
type Rule struct {
	ID         string            `json:"id,omitempty"`
	Tool       string            `json:"tool"`
	InputRegex map[string]string `json:"input_regex,omitempty"`
	Reason     string            `json:"reason,omitempty"`
}

// PermissionsConfig is the on-disk shape of a settings.json permissions
// block. All three buckets are optional.
type PermissionsConfig struct {
	Allow []Rule `json:"allow,omitempty"`
	Deny  []Rule `json:"deny,omitempty"`
	Ask   []Rule `json:"ask,omitempty"`
}

// settingsFile is the top-level schema of ~/.lucode/settings.json. We only
// read the "permissions" block; other top-level keys are ignored.
type settingsFile struct {
	Permissions PermissionsConfig `json:"permissions"`
}

// DefaultUserSettingsPath returns the platform-appropriate user-level
// settings path. Returns "" if $HOME / %USERPROFILE% is unset.
func DefaultUserSettingsPath() string {
	if h := os.Getenv("USERPROFILE"); h != "" {
		return filepath.Join(h, ".lucode", "settings.json")
	}
	if h, _ := os.UserHomeDir(); h != "" {
		return filepath.Join(h, ".lucode", "settings.json")
	}
	return ""
}

// ProjectSettingsPath returns the conventional project-level settings
// path under workDir. Returns "" if workDir is empty.
func ProjectSettingsPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, ".lucode", "settings.json")
}

// LoadSettingsFile reads a settings.json from disk. A missing file is not
// an error: it returns an empty config.
func LoadSettingsFile(path string) (*PermissionsConfig, error) {
	if path == "" {
		return &PermissionsConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PermissionsConfig{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &f.Permissions, nil
}

// WriteSettingsFile writes a settings.json to disk. Creates parent
// directories as needed. Pretty-prints with 2-space indent.
func WriteSettingsFile(path string, cfg *PermissionsConfig) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if cfg == nil {
		cfg = &PermissionsConfig{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(settingsFile{Permissions: *cfg}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// MergedConfig combines a user-level and project-level config. On
// (tool, regex) collisions, project wins. The merged config preserves
// rule order within each bucket so the most-specific rule (added first)
// is preferred at match time.
func MergedConfig(userCfg, projectCfg *PermissionsConfig) *PermissionsConfig {
	if userCfg == nil {
		userCfg = &PermissionsConfig{}
	}
	if projectCfg == nil {
		projectCfg = &PermissionsConfig{}
	}
	merge := func(a, b []Rule) []Rule {
		seen := make(map[string]bool, len(a))
		for _, r := range a {
			seen[ruleKey(r)] = true
		}
		out := append([]Rule(nil), a...)
		for _, r := range b {
			if seen[ruleKey(r)] {
				continue
			}
			seen[ruleKey(r)] = true
			out = append(out, r)
		}
		// Stable sort by specificity: rules with more InputRegex keys
		// come first. Rules with nil InputRegex are last.
		sort.SliceStable(out, func(i, j int) bool {
			return len(out[i].InputRegex) > len(out[j].InputRegex)
		})
		return out
	}
	return &PermissionsConfig{
		Allow: merge(userCfg.Allow, projectCfg.Allow),
		Deny:  merge(userCfg.Deny, projectCfg.Deny),
		Ask:   merge(userCfg.Ask, projectCfg.Ask),
	}
}

// ruleKey returns a stable key for deduplicating rules across layers.
func ruleKey(r Rule) string {
	return r.Tool + "|" + sortedFields(r.InputRegex)
}
