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
	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/google/uuid"
)

var ErrSessionBusy = errors.New("session already has an active turn")

type Manager struct {
	db             *sql.DB
	bus            *natspkg.Bus
	backendNATSURL string
	claudeSettings string

	mu            sync.Mutex
	procs         map[string]*ProcessState
	ollamaPending map[string]chan string // tool_use_id → decision channel
}

func NewManager(db *sql.DB, bus *natspkg.Bus, backendNATSURL, claudeSettings string) *Manager {
	m := &Manager{
		db:             db,
		bus:            bus,
		backendNATSURL: backendNATSURL,
		claudeSettings: claudeSettings,
		procs:          make(map[string]*ProcessState),
		ollamaPending:  make(map[string]chan string),
	}
	bus.OnHookRequest = func(sessID, toolUseID, toolName string, toolInput json.RawMessage) {
		m.onHookRequest(sessID, toolUseID, toolName, toolInput)
	}
	bus.OnHookRespond = func(sessID, toolUseID, decision, reason string) {
		m.onHookRespond(sessID, toolUseID, decision, reason)
	}
	bus.OnHookDisconnect = func(sessID, toolUseID string) {
		// Not triggered via NATS (stateless hook binary), kept for parity.
	}
	return m
}

// StartTurn launches an AI turn. Returns immediately; turn runs in a goroutine.
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
	content := opts.Message
	if opts.ImageBase64 != "" {
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
	userMsgID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
		ID:        userMsgID,
		SessionID: opts.SessionID,
		Role:      "user",
		Content:   content,
		Seq:       seq,
	})

	// Broadcast user message so clients can display it without polling /messages
	m.bus.Broadcast(opts.SessionID, map[string]any{
		"type":       "user_message",
		"session_id": opts.SessionID,
		"id":         userMsgID,
		"content":    content,
		"seq":        seq,
		"created_at": now,
	})

	m.bus.Broadcast(opts.SessionID, map[string]any{
		"type":       "session_status",
		"session_id": opts.SessionID,
		"status":     string(StatusRunning),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)

	if opts.Provider == ProviderOllama {
		history, _ := dbpkg.ListMessages(m.db, opts.SessionID)
		if len(history) > 0 {
			history = history[:len(history)-1]
		}
		handle, err := startOllamaRun(ctx, opts, history)
		if err != nil {
			cancel()
			dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusError))
			m.bus.Broadcast(opts.SessionID, map[string]any{
				"type":       "error",
				"session_id": opts.SessionID,
				"message":    err.Error(),
			})
			return err
		}
		handle.ps.Cancel = cancel

		m.mu.Lock()
		m.procs[opts.SessionID] = handle.ps
		m.mu.Unlock()

		go m.streamOllamaOutput(handle)
		return nil
	}

	if opts.Provider == ProviderAnthropic {
		history, _ := dbpkg.ListMessages(m.db, opts.SessionID)
		if len(history) > 0 {
			history = history[:len(history)-1]
		}
		handle, err := startAnthropicRun(ctx, opts, history)
		if err != nil {
			cancel()
			dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusError))
			m.bus.Broadcast(opts.SessionID, map[string]any{
				"type":       "error",
				"session_id": opts.SessionID,
				"message":    err.Error(),
			})
			return err
		}
		handle.ps.Cancel = cancel

		m.mu.Lock()
		m.procs[opts.SessionID] = handle.ps
		m.mu.Unlock()

		go m.streamAnthropicOutput(handle)
		return nil
	}

	// Default: claude CLI
	opts.ClaudeSettings = m.claudeSettings
	sr, err := spawnClaude(ctx, opts, m.backendNATSURL)
	if err != nil {
		cancel()
		dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusError))
		m.bus.Broadcast(opts.SessionID, map[string]any{
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

func (m *Manager) streamOllamaOutput(handle *ollamaRunHandle) {
	ps := handle.ps
	defer func() {
		ps.Cancel()
		m.mu.Lock()
		delete(m.procs, ps.SessionID)
		m.mu.Unlock()
	}()

	seq, _ := dbpkg.NextMessageSeq(m.db, ps.SessionID)

	emit := func(event any) {
		if data, err := json.Marshal(event); err == nil {
			log.Printf("[ollama→nats] session=%s %s", ps.SessionID[:8], string(data))
		}
		m.bus.Broadcast(ps.SessionID, event)
	}

	persistText := func(text string) {
		_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
			ID:        uuid.New().String(),
			SessionID: ps.SessionID,
			Role:      "assistant",
			Content:   text,
			Seq:       seq,
		})
	}

	askPermission := func(toolUseID, toolName string, toolInput json.RawMessage) string {
		return m.ollamaToolPermissionRequest(ps.SessionID, toolUseID, toolName, toolInput)
	}

	streamOllama(handle, ps.SessionID, emit, persistText, askPermission,
		handle.workDir, handle.host, handle.model, handle.options, handle.messages)

	dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
	m.bus.Broadcast(ps.SessionID, map[string]any{
		"type":       "session_status",
		"session_id": ps.SessionID,
		"status":     string(StatusIdle),
	})
}

