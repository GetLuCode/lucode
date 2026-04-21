package executor

import (
	"encoding/json"
	"fmt"
)

// ParsedEvent is the structured form of one claude stdout line.
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
// Returns (event, isResult, error). isResult=true means the turn is complete.
func ParseLine(sessID string, line []byte) (ParsedEvent, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ParsedEvent{}, false, fmt.Errorf("json: %w", err)
	}

	switch jsonStr(raw["type"]) {
	case "system":
		if jsonStr(raw["subtype"]) == "init" {
			return ParsedEvent{
				EventType: "session_status",
				SessionID: sessID,
				Status:    "running",
			}, false, nil
		}
		return ParsedEvent{}, false, nil

	case "assistant", "user":
		// Partial snapshots from --include-partial-messages; redundant with deltas.
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
		return ParsedEvent{
			EventType: "control_response",
			SessionID: sessID,
			Event:     line,
		}, false, nil

	default:
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
	json.Unmarshal(raw, &s)
	return s
}
