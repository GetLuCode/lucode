package permissions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// compiledRule is a Rule with all its regexes pre-compiled. Compilation
// happens once at Store construction; an invalid regex surfaces as an
// error so we never silently skip a rule.
type compiledRule struct {
	Rule
	Decision Decision
	Source   RuleSource
	Path     string // file path or session id, for diagnostics
	regex    map[string]*regexp.Regexp
}

// ruleMatches returns true if the rule's regexes all match the given
// input. A rule with no InputRegex matches any input of the named tool.
func (c *compiledRule) matches(toolName string, input json.RawMessage) (bool, error) {
	if c.Tool != toolName && c.Tool != "*" {
		return false, nil
	}
	if c.regex == nil && len(c.InputRegex) == 0 {
		return true, nil
	}
	// Extract string fields from input.
	fields, err := extractStringFields(input)
	if err != nil {
		return false, err
	}
	// Every regex must match its corresponding field (or any string
	// field if the rule is field-agnostic — we don't support that
	// today; the rule key must match an input field).
	for field, re := range c.regex {
		val, ok := fields[field]
		if !ok {
			return false, nil
		}
		if !re.MatchString(val) {
			return false, nil
		}
	}
	return true, nil
}

// extractStringFields walks a JSON value and returns all leaf string
// fields keyed by their path (dot-separated). Arrays index into the
// path. We use a flat namespace so a rule like {"path": "^/foo"} can
// target the "path" field regardless of nesting.
func extractStringFields(raw json.RawMessage) (map[string]string, error) {
	out := make(map[string]string)
	if len(raw) == 0 {
		return out, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	walk("", v, out)
	return out, nil
}

func walk(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			walk(key, val, out)
		}
	case []any:
		for i, val := range x {
			walk(fmt.Sprintf("%s[%d]", prefix, i), val, out)
		}
	case string:
		out[prefix] = x
	}
}

// compileRegexes builds the regex map for a rule. Returns an error if
// any pattern fails to compile.
func compileRegexes(r Rule) (map[string]*regexp.Regexp, error) {
	if len(r.InputRegex) == 0 {
		return nil, nil
	}
	out := make(map[string]*regexp.Regexp, len(r.InputRegex))
	for field, pattern := range r.InputRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q field %q: invalid regex %q: %w", r.Tool, field, pattern, err)
		}
		out[field] = re
	}
	return out, nil
}

// sortedFields returns a stable string representation of the rule's
// input field map (for use as a deduplication key).
func sortedFields(m map[string]string) string {
	if len(m) == 0 {
		return "*"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(m[k])
	}
	return sb.String()
}
