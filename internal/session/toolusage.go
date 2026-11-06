package session

import (
	"log"

	dbpkg "claude-orchestrator/internal/db"
)

// number reads an int that arrived as an int (a local event) or a float64 (JSON from NATS).
func number(ev map[string]any, k string) int {
	switch v := ev[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func text(ev map[string]any, k string) string {
	s, _ := ev[k].(string)
	return s
}

// apiCallFromEvent reads the "api_call" event a runner reports for every call to the model.
func apiCallFromEvent(sessID, projectID string, ev map[string]any) dbpkg.APICall {
	first, _ := ev["first_in_turn"].(bool)
	c := dbpkg.APICall{
		SessionID: sessID, ProjectID: projectID, Parent: text(ev, "parent_tool_use_id"), Model: text(ev, "model"),
		FirstInTurn: first,
		Input:       number(ev, "input_tokens"), Output: number(ev, "output_tokens"),
		CacheCreation: number(ev, "cache_creation_input_tokens"), CacheRead: number(ev, "cache_read_input_tokens"),
		CacheCreation1h: number(ev, "cache_creation_1h_tokens"), CacheCreation5m: number(ev, "cache_creation_5m_tokens"),
	}
	add := func(m map[string]any) {
		if id := text(m, "id"); id != "" {
			c.Tools = append(c.Tools, dbpkg.ToolUse{ID: id, Name: text(m, "name")})
		}
	}
	switch tools := ev["tools"].(type) {
	case []map[string]any:
		for _, m := range tools {
			add(m)
		}
	case []any:
		for _, t := range tools {
			if m, ok := t.(map[string]any); ok {
				add(m)
			}
		}
	}
	return c
}

// saveToolEvent stores an "api_call" or "tool_result" event. It reports whether the event was
// one of them: they are for the cost of tools, so the caller does not relay them to the chat.
func (m *Manager) saveToolEvent(sessID, projectID string, ev map[string]any) bool {
	switch text(ev, "type") {
	case "api_call":
		if err := dbpkg.InsertAPICall(m.db, apiCallFromEvent(sessID, projectID, ev)); err != nil {
			log.Printf("[session] api_call of %s: %v", sessID, err)
		}
		return true
	case "tool_result":
		isErr, _ := ev["is_error"].(bool)
		if err := dbpkg.SaveToolResult(m.db, sessID, text(ev, "tool_use_id"), number(ev, "chars"), number(ev, "images"), isErr); err != nil {
			log.Printf("[session] tool_result of %s: %v", sessID, err)
		}
		return true
	}
	return false
}
