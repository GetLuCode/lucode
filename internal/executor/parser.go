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
	IsError       bool            `json:"is_error,omitempty"`        // result: the turn ended in an API error
	APIStatus     int             `json:"api_error_status,omitempty"` // result: HTTP status of that error (429 = limit)
	APIError      string          `json:"api_error,omitempty"`        // result: e.g. "usage_limit_reached"
	ResetsAt      int64           `json:"resets_at,omitempty"`        // rate_limit: unix seconds when the limit lifts
	LimitRejected bool            `json:"limit_rejected,omitempty"`   // rate_limit: the request was rejected
	Cwd           string          `json:"cwd,omitempty"`             // init: directory claude really works in (the worktree, with --worktree)
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
				Cwd:       jsonStr(raw["cwd"]),
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
		var isErr bool
		var status int
		json.Unmarshal(raw["is_error"], &isErr)
		json.Unmarshal(raw["api_error_status"], &status)
		return ParsedEvent{
			EventType:     "turn_complete",
			SessionID:     sessID,
			ResultSubtype: subtype,
			ResultText:    resultText,
			IsError:       isErr,
			APIStatus:     status,
			APIError:      jsonStr(raw["api_error"]),
		}, true, nil

	case "rate_limit_event":
		var info struct {
			Status   string `json:"status"`
			ResetsAt int64  `json:"resetsAt"`
		}
		json.Unmarshal(raw["rate_limit_info"], &info)
		return ParsedEvent{
			EventType:     "rate_limit",
			SessionID:     sessID,
			ResetsAt:      info.ResetsAt,
			LimitRejected: info.Status == "rejected",
		}, false, nil

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
