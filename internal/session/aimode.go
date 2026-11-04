package session

import (
	"context"
	"log"

	"claude-orchestrator/internal/aimode"
)

// runWithAIMode answers a turn through Google's AI Mode in Edge (a local "model"): the
// message is typed into the session's own conversation on the page and the answer is
// read back. It has no tools and no streaming (the answer arrives whole); a picture sent with the
// message is pasted into the page's box.
func (m *Manager) runWithAIMode(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)
	publish := m.cliPublisher(sessID, opts)

	// what the page is doing, for the timer in the chat; not stored, it is only live feedback
	progress := func(text string) {
		m.bus.Broadcast(sessID, map[string]any{"type": "progress", "session_id": sessID, "text": text})
	}
	var img *aimode.Image
	if opts.ImageBase64 != "" {
		img = &aimode.Image{Base64: opts.ImageBase64, MediaType: opts.ImageMediaType}
	}
	answer, err := aimode.Default.AskImage(aimode.WithProgress(ctx, progress), sessID, opts.Message, img)
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
