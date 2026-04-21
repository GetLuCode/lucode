package session

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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
	aiAPIURL       string
	natsCreds      string // path to NATS .creds file, forwarded to hook subprocess

	mu    sync.Mutex
	procs map[string]*ProcessState
}

func NewManager(db *sql.DB, bus *natspkg.Bus, backendNATSURL, claudeSettings string) *Manager {
	m := &Manager{
		db:             db,
		bus:            bus,
		backendNATSURL: backendNATSURL,
		claudeSettings: claudeSettings,
		procs:          make(map[string]*ProcessState),
	}
	bus.OnHookRequest = func(sessID, toolUseID, toolName string, toolInput json.RawMessage) {
		m.onHookRequest(sessID, toolUseID, toolName, toolInput)
	}
	bus.OnHookRespond = func(sessID, toolUseID, decision, reason string) {
		m.onHookRespond(sessID, toolUseID, decision, reason)
	}
	bus.OnHookDisconnect = func(sessID, toolUseID string) {}
	return m
}

// SetAIAPIURL configures the URL of the aiapi service.
func (m *Manager) SetAIAPIURL(url string) {
	m.aiAPIURL = url
	log.Printf("[manager] aiapi url — %s", url)
}

// SetNATSCreds sets the path to the NATS credentials file forwarded to hook subprocesses.
func (m *Manager) SetNATSCreds(path string) {
	m.natsCreds = path
}

// StartTurn launches an AI turn. Returns immediately; the turn runs in a goroutine.
func (m *Manager) StartTurn(opts TurnOptions) error {
	m.mu.Lock()
	_, active := m.procs[opts.SessionID]
	if active {
		m.mu.Unlock()
		return ErrSessionBusy
	}
	m.mu.Unlock()

	dbpkg.UpdateSessionStatus(m.db, opts.SessionID, string(StatusRunning))

	// Persist user message
	seq, _ := dbpkg.NextMessageSeq(m.db, opts.SessionID)
	content := opts.Message
	if opts.ImageBase64 != "" {
		blocks := []map[string]interface{}{
			{"type": "text", "text": opts.Message},
			{"type": "image", "source": map[string]interface{}{
				"type":       "base64",
				"media_type": opts.ImageMediaType,
				"data":       opts.ImageBase64,
			}},
		}
		b, _ := json.Marshal(blocks)
		content = string(b)
	}
	opts.TurnStartSeq = seq
	userMsgID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
		ID:        userMsgID,
		SessionID: opts.SessionID,
		Role:      "user",
		Content:   content,
		Seq:       seq,
	})
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

	// Load history (needed for Ollama/Anthropic; harmless for Claude CLI)
	history, _ := dbpkg.ListMessages(m.db, opts.SessionID)
	if len(history) > 0 {
		history = history[:len(history)-1] // exclude the user msg just inserted
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	ps := &ProcessState{
		SessionID: opts.SessionID,
		Done:      make(chan struct{}),
		Cancel:    cancel,
	}
	m.mu.Lock()
	m.procs[opts.SessionID] = ps
	m.mu.Unlock()

	go m.runWithAIAPI(ctx, ps, opts, history)
	return nil
}

