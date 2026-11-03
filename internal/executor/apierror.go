package executor

import (
	"fmt"
	"time"
)

// A claude result with is_error means the turn did not run (usage limit, API
// failure...) even though the process exits with code 0. These helpers turn it
// into an error event the UI shows and the task workflow reacts to.

// turnAPIError reports whether a result event is a failed turn.
func turnAPIError(e ParsedEvent) bool {
	return e.IsError || e.APIStatus >= 400
}

// publishTurnAPIError tells the user why the turn failed. For a usage limit it
// includes when it lifts. It returns the error code ("usage_limit" or "api_error").
func publishTurnAPIError(sessID string, e ParsedEvent, resetsAt int64, publish func(map[string]any)) string {
	ev := map[string]any{"type": "error", "session_id": sessID}
	code := "api_error"
	msg := e.ResultText
	if msg == "" {
		msg = "claude could not complete the turn"
		if e.APIStatus != 0 {
			msg += fmt.Sprintf(" (API error %d)", e.APIStatus)
		}
	}
	if e.APIError == "usage_limit_reached" || (e.APIStatus == 429 && resetsAt != 0) {
		code = "usage_limit"
		if resetsAt != 0 {
			ev["resets_at"] = resetsAt
			msg = "Claude usage limit reached. Available again at " + time.Unix(resetsAt, 0).Local().Format("15:04 (Mon 2 Jan)") + "."
		} else {
			msg = "Claude usage limit reached. " + e.ResultText
		}
	}
	ev["code"] = code
	ev["message"] = msg
	publish(ev)
	publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
	return code
}