func (m *Manager) streamAnthropicOutput(handle *anthropicRunHandle) {
	ps := handle.ps
	defer func() {
		ps.Cancel()
		m.mu.Lock()
		delete(m.procs, ps.SessionID)
		m.mu.Unlock()
	}()

	seq, _ := dbpkg.NextMessageSeq(m.db, ps.SessionID)

	emit := func(event any) {
		if data, err := json.Marshal(event); err == nil {
			log.Printf("[anthropic→nats] session=%s %s", ps.SessionID[:8], string(data))
		}
		m.bus.Broadcast(ps.SessionID, event)
	}

	persistText := func(text string) {
		_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
			ID:        uuid.New().String(),
			SessionID: ps.SessionID,
			Role:      "assistant",
			Content:   text,
			Seq:       seq,
		})
	}

	askPermission := func(toolUseID, toolName string, toolInput json.RawMessage) string {
		return m.ollamaToolPermissionRequest(ps.SessionID, toolUseID, toolName, toolInput)
	}

	streamAnthropic(handle, ps.SessionID, emit, persistText, askPermission)

	dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
	m.bus.Broadcast(ps.SessionID, map[string]any{
		"type":       "session_status",
		"session_id": ps.SessionID,
		"status":     string(StatusIdle),
	})
}

func (m *Manager) streamOutput(sr *spawnResult) {
	ps := sr.ps
	opts := sr.opts
	defer func() {
		// Close stdin so the CLI sees EOF and can exit cleanly.
		ps.StdinMu.Lock()
		if ps.StdinPipe != nil {
			ps.StdinPipe.Close()
			ps.StdinPipe = nil
		}
		ps.StdinMu.Unlock()

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

		// Handle control_response (e.g. get_context_usage) — don't broadcast raw,
		// instead parse and re-broadcast as a context_usage event.
		if event.EventType == "control_response" {
			if cu, err := parseContextUsage(event.Event); err == nil {
				m.bus.Broadcast(ps.SessionID, map[string]any{
					"type":        "context_usage",
					"session_id":  ps.SessionID,
					"categories":  cu.Categories,
					"totalTokens": cu.TotalTokens,
					"maxTokens":   cu.MaxTokens,
					"percentage":  cu.Percentage,
					"model":       cu.Model,
				})
			} else {
				log.Printf("[context] parse error session=%s: %v", sid8, err)
			}
			continue
		}

		// Broadcast immediately (before processing) so frontend sees updates live
		m.bus.Broadcast(ps.SessionID, event)

		if event.EventType == "stream_event" && event.Event != nil {
			for _, msg := range accum.process(event.Event, ps.SessionID) {
				msg.Seq = seq
				_ = dbpkg.InsertMessage(m.db, msg)
				seq++
			}
		}

		if isResult {
			log.Printf("[stream] session=%s turn complete: subtype=%s", sid8, event.ResultSubtype)
			dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
			m.bus.Broadcast(ps.SessionID, map[string]any{
				"type":       "session_status",
				"session_id": ps.SessionID,
				"status":     string(StatusIdle),
			})

			// Persist token usage for this turn.
			if accum.inputTokens > 0 || accum.outputTokens > 0 {
				tu := dbpkg.TurnUsage{
					ID:                       uuid.New().String(),
					SessionID:                ps.SessionID,
					ProjectID:                opts.ProjectID,
					AccountID:                opts.AccountID,
					Model:                    opts.Model,
					InputTokens:              accum.inputTokens,
					OutputTokens:             accum.outputTokens,
					CacheCreationInputTokens: accum.cacheCreationInputTokens,
					CacheReadInputTokens:     accum.cacheReadInputTokens,
				}
				if err := dbpkg.InsertTurnUsage(m.db, tu); err != nil {
					log.Printf("[usage] insert error session=%s: %v", sid8, err)
				}
				// Broadcast authoritative token counts so the UI header stays accurate.
				m.bus.Broadcast(ps.SessionID, map[string]any{
					"type":                        "turn_usage",
					"session_id":                  ps.SessionID,
					"input_tokens":                accum.inputTokens,
					"output_tokens":               accum.outputTokens,
					"cache_creation_input_tokens": accum.cacheCreationInputTokens,
					"cache_read_input_tokens":     accum.cacheReadInputTokens,
					"model":                       opts.Model,
				})
			}

			// Send get_context_usage and keep scanning — the control_response
			// will arrive as another stdout line and be handled in the next iteration.
			if err := RequestContextUsage(ps); err != nil {
				log.Printf("[context] request error session=%s: %v", sid8, err)
				return
			}
			// Don't return yet — wait for the control_response line.
			// The process will exit after responding, causing scanner.Scan() to return false.
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[stream] session=%s scanner error: %v", sid8, err)
	}

	exitCode := 0
	if ps.Cmd.ProcessState != nil {
		exitCode = ps.Cmd.ProcessState.ExitCode()
	}
	log.Printf("[stream] session=%s process exited code=%d", sid8, exitCode)

	if exitCode != 0 {
		msg := fmt.Sprintf("claude exited with code %s", strconv.Itoa(exitCode))
		m.bus.Broadcast(ps.SessionID, map[string]any{
			"type":       "error",
			"session_id": ps.SessionID,
			"message":    msg,
		})
		dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusError))
	} else {
		dbpkg.UpdateSessionStatus(m.db, ps.SessionID, string(StatusIdle))
	}
}

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

	// Persist to NATS KV for hook state
	_ = m.bus.SetHookState(sessID, toolUseID, natspkg.HookState{
		SessionID: sessID,
		ToolUseID: toolUseID,
		ToolName:  toolName,
		ToolInput: toolInput,
		Status:    "pending",
		CreatedAt: createdAt,
	})

	m.bus.BroadcastToRole(sessID, natspkg.RoleClient, map[string]any{
		"type":        "hook_pending",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  toolInput,
		"created_at":  createdAt.Format(time.RFC3339Nano),
	})
}

