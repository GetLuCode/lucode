package session

import (
	"context"
	"log"

	"claude-orchestrator/internal/aimode"
)

// runWithAIMode answers a turn through Google's AI Mode in Edge (a local "model"): the
// message is typed into the session's own conversation on the page and the answer is
// read back. It has no tools, no streaming and no images; the answer arrives whole.
func (m *Manager) runWithAIMode(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)
	publish := m.cliPublisher(sessID, opts)

	answer, err := aimode.Default.Ask(ctx, sessID, opts.Message)
	if err != nil {
		if ctx.Err() != nil { // the user stopped the turn
			publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
			return
		}
		log.Printf("[aimode] session=%s: %v", sessID[:8], err)
		publish(map[string]any{"type": "error", "session_id": sessID, "message": "Google AI Mode: " + err.Error()})
		publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
		return
	}
	publish(map[string]any{"type": "message_ready", "session_id": sessID, "role": "assistant", "content": answer})
	publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
}
