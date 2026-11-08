package session

import (
	"context"
	"errors"
	"log"
)

// PluginTurnRequest is a chat message for a plugin's model.
type PluginTurnRequest struct {
	SessionID      string
	ProjectID      string
	Message        string
	Model          string
	ImageBase64    string
	ImageMediaType string
}

// runWithPlugin answers a turn with a plugin's code on the hub, through the callback the API
// gives the manager, since this package does not know about plugins. Results are published like
// any local model's: the answer whole, then the status.
func (m *Manager) runWithPlugin(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)
	publish := m.cliPublisher(sessID, opts)

	// what the plugin is doing, for the timer in the chat; not stored, it is only live feedback
	progress := func(text string) {
		m.bus.Broadcast(sessID, map[string]any{"type": "progress", "session_id": sessID, "text": text})
	}
	answer, err := m.callPlugin(ctx, PluginTurnRequest{
		SessionID: sessID, ProjectID: opts.ProjectID, Message: opts.Message, Model: opts.Model,
		ImageBase64: opts.ImageBase64, ImageMediaType: opts.ImageMediaType,
	}, progress)
	if err != nil {
		if ctx.Err() != nil { // the user stopped the turn
			publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
			return
		}
		log.Printf("[plugins] session=%.8s: %v", sessID, err)
		publish(map[string]any{"type": "error", "session_id": sessID, "message": err.Error()})
		publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
		return
	}
	publish(map[string]any{"type": "message_ready", "session_id": sessID, "role": "assistant", "content": answer})
	publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
}

// callPlugin keeps a failure in the callback from taking the session down with it.
func (m *Manager) callPlugin(ctx context.Context, req PluginTurnRequest, progress func(string)) (answer string, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[plugins] turn panic: %v", p)
			err = errors.New("the plugin host hit an internal error")
		}
	}()
	if m.PluginTurn == nil {
		return "", errors.New("plugins are not running on this instance")
	}
	return m.PluginTurn(ctx, req, progress)
}
