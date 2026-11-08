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
	"claude-orchestrator/internal/permissions"

	"github.com/google/uuid"
	"claude-orchestrator/internal/taskmcp"
	execpkg "claude-orchestrator/internal/executor"
)

var ErrSessionBusy = errors.New("session already has an active turn")

type Manager struct {
	// BaseDir is the working dir of this instance (its --working-dir): the folder a
	// relative project work dir is resolved against. Empty means the user's home.
	BaseDir string

	// Live claude processes by session (agents with Persistent set).
	liveMu sync.Mutex
	lives  map[string]*execpkg.Live
	// remoteRunnerOf: runner holding the persistent process of a session run remotely.
	remoteRunnerOf map[string]string
	// remotePID / remoteAt: pid of that process (0 until the runner says) and the time
	// of the last notice about it, to drop stale ones. Guarded by liveMu like the rest.
	remotePID map[string]int
	remoteAt  map[string]int64
	// workspaces: where claude works in each session (its claude_workspace event), as
	// the machine that runs it reported. Guarded by liveMu.
	workspaces map[string]map[string]any

	// OnTurnEnd runs after a turn finishes and the session is free again (used to
	// deliver messages other agents queued for it).
	OnTurnEnd func(sessID string)
	// OnTurnFailed is called when a turn failed in a way the user or a supervising
	// agent must hear about (e.g. code "resume_failed").
	OnTurnFailed func(sessID, code, message string)

	db             *sql.DB
	bus            *natspkg.Bus
	backendNATSURL string
	claudeSettings string
	aiAPIURL       string
	natsCreds      string // path to NATS .creds file, forwarded to hook subprocess

	mu           sync.Mutex
	procs        map[string]*ProcessState
	turnCounters map[string]int // session_id -> current turn index

	// pendingApprovals holds the channels that gate.go is waiting on
	// for the user's response. Keyed by "<sessID>:<toolUseID>".
	pendingApprovalsMu sync.Mutex
	pendingApprovals   map[string]chan hookResponse

	// deniedThisTurn tracks sessions that have had at least one tool
	// denied in the current turn. Subsequent tool calls in the same
	// turn are auto-denied with an instructive reason so the model
	// stops and asks the user how to proceed instead of retrying.
	deniedThisTurn map[string]bool

	// workDirsBySession caches the first work_dir per session so
	// permission lookups don't need to re-derive it.
	workDirsBySession map[string]string

	logMu sync.Mutex // tool calls arrive concurrently and each one takes the next message seq

	// ToolGate gets the tool calls no rule decided, before a person does. It returns "allow"
	// or "deny" with the reason the agent reads; anything else leaves it open. Set by the hub
	// before turns start. It blocks, for a limited time.
	ToolGate func(ToolRequest) (decision, reason string)

	// PluginTurn answers a turn of executor "plugin". Set by the hub; a runner has none.
	PluginTurn func(ctx context.Context, req PluginTurnRequest, progress func(string)) (string, error)
}

func NewManager(db *sql.DB, bus *natspkg.Bus, backendNATSURL, claudeSettings string) *Manager {
	m := &Manager{
		db:                 db,
		bus:                bus,
		backendNATSURL:     backendNATSURL,
		claudeSettings:     claudeSettings,
		procs:              make(map[string]*ProcessState),
		lives:              make(map[string]*execpkg.Live),
		remoteRunnerOf:     make(map[string]string),
		remotePID:          make(map[string]int),
		remoteAt:           make(map[string]int64),
		workspaces:         make(map[string]map[string]any),
		turnCounters:       make(map[string]int),
		pendingApprovals:   make(map[string]chan hookResponse),
		deniedThisTurn:     make(map[string]bool),
		workDirsBySession:  make(map[string]string),
	}
	bus.OnHookRequest = func(sessID, toolUseID, toolName string, toolInput json.RawMessage) {
		m.onHookRequest(sessID, toolUseID, toolName, toolInput, time.Now().UTC())
	}
	bus.OnHookRespond = func(sessID, toolUseID, decision, reason, ruleJSON string) {
		m.onHookRespond(sessID, toolUseID, decision, reason, ruleJSON)
	}
	bus.OnHookDisconnect = func(sessID, toolUseID string) {}
	bus.OnPermissionRequest = m.onPermissionRequest
	return m
}

