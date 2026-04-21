package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Run executes a turn synchronously, calling publish for every event.
// publish is called in-line; Run blocks until the turn completes or ctx is cancelled.
//
// Event types used (same JSON format as manager.go bus.Broadcast calls):
//   - "stream_event", "session_status", "turn_complete", "error", "context_usage"
//     → relay these directly to WebSocket clients
//   - "message_ready": finalized assistant/thinking message (id, role, content)
//   - "turn_usage": per-turn token counts with project/account metadata
//     → relay layer uses these two for DB persistence
func Run(ctx context.Context, req TurnRequest, publish func(event map[string]any)) {
	if req.Provider == ProviderOllama {
		runOllama(ctx, req, publish)
		return
	}
	runClaude(ctx, req, publish)
}

// ─── Claude CLI path ──────────────────────────────────────────────────────────

func runClaude(ctx context.Context, req TurnRequest, publish func(map[string]any)) {
	sr, err := spawnClaude(ctx, req)
	if err != nil {
		publish(map[string]any{"type": "error", "session_id": req.SessionID, "message": err.Error()})
		publish(map[string]any{"type": "session_status", "session_id": req.SessionID, "status": "error"})
		return
	}

	ps := sr.ps
	defer func() {
		ps.StdinMu.Lock()
		if ps.StdinPipe != nil {
			ps.StdinPipe.Close()
			ps.StdinPipe = nil
		}
		ps.StdinMu.Unlock()
		if ps.Cancel != nil {
			ps.Cancel()
		}
		close(ps.Done)
		ps.Cmd.Wait()
	}()

	sid8 := req.SessionID[:8]
	scanner := bufio.NewScanner(sr.stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	accum := newTurnAccum()
	turnCompleted := false

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		log.Printf("[executor/stdout] session=%s: %s", sid8, string(line))

		event, isResult, err := ParseLine(req.SessionID, line)
		if err != nil {
			log.Printf("[executor] session=%s parse error: %v", sid8, err)
			continue
		}
		if event.EventType == "" {
			continue
		}

		if event.EventType == "control_response" {
			if cu, err := ParseContextUsage(event.Event); err == nil {
				publish(map[string]any{
					"type":        "context_usage",
					"session_id":  req.SessionID,
					"categories":  cu.Categories,
					"totalTokens": cu.TotalTokens,
					"maxTokens":   cu.MaxTokens,
					"percentage":  cu.Percentage,
					"model":       cu.Model,
				})
			} else {
				log.Printf("[executor] session=%s context parse error: %v", sid8, err)
			}
			continue
		}

		publish(parsedEventToMap(event))

		if event.EventType == "stream_event" && event.Event != nil {
			for _, msg := range accum.process(event.Event, req.SessionID) {
				publish(map[string]any{
					"type":       "message_ready",
					"session_id": req.SessionID,
					"id":         msg.id,
					"role":       msg.role,
					"content":    msg.content,
				})
			}
		}

		if isResult {
			log.Printf("[executor] session=%s turn complete: subtype=%s", sid8, event.ResultSubtype)

			if accum.inputTokens > 0 || accum.outputTokens > 0 {
				publish(map[string]any{
					"type":                        "turn_usage",
					"session_id":                  req.SessionID,
					"project_id":                  req.ProjectID,
					"account_id":                  req.AccountID,
					"model":                       req.Model,
					"input_tokens":                accum.inputTokens,
					"output_tokens":               accum.outputTokens,
					"cache_creation_input_tokens": accum.cacheCreationInputTokens,
					"cache_read_input_tokens":     accum.cacheReadInputTokens,
					"turn_start_seq":              req.TurnStartSeq,
				})
			}

			publish(map[string]any{
				"type":       "session_status",
				"session_id": req.SessionID,
				"status":     "idle",
			})
			turnCompleted = true

			if err := RequestContextUsage(ps); err != nil {
				log.Printf("[executor] session=%s context request error: %v", sid8, err)
				return
			}
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[executor] session=%s scanner error: %v", sid8, err)
	}

	exitCode := 0
	if ps.Cmd.ProcessState != nil {
		exitCode = ps.Cmd.ProcessState.ExitCode()
	}
	log.Printf("[executor] session=%s process exited code=%d", sid8, exitCode)

	if !turnCompleted {
		if exitCode != 0 {
			publish(map[string]any{
				"type":       "error",
				"session_id": req.SessionID,
				"message":    "claude exited with code " + strconv.Itoa(exitCode),
			})
			publish(map[string]any{"type": "session_status", "session_id": req.SessionID, "status": "error"})
		} else {
			publish(map[string]any{"type": "session_status", "session_id": req.SessionID, "status": "idle"})
		}
	}
}

// ─── Ollama path ──────────────────────────────────────────────────────────────

func runOllama(ctx context.Context, req TurnRequest, publish func(map[string]any)) {
	// Tool permission is always allowed in the executor; the relay layer (lucode)
	// handles the interactive approval flow for non-runner sessions.
	askPermission := func(toolUseID, toolName string, toolInput json.RawMessage) string {
		return "allow"
	}

	handle, err := startOllamaRun(ctx, req, askPermission)
	if err != nil {
		publish(map[string]any{"type": "error", "session_id": req.SessionID, "message": err.Error()})
		publish(map[string]any{"type": "session_status", "session_id": req.SessionID, "status": "error"})
		return
	}

	streamOllama(handle, req.SessionID, publish)

	publish(map[string]any{
		"type":       "session_status",
		"session_id": req.SessionID,
		"status":     "idle",
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func parsedEventToMap(e ParsedEvent) map[string]any {
	m := map[string]any{
		"type":       e.EventType,
		"session_id": e.SessionID,
	}
	if e.Status != "" {
		m["status"] = e.Status
	}
	if e.Event != nil {
		m["event"] = e.Event
	}
	if e.Partial {
		m["partial"] = true
	}
	if e.ResultSubtype != "" {
		m["result_subtype"] = e.ResultSubtype
	}
	if e.ResultText != "" {
		m["result_text"] = e.ResultText
	}
	if e.Message != "" {
		m["message"] = e.Message
	}
	return m
}

// ─── Turn accumulator ─────────────────────────────────────────────────────────

type accumMessage struct {
	id      string
	role    string
	content string
}

type turnAccum struct {
	text       strings.Builder
	thinking   strings.Builder
	blockTypes map[int]string

	inputTokens              int
	outputTokens             int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
}

func newTurnAccum() *turnAccum {
	return &turnAccum{blockTypes: make(map[int]string)}
}

func (a *turnAccum) process(raw json.RawMessage, sessionID string) []accumMessage {
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
		var block struct{ Type string `json:"type"` }
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
				return []accumMessage{{id: uuid.New().String(), role: "thinking", content: text}}
			}
		}

	case "message_stop":
		text := a.text.String()
		a.text.Reset()
		a.blockTypes = make(map[int]string)
		if text != "" {
			return []accumMessage{{id: uuid.New().String(), role: "assistant", content: text}}
		}
	}

	return nil
}