func (m *Manager) ollamaToolPermissionRequest(sessID, toolUseID, toolName string, toolInput json.RawMessage) string {
	ch := make(chan string, 1)
	m.mu.Lock()
	m.ollamaPending[toolUseID] = ch
	m.mu.Unlock()

	createdAt := time.Now().UTC()
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusWaitingHook))
	m.bus.BroadcastToRole(sessID, natspkg.RoleClient, map[string]any{
		"type":        "hook_pending",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  toolInput,
		"created_at":  createdAt.Format(time.RFC3339Nano),
	})

	decision := <-ch

	m.mu.Lock()
	delete(m.ollamaPending, toolUseID)
	m.mu.Unlock()

	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	m.bus.BroadcastToRole(sessID, natspkg.RoleClient, map[string]any{
		"type":        "hook_resolved",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"decision":    decision,
	})

	return decision
}

func (m *Manager) onHookRespond(sessID, toolUseID, decision, reason string) {
	// Check if this is an Ollama tool permission request.
	m.mu.Lock()
	ch, isOllama := m.ollamaPending[toolUseID]
	m.mu.Unlock()
	if isOllama {
		ch <- decision
		return
	}

	hook, err := dbpkg.GetPendingHook(m.db, sessID, toolUseID)
	if err == nil {
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

	// Delete from NATS KV
	_ = m.bus.DeleteHookState(sessID, toolUseID)

	remaining, _ := dbpkg.CountPendingHooks(m.db, sessID)
	if remaining == 0 {
		dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	}

	// Forward decision to the waiting lucodehook via NATS
	m.bus.SendHookResponse(sessID, toolUseID, map[string]any{
		"type":        "hook_response",
		"tool_use_id": toolUseID,
		"decision":    decision,
		"reason":      reason,
	})

	m.bus.BroadcastToRole(sessID, natspkg.RoleClient, map[string]any{
		"type":        "hook_resolved",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"decision":    decision,
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

// turnAccum accumulates Claude streaming events and emits finalized DB messages.
type turnAccum struct {
	text       strings.Builder
	thinking   strings.Builder
	blockTypes map[int]string

	// token usage accumulated from the stream
	inputTokens              int
	outputTokens             int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
}

func newTurnAccum() *turnAccum {
	return &turnAccum{blockTypes: make(map[int]string)}
}

func (a *turnAccum) process(raw json.RawMessage, sessionID string) []dbpkg.Message {
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
	case "message_start":
		// Extract input token counts from the inner event (outer.Event)
		var msgStart struct {
			Message struct {
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(outer.Event, &msgStart); err == nil {
			a.inputTokens = msgStart.Message.Usage.InputTokens
			a.cacheCreationInputTokens = msgStart.Message.Usage.CacheCreationInputTokens
			a.cacheReadInputTokens = msgStart.Message.Usage.CacheReadInputTokens
		}

	case "message_delta":
		// Extract output token count from the inner event
		var msgDelta struct {
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(outer.Event, &msgDelta); err == nil {
			a.outputTokens = msgDelta.Usage.OutputTokens
		}

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
