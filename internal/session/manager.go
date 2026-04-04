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
	hub.OnHookRequest = m.onHookRequest
	hub.OnHookRespond = m.onHookRespond
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
func (m *Manager) onHookRequest(sessID string, toolName string, toolInput json.RawMessage) {
	inputStr := string(toolInput)

	_ = dbpkg.UpsertPendingHook(m.db, dbpkg.PendingHook{
		SessionID: sessID,
		ToolName:  toolName,
		ToolInput: inputStr,
	})
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusWaitingHook))

	m.hub.BroadcastToRole(sessID, wspkg.RoleClient, map[string]any{
		"type":       "hook_pending",
		"session_id": sessID,
		"tool_name":  toolName,
		"tool_input": toolInput,
	})
}

// onHookRespond is called by the hub when the browser sends hook_respond.
func (m *Manager) onHookRespond(sessID string, decision string, reason string) {
	dbpkg.DeletePendingHook(m.db, sessID)
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))

	// Forward hook_response to the hook binary WS client
	m.hub.BroadcastToRole(sessID, wspkg.RoleHook, map[string]any{
		"type":     "hook_response",
		"decision": decision,
		"reason":   reason,
	})

	m.hub.Broadcast(sessID, map[string]any{
		"type":       "hook_resolved",
		"session_id": sessID,
		"decision":   decision,
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
