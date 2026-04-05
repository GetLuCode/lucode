package session

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	wspkg "claude-orchestrator/internal/ws"

	"github.com/google/uuid"
)

var ErrSessionBusy = errors.New("session already has an active turn")

type Manager struct {
	db           *sql.DB
	hub          *wspkg.Hub
	backendWSURL string

	mu    sync.Mutex
	procs map[string]*ProcessState
}

func NewManager(db *sql.DB, hub *wspkg.Hub, backendWSURL string) *Manager {
	m := &Manager{
		db:           db,
		hub:          hub,
		backendWSURL: backendWSURL,
		procs:        make(map[string]*ProcessState),
	}
	hub.OnHookRequest = func(sessID, toolUseID, toolName string, toolInput json.RawMessage) {
		m.onHookRequest(sessID, toolUseID, toolName, toolInput)
	}
	hub.OnHookRespond = func(sessID, toolUseID, decision, reason string) {
		m.onHookRespond(sessID, toolUseID, decision, reason)
	}
	hub.OnHookDisconnect = func(sessID, toolUseID string) {
		m.onHookDisconnect(sessID, toolUseID)
	}
	return m
}

// StartTurn launches a claude CLI process for the given turn options.
// Returns immediately; process runs in a goroutine.
func (m *Manager) StartTurn(opts TurnOptions) error {
	m.mu.Lock()
	if _, active := m.procs[opts.SessionID]; active {
		m.mu.Unlock()
		return ErrSessionBusy
	}
	m.mu.Unlock()

	dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusRunning))

	// Persist user message
	seq, _ := dbpkg.NextMessageSeq(m.db, opts.SessionID)
	_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
		ID:        uuid.New().String(),
		SessionID: opts.SessionID,
		Role:      "user",
		Content:   opts.Message,
		Seq:       seq,
	})

	m.hub.Broadcast(opts.SessionID, map[string]any{
		"type":       "session_status",
		"session_id": opts.SessionID,
		"status":     string(StatusRunning),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)

	sr, err := spawnClaude(ctx, opts, m.backendWSURL)
	if err != nil {
		cancel()
		dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusError))
		m.hub.Broadcast(opts.SessionID, map[string]any{
			"type":       "error",
			"session_id": opts.SessionID,
			"message":    err.Error(),
		})
		return err
	}
	sr.ps.Cancel = cancel

	m.mu.Lock()
	m.procs[opts.SessionID] = sr.ps
	m.mu.Unlock()

	go m.streamOutput(sr)
	return nil
}

func (m *Manager) streamOutput(sr *spawnResult) {
	ps := sr.ps
	defer func() {
		ps.Cancel()
		close(ps.Done)
		ps.Cmd.Wait()
		m.mu.Lock()
		delete(m.procs, ps.SessionID)
		m.mu.Unlock()
	}()

	sid8 := ps.SessionID[:8]
	log.Printf("[stream] session=%s reading stdout", sid8)

	scanner := bufio.NewScanner(sr.stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	seq, _ := dbpkg.NextMessageSeq(m.db, ps.SessionID)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		log.Printf("[stdout] session=%s: %s", sid8, string(line))

		event, isResult, err := ParseLine(ps.SessionID, line)
		if err != nil {
			log.Printf("[stream] session=%s parse error: %v", sid8, err)
			continue
		}
		if event.EventType == "" {
			continue
		}

		log.Printf("[event] session=%s type=%s partial=%v isResult=%v", sid8, event.EventType, event.Partial, isResult)

		// Persist final (non-partial) stream events
		if event.EventType == "stream_event" && !event.Partial && event.Event != nil {
			role := guessRole(event.Event)
			_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
				ID:        uuid.New().String(),
				SessionID: ps.SessionID,
				Role:      role,
				Content:   string(event.Event),
				Seq:       seq,
			})
			seq++
		}

		m.hub.Broadcast(ps.SessionID, event)

		if isResult {
			log.Printf("[stream] session=%s turn complete: subtype=%s", sid8, event.ResultSubtype)
			dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
			m.hub.Broadcast(ps.SessionID, map[string]any{
				"type":       "session_status",
				"session_id": ps.SessionID,
				"status":     string(StatusIdle),
			})
			return
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[stream] session=%s scanner error: %v", sid8, err)
	}

	// Process ended without a result event
	exitCode := 0
	if ps.Cmd.ProcessState != nil {
		exitCode = ps.Cmd.ProcessState.ExitCode()
	}
	log.Printf("[stream] session=%s process exited code=%d", sid8, exitCode)

	if exitCode != 0 {
		msg := fmt.Sprintf("claude exited with code %s", strconv.Itoa(exitCode))
		m.hub.Broadcast(ps.SessionID, map[string]any{
			"type":       "error",
			"session_id": ps.SessionID,
			"message":    msg,
		})
		dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusError))
	} else {
		dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
	}
}

