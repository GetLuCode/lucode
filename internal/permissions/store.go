package permissions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionStore is the persistence interface the Store needs for
// session-scope rules. The DB layer implements this; tests can use an
// in-memory map.
type SessionStore interface {
	InsertSessionRule(rule SessionRuleRow) error
	ListSessionRules(sessID string) ([]SessionRuleRow, error)
	DeleteSessionRule(sessID, ruleID string) error
}

// SessionRuleRow is the DB shape of a session permission. Defined here
// to keep the permissions package free of db imports (the caller wires
// the real db.SessionRuleRow via the SessionStore impl).
type SessionRuleRow struct {
	ID         string
	SessionID  string
	Tool       string
	InputRegex string // JSON-encoded map[string]string
	Scope      string // "allow" | "deny" | "ask"
	CreatedAt  time.Time
}

// Store is the in-memory permission state for one session. Rules are
// kept in a single flat slice ordered by precedence: deny rules first
// (most specific first), then allow, then ask, then session rules of
// each kind inserted at the head of their bucket. Match iterates and
// returns the first hit.
//
// The Store is immutable after LoadForSession returns; new session
// rules require rebuilding the Store.
type Store struct {
	userPath    string
	projectPath string
	rules       []compiledRule // each carries its own Decision
}

// LoadForSession reads the user-level and project-level settings files
// for workDir, merges them, compiles all rules, and returns a Store.
// Session-scope rules are loaded from sess for sessID.
func LoadForSession(workDir string, sess SessionStore, sessID string) (*Store, error) {
	userPath := DefaultUserSettingsPath()
	projectPath := ProjectSettingsPath(workDir)

	userCfg, err := LoadSettingsFile(userPath)
	if err != nil {
		return nil, fmt.Errorf("user settings: %w", err)
	}
	projectCfg, err := LoadSettingsFile(projectPath)
	if err != nil {
		return nil, fmt.Errorf("project settings: %w", err)
	}
	merged := MergedConfig(userCfg, projectCfg)

	s := &Store{
		userPath:    userPath,
		projectPath: projectPath,
	}

	// Build the rule list. Order within a bucket: most-specific first
	// (already sorted by MergedConfig). Bucket order: deny, allow, ask.
	if err := s.appendBucket(merged.Deny, Deny, userPath, projectPath); err != nil {
		return nil, err
	}
	if err := s.appendBucket(merged.Allow, Allow, userPath, projectPath); err != nil {
		return nil, err
	}
	if err := s.appendBucket(merged.Ask, Ask, userPath, projectPath); err != nil {
		return nil, err
	}

	// Session rules go at the end of their bucket so they shadow
	// file-scope rules of the same decision (added at head).
	if sess != nil && sessID != "" {
		rows, err := sess.ListSessionRules(sessID)
		if err != nil {
			return nil, fmt.Errorf("session rules: %w", err)
		}
		for _, row := range rows {
			dec := sessionDecisionFromScope(row.Scope)
			if dec == Miss {
				continue
			}
			rule := Rule{
				ID:         row.ID,
				Tool:       row.Tool,
				InputRegex: decodeInputRegex(row.InputRegex),
			}
			if err := s.appendOne(rule, dec, SourceSession, sessID); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// appendBucket adds a slice of rules to the store, attributing each to
// the right source file. A rule present in both user and project files
// (which shouldn't happen post-merge, but defensively) is attributed
// to the project file.
func (s *Store) appendBucket(rules []Rule, dec Decision, userPath, projectPath string) error {
	for _, r := range rules {
		// Determine source: project file is checked first because
		// MergedConfig deduplicated user rules that collided with
		// project ones. If a rule is only in the user file, it's a
		// user rule; otherwise it's a project rule.
		src, path := SourceProjectFile, projectPath
		if !hasRuleInFile(r, projectPath) && hasRuleInFile(r, userPath) {
			src, path = SourceUserFile, userPath
		}
		if err := s.appendOne(r, dec, src, path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appendOne(r Rule, dec Decision, src RuleSource, path string) error {
	regex, err := compileRegexes(r)
	if err != nil {
		return err
	}
	tool, err := compileTool(r)
	if err != nil {
		return err
	}
	s.rules = append(s.rules, compiledRule{
		Rule:     r,
		Decision: dec,
		Source:   src,
		Path:     path,
		regex:    regex,
		tool:     tool,
	})
	return nil
}

func hasRuleInFile(r Rule, path string) bool {
	if path == "" {
		return false
	}
	cfg, err := LoadSettingsFile(path)
	if err != nil {
		return false
	}
	for _, candidate := range append(append(cfg.Allow, cfg.Deny...), cfg.Ask...) {
		if ruleKey(candidate) == ruleKey(r) {
			return true
		}
	}
	return false
}

func decodeInputRegex(s string) map[string]string {
	if s == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

func sessionDecisionFromScope(scope string) Decision {
	switch scope {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "ask":
		return Ask
	}
	return Miss
}

// UserPath returns the path to the user-level settings file.
func (s *Store) UserPath() string { return s.userPath }

// ProjectPath returns the path to the project-level settings file.
func (s *Store) ProjectPath() string { return s.projectPath }

// MatchResult is the outcome of a Match call.
type MatchResult struct {
	Decision Decision
	Rule     *Rule
	Source   RuleSource
	Path     string
	Reason   string
}

// Match returns the first rule that matches the given tool name and
// input. A nil result with Miss means the caller should prompt the user.
func (s *Store) Match(toolName string, input json.RawMessage) (MatchResult, error) {
	for _, cr := range s.rules {
		ok, err := cr.matches(toolName, input)
		if err != nil {
			return MatchResult{}, err
		}
		if !ok {
			continue
		}
		return MatchResult{
			Decision: cr.Decision,
			Rule:     &cr.Rule,
			Source:   cr.Source,
			Path:     cr.Path,
			Reason:   cr.Reason,
		}, nil
	}
	return MatchResult{Decision: Miss}, nil
}

// --- Session rule CRUD (DB-backed) ---

// AddSessionRule persists a new session-scope rule. Returns the rule
// with its generated ID.
func AddSessionRule(sess SessionStore, sessID, scope string, r Rule) (Rule, error) {
	if sess == nil {
		return Rule{}, errors.New("no session store configured")
	}
	dec := sessionDecisionFromScope(scope)
	if dec == Miss {
		return Rule{}, fmt.Errorf("invalid scope %q (want allow|deny|ask)", scope)
	}
	// Compile-check the regexes up front so we don't persist a broken rule.
	if _, err := compileRegexes(r); err != nil {
		return Rule{}, err
	}
	if _, err := compileTool(r); err != nil {
		return Rule{}, err
	}
	row := SessionRuleRow{
		ID:         uuid.New().String(),
		SessionID:  sessID,
		Tool:       r.Tool,
		InputRegex: encodeInputRegex(r.InputRegex),
		Scope:      scope,
		CreatedAt:  time.Now().UTC(),
	}
	if err := sess.InsertSessionRule(row); err != nil {
		return Rule{}, err
	}
	r.ID = row.ID
	return r, nil
}

// RemoveSessionRule deletes a session-scope rule. No-op if the rule
// doesn't exist.
func RemoveSessionRule(sess SessionStore, sessID, ruleID string) error {
	if sess == nil {
		return errors.New("no session store configured")
	}
	return sess.DeleteSessionRule(sessID, ruleID)
}

// ListSessionRules returns all session-scope rules for a session.
func ListSessionRules(sess SessionStore, sessID string) ([]Rule, error) {
	if sess == nil {
		return nil, nil
	}
	rows, err := sess.ListSessionRules(sessID)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		out = append(out, Rule{
			ID:         row.ID,
			Tool:       row.Tool,
			InputRegex: decodeInputRegex(row.InputRegex),
		})
	}
	return out, nil
}

func encodeInputRegex(m map[string]string) string {
	if m == nil {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}
