package session

import (
	"encoding/json"
	"fmt"
)

// ParsedEvent is what we broadcast over WebSocket.
type ParsedEvent struct {
	EventType     string          `json:"type"`
	SessionID     string          `json:"session_id"`
	Status        string          `json:"status,omitempty"`
	Event         json.RawMessage `json:"event,omitempty"`
	Partial       bool            `json:"partial,omitempty"`
	ResultSubtype string          `json:"result_subtype,omitempty"`
	ResultText    string          `json:"result_text,omitempty"`
	Message       string          `json:"message,omitempty"`
}

// ParseLine parses one newline-delimited JSON line from claude stdout.
// Returns (event, isResult, error). isResult=true means turn is complete.
func ParseLine(sessID string, line []byte) (ParsedEvent, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ParsedEvent{}, false, fmt.Errorf("json: %w", err)
	}

	typeVal := jsonStr(raw["type"])
	// Debug: log what we're parsing
	if typeVal == "stream_event" {
		// Don't log full content, just note that we got it
	}

	switch typeVal {
	case "system":
		// Hook events and init — only broadcast init
		if jsonStr(raw["subtype"]) == "init" {
			return ParsedEvent{
				EventType: "session_status",
				SessionID: sessID,
				Status:    "running",
			}, false, nil
		}
		return ParsedEvent{}, false, nil

	case "assistant", "user":
		// Partial/final message snapshots from --include-partial-messages.
		// These are redundant with the streaming content_block_delta events
		// we already broadcast; skip them to avoid flooding the frontend.
		return ParsedEvent{}, false, nil

	case "result":
		subtype := jsonStr(raw["subtype"])
		var resultText string
		if rt, ok := raw["result"]; ok {
			json.Unmarshal(rt, &resultText)
		}
		return ParsedEvent{
			EventType:     "turn_complete",
			SessionID:     sessID,
			ResultSubtype: subtype,
			ResultText:    resultText,
		}, true, nil

	case "control_response":
		// SDK control request response — not a turn result, handled separately.
		return ParsedEvent{
			EventType: "control_response",
			SessionID: sessID,
			Event:     line,
		}, false, nil

	default:
		// Pass everything else through: stream_event, rate_limit_event, thinking, etc.
		// The frontend handles rendering based on inner event.type
		return ParsedEvent{
			EventType: "stream_event",
			SessionID: sessID,
			Event:     line,
		}, false, nil
	}
}

func jsonStr(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func jsonBool(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	var b bool
	json.Unmarshal(raw, &b)
	return b
}