// onHookRequest is called by the hub when a hook binary sends hook_request.
func (m *Manager) onHookRequest(sessID, toolUseID, toolName string, toolInput json.RawMessage) {
	inputStr := string(toolInput)

	_ = dbpkg.InsertPendingHook(m.db, dbpkg.PendingHook{
		SessionID: sessID,
		ToolUseID: toolUseID,
		ToolName:  toolName,
		ToolInput: inputStr,
	})
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusWaitingHook))

	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":        "hook_pending",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  toolInput,
	})
}

// onHookRespond is called by the hub when the browser sends hook_respond.
func (m *Manager) onHookRespond(sessID, toolUseID, decision, reason string) {
	dbpkg.DeletePendingHook(m.db, sessID, toolUseID)

	// If no more pending hooks, mark session as running; otherwise stay waiting_hook
	remaining, _ := dbpkg.CountPendingHooks(m.db, sessID)
	if remaining == 0 {
		dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	}

	// Forward hook_response to the specific hook binary WS client by tool_use_id
	sent := m.hub.SendToHookClient(sessID, toolUseID, map[string]any{
		"type":        "hook_response",
		"tool_use_id": toolUseID,
		"decision":    decision,
		"reason":      reason,
	})

	if !sent {
		// Hook binary already disconnected (timed out and auto-allowed before user responded).
		// Inform the user so they know their approval arrived too late.
		m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
			"type":       "info",
			"session_id": sessID,
			"message":    "Hook approval timed out — tool was automatically allowed before your response.",
		})
	}

	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":        "hook_resolved",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"decision":    decision,
	})
}

// onHookDisconnect is called when a hook binary WS client disconnects unexpectedly.
// If the hook was still pending (user hadn't responded), we auto-resolve it so the
// frontend modal closes. The hook binary already exited (allow or timeout).
func (m *Manager) onHookDisconnect(sessID, toolUseID string) {
	deleted, _ := dbpkg.DeletePendingHook(m.db, sessID, toolUseID)
	if deleted == 0 {
		// Already resolved by user — nothing to clean up
		return
	}
	log.Printf("[hook] session=%s tool_use_id=%s disconnected while pending — auto-resolving", sessID[:8], toolUseID)

	remaining, _ := dbpkg.CountPendingHooks(m.db, sessID)
	if remaining == 0 {
		dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	}

	// Close the modal and show an informational message
	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":        "hook_resolved",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"decision":    "timeout",
	})
	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":       "info",
		"session_id": sessID,
		"message":    "Hook approval timed out — tool was automatically allowed.",
	})
}

func (m *Manager) GetStatus(sessID string) Status {
	m.mu.Lock()
	_, active := m.procs[sessID]
	m.mu.Unlock()
	if active {
		return StatusRunning
	}
	sess, err := dbpkg.GetSession(m.db, sessID)
	if err != nil {
		return StatusError
	}
	return Status(sess.Status)
}

// guessRole returns "assistant" for most events; "tool_result" for user-side tool responses.
func guessRole(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "assistant"
	}
	t := jsonStr(m["type"])
	if t == "user" {
		return "tool_result"
	}
	return "assistant"
}