// hookResponse is the value delivered on a pendingApprovals channel
// when the user (or settings file) decides on a pending permission.
type hookResponse struct {
	decision string
	reason   string
	ruleJSON string // optional, populated for allow_always / allow_with_rule
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

// broadcastAndPersist sends an event to WS clients and persists it to the raw_events table.
func (m *Manager) broadcastAndPersist(sessID string, ev map[string]any) {
	// Always inject session_id so the frontend can route the event.
	if _, ok := ev["session_id"]; !ok {
		ev["session_id"] = sessID
	}
	// Get current turn index
	m.mu.Lock()
	turnIdx := m.turnCounters[sessID]
	m.mu.Unlock()

	// Persist to DB asynchronously (non-blocking)
	evJSON, _ := json.Marshal(ev)
	go func() {
		_ = dbpkg.InsertRawEvent(m.db, dbpkg.RawEvent{
			ID:        uuid.New().String(),
			SessionID: sessID,
			TurnIndex: turnIdx,
			Event:     json.RawMessage(evJSON),
		})
	}()

	// Increment turn counter when turn completes
	if evType, _ := ev["type"].(string); evType == "turn_complete" {
		m.mu.Lock()
		m.turnCounters[sessID]++
		m.mu.Unlock()
	}

	m.bus.Broadcast(sessID, ev)
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

	// Reset denial flag so a fresh turn starts with no prior denials.
	m.pendingApprovalsMu.Lock()
	m.deniedThisTurn[opts.SessionID] = false
	m.pendingApprovalsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	ps := &ProcessState{
		SessionID: opts.SessionID,
		Done:      make(chan struct{}),
		Cancel:    cancel,
	}
	m.mu.Lock()
	m.procs[opts.SessionID] = ps
	m.mu.Unlock()

	if opts.Executor == "plugin" {
		go m.runWithPlugin(ctx, ps, opts)
	} else if opts.Executor == "claude" && opts.RunnerID != "" {
		go m.runWithRemoteCLI(ctx, ps, opts)
	} else if opts.Executor == "claude" {
		go m.runWithCLI(ctx, ps, opts)
	} else if opts.AIAPIInstanceID != "" {
		go m.runWithNATSAIAPI(ctx, ps, opts, history)
	} else {
		go m.runWithAIAPI(ctx, ps, opts, history)
	}
	return nil
}

// CancelTurn cancels an active turn for a session, returning the ProcessState if one was active.
func (m *Manager) CancelTurn(sessionID string) *ProcessState {
	m.mu.Lock()
	ps := m.procs[sessionID]
	m.mu.Unlock()

	if ps == nil {
		return nil
	}

	log.Printf("[manager] canceling turn for session=%s", sessionID)
	ps.Cancel()
	return ps
}

// runWithAIAPI calls the aiapi service via HTTP and streams SSE events
// to the NATS bus (→ WS clients) while persisting relevant events to DB.
func (m *Manager) runWithAIAPI(ctx context.Context, ps *ProcessState, opts TurnOptions, history []dbpkg.Message) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)

	// Cache the work_dir so the permission store can resolve settings.json.
	if len(opts.WorkDirs) > 0 {
		m.SetWorkDir(sessID, opts.WorkDirs[0])
	}

	// Build history payload (user + assistant only) and include it in the
	// request body. aiapi is stateless — it needs the prior turns on every
	// call to keep the model in context. This mirrors runWithNATSAIAPI.
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

	reqBody := map[string]any{
		"session_id":       sessID,
		"message":          opts.Message,
		"model":            opts.Model,
		"work_dirs":        opts.WorkDirs,
		"resume":           opts.Resume,
		"fork_from":        opts.ForkFrom,
		"name":             opts.Name,
		"image_base64":     opts.ImageBase64,
		"image_media_type": opts.ImageMediaType,
		"thinking":         opts.Thinking,
		"system_prompt":    opts.SystemPrompt,
		"tools":            opts.Tools,
		"lucode_tools":     opts.LucodeTools,
		"history":          histMsgs,
		// Pass our NATS URL so aiapi can lazily connect for the permission gate
		// even if it wasn't started with --nats-url.
		"nats_url": m.backendNATSURL,
	}

	data, _ := json.Marshal(reqBody)
	log.Printf("[manager] POST /turns: session_id=%s", sessID)
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
				thinking, _ := msg["thinking"].(string)
				log.Printf("[manager] message_ready sess=%s role=%q content_len=%d thinking_len=%d nextSeq=%d", sessID[:8], role, len(content), len(thinking), nextSeq)
				// Persist the thinking block as its own message so it's restored
				// after a page refresh (the streaming text is volatile).
				if role == "assistant" && thinking != "" {
					if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
						ID:        uuid.New().String(),
						SessionID: sessID,
						Role:      "thinking",
						Content:   thinking,
						Seq:       nextSeq,
					}); err != nil {
						log.Printf("[manager] THINKING INSERT FAILED sess=%s seq=%d role=thinking err=%v", sessID[:8], nextSeq, err)
					} else {
						log.Printf("[manager] inserted thinking sess=%s seq=%d len=%d", sessID[:8], nextSeq, len(thinking))
						nextSeq++
					}
				}
				if role != "" && content != "" {
					if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
						ID:        uuid.New().String(),
						SessionID: sessID,
						Role:      role,
						Content:   content,
						Seq:       nextSeq,
					}); err != nil {
						log.Printf("[manager] ASSISTANT INSERT FAILED sess=%s seq=%d role=%s err=%v", sessID[:8], nextSeq, role, err)
					} else {
						log.Printf("[manager] inserted assistant sess=%s seq=%d role=%s len=%d", sessID[:8], nextSeq, role, len(content))
						nextSeq++
					}
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

		// Forward every event to WS clients via NATS bus and persist to DB
		m.broadcastAndPersist(sessID, ev)
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Printf("[manager] SSE scanner error sess=%s: %v", sessID[:8], err)
	}
}

