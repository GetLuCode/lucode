package natsbus

// Subject conventions:
//
//   hooks.{sessId}.request              lucodehook  → server  (hook_request)
//   hooks.{sessId}.response.{toolUseId} server      → lucodehook (decision)
//   sess.{sessId}.events                server      → browser  (all session events)
//   sess.{sessId}.cmd                   browser     → server  (hook_respond, etc.)

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
)

func HookRequestSubject(sessID string) string {
	return fmt.Sprintf("hooks.%s.request", sessID)
}

func HookResponseSubject(sessID, toolUseID string) string {
	return fmt.Sprintf("hooks.%s.response.%s", sessID, toolUseID)
}

func SessionEventsSubject(sessID string) string {
	return fmt.Sprintf("sess.%s.events", sessID)
}

func SessionCmdSubject(sessID string) string {
	return fmt.Sprintf("sess.%s.cmd", sessID)
}

// Bus is the internal pub/sub adapter. It drives the same callbacks that
// the old ws.Hub used so session.Manager needs no changes to its logic.
type Bus struct {
	nc *nats.Conn

	OnHookRequest    func(sessID, toolUseID, toolName string, toolInput json.RawMessage)
	OnHookRespond    func(sessID, toolUseID, decision, reason string)
	OnHookDisconnect func(sessID, toolUseID string) // kept for interface parity; unused with NATS
}

// NewBus wraps an existing nats.Conn (the embedded server's internal connection).
func NewBus(nc *nats.Conn) *Bus {
	return &Bus{nc: nc}
}

// Start subscribes to inbound subjects. Call after setting On* callbacks.
func (b *Bus) Start() error {
	if _, err := b.nc.Subscribe("hooks.*.request", b.handleHookRequest); err != nil {
		return fmt.Errorf("subscribe hooks.*.request: %w", err)
	}
	if _, err := b.nc.Subscribe("sess.*.cmd", b.handleSessionCmd); err != nil {
		return fmt.Errorf("subscribe sess.*.cmd: %w", err)
	}
	return nil
}

// Broadcast publishes a session event to all browser subscribers.
func (b *Bus) Broadcast(sessID string, event any) {
	b.publish(SessionEventsSubject(sessID), event)
}

// BroadcastToRole is kept for interface parity with ws.Hub.
// With NATS, both roles share the same subject; the browser filters by type.
func (b *Bus) BroadcastToRole(sessID string, _ Role, event any) {
	b.Broadcast(sessID, event)
}

// SendHookResponse sends a decision payload to the waiting lucodehook instance.
func (b *Bus) SendHookResponse(sessID, toolUseID string, event any) bool {
	b.publish(HookResponseSubject(sessID, toolUseID), event)
	return true // NATS fire-and-forget; assume delivered
}

func (b *Bus) publish(subj string, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := b.nc.Publish(subj, data); err != nil {
		log.Printf("[natsbus] publish %s: %v", subj, err)
	}
}

// --- inbound handlers ---

func (b *Bus) handleHookRequest(msg *nats.Msg) {
	var req struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		ToolUseID string          `json:"tool_use_id"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		log.Printf("[natsbus] hook request parse error: %v", err)
		return
	}
	if b.OnHookRequest == nil {
		return
	}
	log.Printf("[natsbus] hook_request sess=%s tool=%s id=%s", req.SessionID, req.ToolName, req.ToolUseID)
	b.OnHookRequest(req.SessionID, req.ToolUseID, req.ToolName, req.ToolInput)
}

func (b *Bus) handleSessionCmd(msg *nats.Msg) {
	var cmd struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		ToolUseID string `json:"tool_use_id"`
		Decision  string `json:"decision"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		log.Printf("[natsbus] session cmd parse error: %v", err)
		return
	}
	switch cmd.Type {
	case "hook_respond":
		if b.OnHookRespond != nil {
			log.Printf("[natsbus] hook_respond sess=%s tool=%s decision=%s", cmd.SessionID, cmd.ToolUseID, cmd.Decision)
			b.OnHookRespond(cmd.SessionID, cmd.ToolUseID, cmd.Decision, cmd.Reason)
		}
	default:
		log.Printf("[natsbus] unknown cmd type=%s", cmd.Type)
	}
}

// Role is kept for BroadcastToRole interface parity.
type Role string

const (
	RoleClient Role = "client"
	RoleHook   Role = "hook"
)
