package natsbus

// Subject conventions:
//
//   hooks.{sessId}.request              lucodehook  → server  (hook_request)
//   hooks.{sessId}.response.{toolUseId} server      → lucodehook (decision)
//   sess.{sessId}.events                server      → browser  (all session events)
//   sess.{sessId}.cmd                   browser     → server  (hook_respond, etc.)
//   hook.events.{sessId}                server      → browser  (hook_pending, hook_resolved via JetStream)
//   lucode.permissions.{sessId}         aiapi       → server  (permission request, request/reply)

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// HookState represents the state of a pending hook in the KV store.
type HookState struct {
	SessionID string          `json:"session_id"`
	ToolUseID string          `json:"tool_use_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Status    string          `json:"status"` // "pending" | "approved" | "denied"
	CreatedAt time.Time       `json:"created_at"`
}

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

	mu           sync.RWMutex
	pendingHooks map[string]map[string]*HookState // sessID -> toolUseID -> HookState

	OnBroadcast      func(data []byte) // sends event to all browser WS clients
	OnHookRequest    func(sessID, toolUseID, toolName string, toolInput json.RawMessage)
	OnHookRespond    func(sessID, toolUseID, decision, reason string, ruleJSON string)
	OnHookDisconnect func(sessID, toolUseID string)

	// OnPermissionRequest is invoked when aiapi asks for permission
	// before executing a tool. The callback MUST block until it has a
	// decision (allow/deny) and return it as the (decision, reason)
	// pair. The orchestrator's manager wires this to its store + UI
	// prompt path. Requests run concurrently. Besides allow and deny it can answer auto, which
	// leaves the call to claude's auto mode.
	OnPermissionRequest func(PermissionRequest) (decision, reason string)

	// OnTaskTool runs a task tool (task_comment / task_done / task_blocked) an
	// agent called. The returned text is shown to the agent; an error is
	// reported to it as a failed tool call.
	OnTaskTool func(sessID, tool string, args json.RawMessage) (string, error)

	// OwnsSession reports whether a session belongs to this instance. Every lucode
	// on the NATS server sees the hook, permission and task-tool subjects of every
	// session, so an instance must ignore the ones it does not own (for example a
	// session a hub runs on this instance as a runner). Nil = own all.
	OwnsSession func(sessID string) bool

	// large holds the Object Store used for messages above the NATS limit
	large largeStore

	// runnerStream: the JetStream stream with the events of turns run on runners
	runnerStreamMu sync.Mutex
	runnerStreamOK bool

	// discovery
	peersMu           sync.RWMutex
	selfInfo          InstanceInfo
	peers             map[string]peerEntry
	OnPeersChange     func([]InstanceInfo)
	queryReplyPending bool // a delayed answer to a discovery query is scheduled (guarded by peersMu)
}

// NewBus wraps an existing nats.Conn (the embedded server's internal connection).
func NewBus(nc *nats.Conn) *Bus {
	return &Bus{
		nc:           nc,
		pendingHooks: make(map[string]map[string]*HookState),
		peers:        make(map[string]peerEntry),
	}
}

// Start subscribes to inbound subjects. Call after setting On* callbacks.
func (b *Bus) Start() error {
	if _, err := b.nc.Subscribe("hooks.*.request", b.handleHookRequest); err != nil {
		return fmt.Errorf("subscribe hooks.*.request: %w", err)
	}
	if _, err := b.nc.Subscribe("sess.*.cmd", b.handleSessionCmd); err != nil {
		return fmt.Errorf("subscribe sess.*.cmd: %w", err)
	}
	// A subscription delivers its messages one at a time and this handler waits for a person,
	// so each request needs its own goroutine: otherwise one pending approval holds up every
	// session until it is answered or expires.
	if _, err := b.nc.Subscribe("lucode.permissions.>", func(msg *nats.Msg) { go b.handlePermissionRequest(msg) }); err != nil {
		return fmt.Errorf("subscribe lucode.permissions.>: %w", err)
	}
	if _, err := b.nc.Subscribe("lucode.tasktools.>", b.handleTaskTool); err != nil {
		return fmt.Errorf("subscribe lucode.tasktools.>: %w", err)
	}
	return nil
}

// PermissionSubject is the subject aiapi uses to request a permission
// decision from the orchestrator (NATS request/reply).
func PermissionSubject(sessID string) string {
	return fmt.Sprintf("lucode.permissions.%s", sessID)
}

// Broadcast sends a session event to all browser WS clients.
func (b *Bus) Broadcast(sessID string, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if b.OnBroadcast != nil {
		b.OnBroadcast(data)
	}
}

// BroadcastToRole is kept for interface parity with ws.Hub.
func (b *Bus) BroadcastToRole(sessID string, _ Role, event any) {
	b.Broadcast(sessID, event)
}

// SendHookResponse sends a decision payload to the waiting lucodehook instance.
func (b *Bus) SendHookResponse(sessID, toolUseID string, event any) bool {
	b.publish(HookResponseSubject(sessID, toolUseID), event)
	return true // NATS fire-and-forget; assume delivered
}

// SetHookState persists hook state to the KV store.
// SetHookState stores a hook state in memory.
func (b *Bus) SetHookState(sessID, toolUseID string, state HookState) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pendingHooks[sessID] == nil {
		b.pendingHooks[sessID] = make(map[string]*HookState)
	}
	b.pendingHooks[sessID][toolUseID] = &state
	return nil
}

// DeleteHookState removes a hook state from memory.
func (b *Bus) DeleteHookState(sessID, toolUseID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pendingHooks[sessID] != nil {
		delete(b.pendingHooks[sessID], toolUseID)
	}
	return nil
}

// GetSessionHooks returns all pending hooks for a session.
func (b *Bus) GetSessionHooks(sessID string) map[string]*HookState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	hooks := make(map[string]*HookState)
	if b.pendingHooks[sessID] != nil {
		for k, v := range b.pendingHooks[sessID] {
			hooks[k] = v
		}
	}
	return hooks
}

// GetAllPendingHooks returns all pending hooks across all sessions.
func (b *Bus) GetAllPendingHooks() []*HookState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var all []*HookState
	for _, hooks := range b.pendingHooks {
		for _, h := range hooks {
			all = append(all, h)
		}
	}
	return all
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

func (b *Bus) owns(sessID string) bool {
	return b.OwnsSession == nil || b.OwnsSession(sessID)
}

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
	if b.OnHookRequest == nil || !b.owns(req.SessionID) {
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
		Rule      string `json:"rule,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		log.Printf("[natsbus] session cmd parse error: %v", err)
		return
	}
	switch cmd.Type {
	case "hook_respond":
		if b.OnHookRespond != nil && b.owns(cmd.SessionID) {
			log.Printf("[natsbus] hook_respond sess=%s tool=%s decision=%s", cmd.SessionID, cmd.ToolUseID, cmd.Decision)
			b.OnHookRespond(cmd.SessionID, cmd.ToolUseID, cmd.Decision, cmd.Reason, cmd.Rule)
		}
	default:
		log.Printf("[natsbus] unknown cmd type=%s", cmd.Type)
	}
}