// runWithNATSAIAPI executes a turn via a remote aiapi instance over NATS.
// It reuses the same event processing logic as runWithAIAPI.
func (m *Manager) runWithNATSAIAPI(ctx context.Context, ps *ProcessState, opts TurnOptions, history []dbpkg.Message) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)

	// Cache the work_dir so the permission store can resolve settings.json.
	if len(opts.WorkDirs) > 0 {
		m.SetWorkDir(sessID, opts.WorkDirs[0])
	}

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

	payload := map[string]any{
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
		"tools":             opts.Tools,
		"lucode_tools":      opts.LucodeTools,
		"ollama_host":       opts.OllamaHost,
		"image_base64":      opts.ImageBase64,
		"image_media_type":  opts.ImageMediaType,
		"account":           opts.AccountID,
		"history":           histMsgs,
	}
	log.Printf("[manager] NATS turn: session_id=%s account=%q claude_config_dir=%q", sessID, opts.AccountID, claudeConfigDir)
	data, _ := json.Marshal(payload)

	nextSeq, _ := dbpkg.NextMessageSeq(m.db, sessID)

	err := m.bus.AIAPIExecute(opts.AIAPIInstanceID, data, func(eventData []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		var ev map[string]any
		if err := json.Unmarshal(eventData, &ev); err != nil {
			return true
		}

		evType, _ := ev["type"].(string)
		switch evType {
		case "message_ready":
			msg, _ := ev["message"].(map[string]any)
			if msg != nil {
				role, _ := msg["role"].(string)
				content, _ := msg["content"].(string)
				thinking, _ := msg["thinking"].(string)
				log.Printf("[manager] nats message_ready sess=%s role=%q content_len=%d thinking_len=%d nextSeq=%d", sessID[:8], role, len(content), len(thinking), nextSeq)
				// Persist thinking as its own message so it survives refresh.
				if role == "assistant" && thinking != "" {
					if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
						ID:        uuid.New().String(),
						SessionID: sessID,
						Role:      "thinking",
						Content:   thinking,
						Seq:       nextSeq,
					}); err != nil {
						log.Printf("[manager] NATS THINKING INSERT FAILED sess=%s seq=%d err=%v", sessID[:8], nextSeq, err)
					} else {
						log.Printf("[manager] nats inserted thinking sess=%s seq=%d len=%d", sessID[:8], nextSeq, len(thinking))
						nextSeq++
					}
				}
				if role != "" && content != "" {
					if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
						ID:        uuid.New().String(),
						SessionID: sessID,
						Role:      role,
						Content:   content,
						Seq:       nextSeq,
					}); err != nil {
						log.Printf("[manager] NATS ASSISTANT INSERT FAILED sess=%s seq=%d role=%s err=%v", sessID[:8], nextSeq, role, err)
					} else {
						log.Printf("[manager] nats inserted assistant sess=%s seq=%d role=%s len=%d", sessID[:8], nextSeq, role, len(content))
						nextSeq++
					}
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
			dbpkg.InsertTurnUsage(m.db, tu)
		case "context_usage":
			if usageJSON, err := json.Marshal(ev); err == nil {
				dbpkg.UpdateSessionContextUsage(m.db, sessID, string(usageJSON))
			}
		}

		m.broadcastAndPersist(sessID, ev)
		return true
	})

	if err != nil && ctx.Err() == nil {
		log.Printf("[manager] nats aiapi error sess=%s: %v", sessID[:8], err)
		m.broadcastError(sessID, "aiapi error: "+err.Error())
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

// onHookRequest shows a person the request. createdAt is when the hook asked, which is what the
// countdown on the page runs from: a flow may already have used part of that time.
func (m *Manager) onHookRequest(sessID, toolUseID, toolName string, toolInput json.RawMessage, createdAt time.Time) {
	inputStr := string(toolInput)

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

// expireApproval clears an approval nobody answered: drops the pending row and
// in-memory state, and tells the UI it was resolved (denied) so it stops waiting.
func (m *Manager) expireApproval(sessID, toolUseID string) {
	dbpkg.DeletePendingHook(m.db, sessID, toolUseID)
	_ = m.bus.DeleteHookState(sessID, toolUseID)
	if remaining, _ := dbpkg.CountPendingHooks(m.db, sessID); remaining == 0 {
		dbpkg.UpdateSessionStatus(m.db, sessID, string(StatusRunning))
	}
	m.bus.BroadcastToRole(sessID, natspkg.RoleClient, map[string]any{
		"type":        "hook_resolved",
		"session_id":  sessID,
		"tool_use_id": toolUseID,
		"decision":    "deny",
	})
}

func (m *Manager) onHookRespond(sessID, toolUseID, decision, reason, ruleJSON string) {
	if hook, err := dbpkg.GetPendingHook(m.db, sessID, toolUseID); err == nil {
		content, _ := json.Marshal(map[string]any{
			"tool_use_id": toolUseID,
			"tool_name":   hook.ToolName,
			"tool_input":  json.RawMessage(hook.ToolInput),
			"decision":    decision,
			"reason":      reason,
		})
		m.logMu.Lock()
		seq, _ := dbpkg.NextMessageSeq(m.db, sessID)
		dbpkg.InsertMessage(m.db, dbpkg.Message{
			ID:        uuid.New().String(),
			SessionID: sessID,
			Role:      "hook",
			Content:   string(content),
			Seq:       seq,
		})
		m.logMu.Unlock()
	}

	// Persist a session-scope rule if the user picked "Allow always"
	// or "Edit permission". ruleJSON is a JSON-encoded
	// permissions.Rule (or empty).
	if ruleJSON != "" && (decision == "allow_always" || decision == "allow_with_rule") {
		var rule permissions.Rule
		if jerr := json.Unmarshal([]byte(ruleJSON), &rule); jerr == nil {
			scope := "allow"
			if decision == "allow_with_rule" {
				scope = "allow" // same scope, but the rule carries input_regex
			}
			if _, perr := permissions.AddSessionRule(permDBAdapter{m.db}, sessID, scope, rule); perr != nil {
				log.Printf("[manager] persist session rule failed sess=%s tool=%s: %v", sessID[:8], rule.Tool, perr)
			}
		}
	}

	// Unblock the permission gate if one is waiting on this hook.
	m.signalApproval(sessID, toolUseID, hookResponse{decision: decision, reason: reason, ruleJSON: ruleJSON})

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

// approvalTimeout is how long an approval can take, counted from when the hook asked. The hook
// gives up at permissionTimeout (cmd/lucodehook); the margin lets the hub answer first, so the
// agent reads the real reason and the page stops waiting at the same moment.
const (
	approvalTimeout = 4 * time.Minute
	approvalMargin  = 5 * time.Second
)

// ToolRequest is a tool call that no rule decided, offered to the ToolGate.
type ToolRequest struct {
	SessionID string
	ToolUseID string
	ToolName  string
	WorkDir   string
	AutoMode  bool
	Input     json.RawMessage
}

// Who decided a tool call, as the web's tool log shows it.
const (
	decidedByLucode = "lucode"
	decidedByRule   = "rule"
	decidedByFlow   = "flow"
	decidedByAuto   = "auto"
	decidedByPerson = "person"
)

// onPermissionRequest is invoked by the bus, once per request and concurrently, when the hook
// asks for permission to execute a tool. Whatever the answer, the call reaches the session's
// tool log: an approval a person answers is already there as its own message.
func (m *Manager) onPermissionRequest(req natspkg.PermissionRequest) (string, string) {
	decision, reason, by := m.decideTool(req)
	if by != decidedByPerson {
		m.logToolCall(req, decision, reason, by)
	}
	return decision, reason
}

// logToolCall keeps a tool call in the session's history and shows it on the page. Requests run
// concurrently, and the sequence of a message has to be read and written as one step.
func (m *Manager) logToolCall(req natspkg.PermissionRequest, decision, reason, by string) {
	input := req.ToolInput
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fields := map[string]any{
		"tool_name": req.ToolName, "tool_input": input, "tool_use_id": req.ToolUseID,
		"timestamp": now, "decision": decision, "decided_by": by, "reason": reason,
	}
	content, _ := json.Marshal(fields)
	m.logMu.Lock()
	seq, _ := dbpkg.NextMessageSeq(m.db, req.SessionID)
	err := dbpkg.InsertMessage(m.db, dbpkg.Message{ID: id, SessionID: req.SessionID, Role: "tool_execution", Content: string(content), Seq: seq})
	m.logMu.Unlock()
	if err != nil {
		log.Printf("[manager] log tool call sess=%s tool=%s: %v", req.SessionID, req.ToolName, err)
	}
	fields["type"] = "tool_execution"
	fields["session_id"] = req.SessionID
	fields["message_id"] = id
	m.bus.BroadcastToRole(req.SessionID, natspkg.RoleClient, fields)
}

// decideTool answers a tool call and says who decided, in this order:
//
//  1. Tools of lucode itself, and a turn where something was already denied.
//  2. The session's PermissionStore (lazy-loaded from
//     %USERPROFILE%/.lucode/settings.json + <workdir>/.lucode/settings.json
//     + the session_permissions table). Allow and Deny are final, and so is an
//     explicit Ask, which goes straight to a person.
//  3. The ToolGate (a project's flow), which can allow it, deny it or leave it open.
//  4. What is left open goes to claude's auto mode if it runs in it. Otherwise to a person:
//     record the request in pendingApprovals, call onHookRequest to broadcast hook_pending
//     to the WS, and block until onHookRespond signals us back.
//
// The decision is allow, deny or auto. The reason comes from the matched rule, the flow or the
// user's response.
func (m *Manager) decideTool(req natspkg.PermissionRequest) (string, string, string) {
	askedAt := time.Now().UTC()
	sessID, toolUseID, toolName, toolInput := req.SessionID, req.ToolUseID, req.ToolName, req.ToolInput
	autoMode := req.PermissionMode == "auto"

	// The task tools only report on the agent's own task; they never need approval
	// (and must stay usable after a denial so the agent can report it as blocked).
	if taskmcp.IsTaskTool(toolName) {
		return "allow", "lucode task tool", decidedByLucode
	}
	// The flow tools only read flows and write drafts, which run nothing until a person publishes
	// them; asking on each edit would rule out working on a flow together, live.
	if taskmcp.IsFlowTool(toolName) {
		return "allow", "only writes unpublished drafts", decidedByLucode
	}
	// ToolSearch only loads the schemas of deferred tools (how claude finds the task tools).
	if toolName == "ToolSearch" {
		return "allow", "loads tool schemas only", decidedByLucode
	}

	// If a tool was already denied this turn, stop immediately and instruct
	// the model to inform the user rather than attempting more tools.
	m.pendingApprovalsMu.Lock()
	alreadyDenied := m.deniedThisTurn[sessID]
	m.pendingApprovalsMu.Unlock()
	if alreadyDenied {
		return "deny", "A tool call was already denied during this turn. Stop executing tools, explain to the user what you were attempting, and ask how they would like to proceed.", decidedByLucode
	}

	store, err := permissions.LoadForSession(m.workDirFor(sessID), permDBAdapter{m.db}, sessID)
	if err != nil {
		log.Printf("[manager] permission store load failed sess=%s: %v", sessID[:8], err)
		m.markDeniedThisTurn(sessID)
		return "deny", "permission store unavailable", decidedByLucode
	}

	match, err := store.Match(toolName, toolInput)
	if err != nil {
		log.Printf("[manager] permission match error sess=%s tool=%s: %v", sessID[:8], toolName, err)
		m.markDeniedThisTurn(sessID)
		return "deny", "permission match error: " + err.Error(), decidedByLucode
	}
	switch match.Decision {
	case permissions.Allow:
		return "allow", ruleReason(match), decidedByRule
	case permissions.Deny:
		m.markDeniedThisTurn(sessID)
		return "deny", ruleReason(match), decidedByRule
	}

	// No rule decided: a flow of the project may. An explicit Ask rule is not left open: it says
	// a person answers this, also in auto mode, the way claude treats its own ask rules.
	if match.Decision != permissions.Ask {
		if m.ToolGate != nil {
			decision, reason := m.ToolGate(ToolRequest{SessionID: sessID, ToolUseID: toolUseID, ToolName: toolName, WorkDir: req.WorkDir, AutoMode: autoMode, Input: toolInput})
			switch decision {
			case "allow":
				return decision, reason, decidedByFlow
			case "deny":
				m.markDeniedThisTurn(sessID)
				return decision, reason, decidedByFlow
			}
		}
		if autoMode {
			return "auto", "", decidedByAuto
		}
	}

	// Ask / Miss: ask the user. Register the wait channel first so we
	// don't race with the respond.
	ch := m.registerApproval(sessID, toolUseID)
	defer m.unregisterApproval(sessID, toolUseID)

	m.onHookRequest(sessID, toolUseID, toolName, toolInput, askedAt)

	timer := time.NewTimer(time.Until(askedAt.Add(approvalTimeout - approvalMargin)))
	defer timer.Stop()
	select {
	case resp := <-ch:
		// Normalize UI decisions ('approve', 'allow_always', 'allow_with_rule')
		// to the gate protocol values ('allow' / 'deny').
		decision := resp.decision
		switch decision {
		case "approve", "allow_always", "allow_with_rule":
			decision = "allow"
		case "deny", "deny_always":
			decision = "deny"
		}
		if decision == "deny" {
			m.markDeniedThisTurn(sessID)
		}
		return decision, resp.reason, decidedByPerson
	case <-timer.C:
		m.markDeniedThisTurn(sessID)
		m.expireApproval(sessID, toolUseID)
		return "deny", "user did not respond", decidedByPerson
	}
}

func (m *Manager) markDeniedThisTurn(sessID string) {
	m.pendingApprovalsMu.Lock()
	m.deniedThisTurn[sessID] = true
	m.pendingApprovalsMu.Unlock()
}

func ruleReason(m permissions.MatchResult) string {
	if m.Reason != "" {
		return m.Reason
	}
	if m.Rule != nil && m.Rule.Tool != "" {
		return fmt.Sprintf("matched rule for %s", m.Rule.Tool)
	}
	return ""
}

// workDirFor returns the cached work_dir for a session, falling back
// to the first work_dir in the active turn if any.
func (m *Manager) workDirFor(sessID string) string {
	m.pendingApprovalsMu.Lock()
	defer m.pendingApprovalsMu.Unlock()
	if wd, ok := m.workDirsBySession[sessID]; ok {
		return wd
	}
	return ""
}

// SetWorkDir records a session's work_dir for permission lookups.
// Called by the turn-start path so the gate can resolve settings.
func (m *Manager) SetWorkDir(sessID, workDir string) {
	m.pendingApprovalsMu.Lock()
	defer m.pendingApprovalsMu.Unlock()
	m.workDirsBySession[sessID] = workDir
}

func (m *Manager) registerApproval(sessID, toolUseID string) chan hookResponse {
	ch := make(chan hookResponse, 1)
	m.pendingApprovalsMu.Lock()
	m.pendingApprovals[pendingKey(sessID, toolUseID)] = ch
	m.pendingApprovalsMu.Unlock()
	return ch
}

func (m *Manager) unregisterApproval(sessID, toolUseID string) {
	m.pendingApprovalsMu.Lock()
	delete(m.pendingApprovals, pendingKey(sessID, toolUseID))
	m.pendingApprovalsMu.Unlock()
}

func (m *Manager) signalApproval(sessID, toolUseID string, resp hookResponse) {
	m.pendingApprovalsMu.Lock()
	ch, ok := m.pendingApprovals[pendingKey(sessID, toolUseID)]
	m.pendingApprovalsMu.Unlock()
	if ok {
		// Non-blocking send: the channel has buffer 1.
		select {
		case ch <- resp:
		default:
		}
	}
}

func pendingKey(sessID, toolUseID string) string { return sessID + ":" + toolUseID }

// permDBAdapter wraps the *sql.DB and exposes the SessionStore methods
// that internal/permissions needs. The actual SQL lives in
// internal/db/queries.go; this adapter is the bridge.
type permDBAdapter struct{ db *sql.DB }

func (a permDBAdapter) InsertSessionRule(rule permissions.SessionRuleRow) error {
	return dbpkg.InsertSessionRule(a.db, dbpkg.SessionRuleRow{
		ID:         rule.ID,
		SessionID:  rule.SessionID,
		Tool:       rule.Tool,
		InputRegex: rule.InputRegex,
		Scope:      rule.Scope,
		CreatedAt:  rule.CreatedAt,
	})
}

func (a permDBAdapter) ListSessionRules(sessID string) ([]permissions.SessionRuleRow, error) {
	rows, err := dbpkg.ListSessionRules(a.db, sessID)
	if err != nil {
		return nil, err
	}
	out := make([]permissions.SessionRuleRow, len(rows))
	for i, r := range rows {
		out[i] = permissions.SessionRuleRow{
			ID:         r.ID,
			SessionID:  r.SessionID,
			Tool:       r.Tool,
			InputRegex: r.InputRegex,
			Scope:      r.Scope,
			CreatedAt:  r.CreatedAt,
		}
	}
	return out, nil
}

func (a permDBAdapter) DeleteSessionRule(sessID, ruleID string) error {
	return dbpkg.DeleteSessionRule(a.db, sessID, ruleID)
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


// finishTurn releases the session after a turn: cancels its context, frees the
// busy marker and lets OnTurnEnd deliver anything queued meanwhile.
func (m *Manager) finishTurn(sessID string, ps *ProcessState) {
	ps.Cancel()
	close(ps.Done)
	m.mu.Lock()
	if m.procs[sessID] == ps {
		delete(m.procs, sessID)
	}
	m.mu.Unlock()
	if m.OnTurnEnd != nil {
		go m.OnTurnEnd(sessID)
	}
}
