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
	"strings"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	wspkg "claude-orchestrator/internal/ws"

	"github.com/google/uuid"
)

var ErrSessionBusy = errors.New("session already has an active turn")

type Manager struct {
	db             *sql.DB
	hub            *wspkg.Hub
	backendWSURL   string
	claudeSettings string // path to --settings file for claude CLI (empty = not used)

	mu    sync.Mutex
	procs map[string]*ProcessState
}

func NewManager(db *sql.DB, hub *wspkg.Hub, backendWSURL, claudeSettings string) *Manager {
	m := &Manager{
		db:             db,
		hub:            hub,
		backendWSURL:   backendWSURL,
		claudeSettings: claudeSettings,
		procs:          make(map[string]*ProcessState),
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

	// Persist user message (with image if present)
	seq, _ := dbpkg.NextMessageSeq(m.db, opts.SessionID)
	content := opts.Message
	if opts.ImageBase64 != "" {
		// Store as JSON content blocks
		contentBlocks := []map[string]interface{}{
			{"type": "text", "text": opts.Message},
			{"type": "image", "source": map[string]interface{}{
				"type":       "base64",
				"media_type": opts.ImageMediaType,
				"data":       opts.ImageBase64,
			}},
		}
		contentJSON, _ := json.Marshal(contentBlocks)
		content = string(contentJSON)
	}
	_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
		ID:        uuid.New().String(),
		SessionID: opts.SessionID,
		Role:      "user",
		Content:   content,
		Seq:       seq,
	})

	m.hub.Broadcast(opts.SessionID, map[string]any{
		"type":       "session_status",
		"session_id": opts.SessionID,
		"status":     string(StatusRunning),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)

	opts.ClaudeSettings = m.claudeSettings
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
	accum := newTurnAccum()

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

		// Accumulate streaming content and persist only finalized messages
		// (thinking on content_block_stop, assistant text on message_stop).
		if event.EventType == "stream_event" && event.Event != nil {
			for _, msg := range accum.process(event.Event, ps.SessionID) {
				msg.Seq = seq
				_ = dbpkg.InsertMessage(m.db, msg)
				seq++
			}
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
	createdAt := time.Now().UTC()

	_ = dbpkg.InsertPendingHook(m.db, dbpkg.PendingHook{
		SessionID: sessID,
		ToolUseID: toolUseID,
		ToolName:  toolName,
		ToolInput: inputStr,
		CreatedAt: createdAt,
	})
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusWaitingHook))

	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":        "hook_pending",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  toolInput,
		"created_at":  createdAt.Format(time.RFC3339Nano),
	})
}

// onHookRespond is called by the hub when the browser sends hook_respond.
func (m *Manager) onHookRespond(sessID, toolUseID, decision, reason string) {
	// Retrieve hook info before deleting
	hook, err := dbpkg.GetPendingHook(m.db, sessID, toolUseID)
	if err == nil {
		// Insert hook decision message in DB
		content, _ := json.Marshal(map[string]any{
			"tool_use_id": toolUseID,
			"tool_name":   hook.ToolName,
			"tool_input":  json.RawMessage(hook.ToolInput),
			"decision":    decision,
			"reason":      reason,
		})
		seq, _ := dbpkg.NextMessageSeq(m.db, sessID)
		dbpkg.InsertMessage(m.db, dbpkg.Message{
			ID:        uuid.New().String(),
			SessionID: sessID,
			Role:      "hook",
			Content:   string(content),
			Seq:       seq,
		})
	}

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
		// Hook binary already disconnected (timed out and auto-denied before user responded).
		// Inform the user so they know their response arrived too late.
		m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
			"type":       "info",
			"session_id": sessID,
			"message":    "Hook approval timed out — tool was automatically denied.",
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
	// Retrieve hook info before deleting
	hook, _ := dbpkg.GetPendingHook(m.db, sessID, toolUseID)

	deleted, _ := dbpkg.DeletePendingHook(m.db, sessID, toolUseID)
	if deleted == 0 {
		// Already resolved by user — nothing to clean up
		return
	}
	log.Printf("[hook] session=%s tool_use_id=%s disconnected while pending — auto-resolving", sessID[:8], toolUseID)

	// Insert hook timeout message in DB
	if hook.SessionID != "" { // Ensure we got a valid hook record
		content, _ := json.Marshal(map[string]any{
			"tool_name":  hook.ToolName,
			"tool_input": json.RawMessage(hook.ToolInput),
			"decision":   "timeout",
			"reason":     "",
		})
		seq, _ := dbpkg.NextMessageSeq(m.db, sessID)
		dbpkg.InsertMessage(m.db, dbpkg.Message{
			ID:        uuid.New().String(),
			SessionID: sessID,
			Role:      "hook",
			Content:   string(content),
			Seq:       seq,
		})
	}

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
		"message":    "Hook approval timed out — tool was automatically denied.",
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

// turnAccum accumulates Claude streaming events during a turn and emits
// finalized DB messages only at natural boundaries (message_stop, thinking block stop).
// This mirrors the frontend's handleWSEvent logic so stored messages render correctly.
type turnAccum struct {
	text       strings.Builder
	thinking   strings.Builder
	blockTypes map[int]string // block index → "text" | "thinking"
}

func newTurnAccum() *turnAccum {
	return &turnAccum{blockTypes: make(map[int]string)}
}

// process handles one raw stream_event line and returns any messages ready to persist.
func (a *turnAccum) process(raw json.RawMessage, sessionID string) []dbpkg.Message {
	// The raw line has shape: {"type":"stream_event","session_id":"...","event":{...}}
	var outer struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil || outer.Event == nil {
		return nil
	}

	var inner map[string]json.RawMessage
	if err := json.Unmarshal(outer.Event, &inner); err != nil {
		return nil
	}

	switch jsonStr(inner["type"]) {
	case "content_block_start":
		var idx int
		json.Unmarshal(inner["index"], &idx)
		var block struct {
			Type string `json:"type"`
		}
		if cb, ok := inner["content_block"]; ok {
			json.Unmarshal(cb, &block)
		}
		a.blockTypes[idx] = block.Type

	case "content_block_delta":
		var delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if d, ok := inner["delta"]; ok {
			json.Unmarshal(d, &delta)
		}
		switch delta.Type {
		case "text_delta":
			a.text.WriteString(delta.Text)
		case "thinking_delta":
			a.thinking.WriteString(delta.Thinking)
		}

	case "content_block_stop":
		var idx int
		json.Unmarshal(inner["index"], &idx)
		blockType := a.blockTypes[idx]
		delete(a.blockTypes, idx)

		if blockType == "thinking" {
			text := a.thinking.String()
			a.thinking.Reset()
			if text != "" {
				return []dbpkg.Message{{
					ID:        uuid.New().String(),
					SessionID: sessionID,
					Role:      "thinking",
					Content:   text,
				}}
			}
		}

	case "message_stop":
		text := a.text.String()
		a.text.Reset()
		a.blockTypes = make(map[int]string)
		if text != "" {
			return []dbpkg.Message{{
				ID:        uuid.New().String(),
				SessionID: sessionID,
				Role:      "assistant",
				Content:   text,
			}}
		}
	}

	return nil
}