// PermissionRequest is a tool call the hook asks about, as the claude on the runner reports it.
type PermissionRequest struct {
	SessionID      string          `json:"-"` // from the subject
	ToolUseID      string          `json:"tool_use_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	WorkDir        string          `json:"work_dir"`
	PermissionMode string          `json:"permission_mode"`
}

// handlePermissionRequest is the aiapi→orchestrator permission gate.
// It runs the OnPermissionRequest callback (which blocks until the
// orchestrator's manager has a decision) and replies on the request's
// inbox subject. Fail-closed: any error path replies with deny.
func (b *Bus) handlePermissionRequest(msg *nats.Msg) {
	var req PermissionRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		log.Printf("[natsbus] permission request parse error: %v", err)
		b.replyDeny(msg, "malformed permission request")
		return
	}
	sessID := sessionIDFromPermissionSubject(msg.Subject)
	if sessID == "" {
		log.Printf("[natsbus] permission request bad subject: %s", msg.Subject)
		b.replyDeny(msg, "bad subject")
		return
	}
	if !b.owns(sessID) {
		return // another instance owns this session and answers it
	}
	if b.OnPermissionRequest == nil {
		b.replyDeny(msg, "permission gate not configured")
		return
	}
	req.SessionID = sessID
	decision, reason := b.OnPermissionRequest(req)
	// auto only means something to a claude in auto mode: anywhere else it would be silence,
	// and the hook of an older runner would read it as a deny anyway.
	if decision != "allow" && decision != "deny" && !(decision == "auto" && req.PermissionMode == "auto") {
		// fail-closed: unknown decision → deny
		decision = "deny"
		if reason == "" {
			reason = "permission gate returned no decision"
		}
	}
	reply := map[string]any{
		"tool_use_id": req.ToolUseID,
		"decision":    decision,
		"reason":      reason,
	}
	data, _ := json.Marshal(reply)
	if err := b.nc.Publish(msg.Reply, data); err != nil {
		log.Printf("[natsbus] permission reply publish: %v", err)
	}
}

// handleTaskTool serves lucode.tasktools.<sessID> request/reply: the agent's
// task tools (via the MCP server or aiapi) land here.
func (b *Bus) handleTaskTool(msg *nats.Msg) {
	reply := func(ok bool, text string) {
		if msg.Reply == "" {
			return
		}
		data, _ := json.Marshal(map[string]any{"ok": ok, "message": text})
		if err := b.nc.Publish(msg.Reply, data); err != nil {
			log.Printf("[natsbus] task tool reply: %v", err)
		}
	}
	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		reply(false, "malformed task tool request")
		return
	}
	const prefix = "lucode.tasktools."
	if len(msg.Subject) <= len(prefix) {
		reply(false, "bad subject")
		return
	}
	if !b.owns(msg.Subject[len(prefix):]) {
		return // another instance owns this session and answers it
	}
	if b.OnTaskTool == nil {
		reply(false, "task tools not configured")
		return
	}
	text, err := b.OnTaskTool(msg.Subject[len(prefix):], req.Tool, req.Args)
	if err != nil {
		reply(false, err.Error())
		return
	}
	reply(true, text)
}

func (b *Bus) replyDeny(req *nats.Msg, reason string) {
	if req.Reply == "" {
		return
	}
	data, _ := json.Marshal(map[string]any{"decision": "deny", "reason": reason})
	if err := b.nc.Publish(req.Reply, data); err != nil {
		log.Printf("[natsbus] permission deny reply: %v", err)
	}
}

// sessionIDFromPermissionSubject extracts the session id from a
// "lucode.permissions.<sessID>" subject.
func sessionIDFromPermissionSubject(subj string) string {
	const prefix = "lucode.permissions."
	if len(subj) <= len(prefix) {
		return ""
	}
	return subj[len(prefix):]
}

// Role is kept for BroadcastToRole interface parity.
type Role string

const (
	RoleClient Role = "client"
	RoleHook   Role = "hook"
)