// runWithAIAPI calls the aiapi service via HTTP and streams SSE events
// to the NATS bus (→ WS clients) while persisting relevant events to DB.
func (m *Manager) runWithAIAPI(ctx context.Context, ps *ProcessState, opts TurnOptions, history []dbpkg.Message) {
	sessID := ps.SessionID
	defer func() {
		ps.Cancel()
		close(ps.Done)
		m.mu.Lock()
		if m.procs[sessID] == ps {
			delete(m.procs, sessID)
		}
		m.mu.Unlock()
	}()

	// Build history payload (user + assistant only)
	type histMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	histMsgs := make([]histMsg, 0, len(history))
	for _, h := range history {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		histMsgs = append(histMsgs, histMsg{Role: h.Role, Content: h.Content})
	}

	claudeConfigDir := opts.AccountClaudeConfigDir
	if claudeConfigDir == "" {
		claudeConfigDir = opts.ClaudeConfigDir
	}

	reqBody := map[string]any{
		"session_id":        sessID,
		"message":           opts.Message,
		"model":             opts.Model,
		"provider":          string(opts.Provider),
		"work_dirs":         opts.WorkDirs,
		"resume":            opts.Resume,
		"fork_from":         opts.ForkFrom,
		"name":              opts.Name,
		"effort":            opts.Effort,
		"thinking":          opts.Thinking,
		"claude_config_dir": claudeConfigDir,
		"claude_settings":   m.claudeSettings,
		"nats_settings":     m.backendNATSURL,
		"nats_creds":        m.natsCreds,
		"ollama_host":       opts.OllamaHost,
		"image_base64":      opts.ImageBase64,
		"image_media_type":  opts.ImageMediaType,
		"history":           histMsgs,
	}
	data, _ := json.Marshal(reqBody)

	postResp, err := http.Post(m.aiAPIURL+"/turns", "application/json", bytes.NewReader(data))
	if err != nil {
		m.broadcastError(sessID, "aiapi unreachable: "+err.Error())
		return
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		m.broadcastError(sessID, fmt.Sprintf("aiapi returned %d", postResp.StatusCode))
		return
	}

	var turnResp struct {
		TurnID string `json:"turn_id"`
	}
	json.NewDecoder(postResp.Body).Decode(&turnResp)
	if turnResp.TurnID == "" {
		m.broadcastError(sessID, "aiapi: empty turn_id")
		return
	}

	// Stream SSE from GET /turns/{id}/stream
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", m.aiAPIURL+"/turns/"+turnResp.TurnID+"/stream", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		m.broadcastError(sessID, "aiapi stream error: "+err.Error())
		return
	}
	defer sseResp.Body.Close()

	nextSeq, _ := dbpkg.NextMessageSeq(m.db, sessID)

	scanner := bufio.NewScanner(sseResp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &ev); err != nil {
			continue
		}

		evType, _ := ev["type"].(string)
		switch evType {
		case "message_ready":
			msg, _ := ev["message"].(map[string]any)
			if msg != nil {
				role, _ := msg["role"].(string)
				content, _ := msg["content"].(string)
				if role != "" && content != "" {
					_ = dbpkg.InsertMessage(m.db, dbpkg.Message{
						ID:        uuid.New().String(),
						SessionID: sessID,
						Role:      role,
						Content:   content,
						Seq:       nextSeq,
					})
					nextSeq++
				}
			}

		case "session_status":
			if status, _ := ev["status"].(string); status != "" {
				dbpkg.UpdateSessionStatus(m.db, sessID, status)
			}

		case "turn_usage":
			tu := dbpkg.TurnUsage{
				ID:        uuid.New().String(),
				SessionID: sessID,
				ProjectID: opts.ProjectID,
				AccountID: opts.AccountID,
				Model:     opts.Model,
			}
			if v, ok := ev["input_tokens"].(float64); ok {
				tu.InputTokens = int(v)
			}
			if v, ok := ev["output_tokens"].(float64); ok {
				tu.OutputTokens = int(v)
			}
			if v, ok := ev["cache_creation_input_tokens"].(float64); ok {
				tu.CacheCreationInputTokens = int(v)
			}
			if v, ok := ev["cache_read_input_tokens"].(float64); ok {
				tu.CacheReadInputTokens = int(v)
			}
			if err := dbpkg.InsertTurnUsage(m.db, tu); err != nil {
				log.Printf("[manager] turn_usage insert sess=%s: %v", sessID[:8], err)
			}

		case "context_usage":
			if usageJSON, err := json.Marshal(ev); err == nil {
				_ = dbpkg.UpdateSessionContextUsage(m.db, sessID, string(usageJSON))
			}
		}

		// Forward every event to WS clients via NATS bus
		m.bus.Broadcast(sessID, ev)
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Printf("[manager] SSE scanner error sess=%s: %v", sessID[:8], err)
	}
}

func (m *Manager) broadcastError(sessID, msg string) {
	dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusError))
	m.bus.Broadcast(sessID, map[string]any{
		"type":       "session_status",
		"session_id": sessID,
		"status":     string(StatusError),
		"error":      msg,
	})
	log.Printf("[manager] error sess=%s: %s", sessID[:8], msg)
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

func (m *Manager) onHookRespond(sessID, toolUseID, decision, reason string) {
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
	_ = m.bus.DeleteHookState(sessID, toolUseID)

	remaining, _ := dbpkg.CountPendingHooks(m.db, sessID)
	if remaining == 0 {
		dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	}

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

func jsonStr(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	json.Unmarshal(raw, &s)
	return s
}
