package session

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	execpkg "claude-orchestrator/internal/executor"

	"github.com/google/uuid"
)

// InstallHookSettings writes the embedded lucodehook binary to ~/.lucode and a
// claude settings file that registers it as the PreToolUse hook (headless
// `claude -p` has no permission dialog, so PermissionRequest never fires). It
// returns the settings path to pass to `claude --settings`, or "" on failure.
func InstallHookSettings(hookBinary []byte) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".lucode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := "lucodehook"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	hookPath := filepath.Join(dir, name)
	if len(hookBinary) > 0 {
		if err := os.WriteFile(hookPath, hookBinary, 0o755); err != nil {
			log.Printf("[cli] write hook binary: %v", err)
		}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "",
				// claude runs hook commands through a bash-style shell on Windows,
				// which swallows backslashes, so the path must use forward slashes.
				"hooks": []any{map[string]any{"type": "command", "command": filepath.ToSlash(hookPath)}},
			}},
		},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	settingsPath := filepath.Join(dir, "claude-hooks.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		log.Printf("[cli] write hook settings: %v", err)
		return ""
	}
	return settingsPath
}

// permissionModeOf is the claude --permission-mode of a turn ("" = the default
// flow, where every tool call goes through lucode's approval hook).
func permissionModeOf(opts TurnOptions) string {
	if opts.AutoMode {
		return "auto"
	}
	return ""
}

// writeTaskMCPConfig writes the MCP config that starts `lucode mcp-task` for
// this session, giving the agent the task and teammate tools. Returns "" when it
// cannot be written (the agent then just has no lucode tools).
func (m *Manager) writeTaskMCPConfig(sessID string, lucodeTools []string) string {
	if lucodeTools != nil && len(lucodeTools) == 0 {
		return "" // the agent has no lucode tools: do not start the MCP server at all
	}
	exe, err := os.Executable()
	home, herr := os.UserHomeDir()
	if err != nil || herr != nil {
		return ""
	}
	dir := filepath.Join(home, ".lucode", "mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	env := map[string]string{
		"LUCODE_SESSION_ID": sessID,
		"LUCODE_NATS_URL":   m.backendNATSURL,
	}
	if m.natsCreds != "" {
		env["LUCODE_NATS_CREDS"] = m.natsCreds
	}
	if lucodeTools != nil {
		env["LUCODE_TOOLS"] = strings.Join(lucodeTools, ",")
	}
	cfg := map[string]any{"mcpServers": map[string]any{"lucode": map[string]any{
		"command": filepath.ToSlash(exe),
		"args":    []string{"mcp-task"},
		"env":     env,
	}}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	path := filepath.Join(dir, sessID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("[cli] write mcp config: %v", err)
		return ""
	}
	return path
}

// runWithCLI runs a turn with the local `claude -p` CLI (the paperclip-style
// executor). Tool approvals go through lucodehook → NATS → the same permission
// gate aiapi uses, so the UI shows them identically.
//
// By default every turn is its own process (--resume carries the conversation).
// With opts.Persistent the process stays alive between turns and later messages
// are written to its stdin.
func (m *Manager) runWithCLI(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)

	if len(opts.WorkDirs) > 0 {
		m.SetWorkDir(sessID, opts.WorkDirs[0])
	}

	req := execpkg.TurnRequest{
		MCPConfig:          m.writeTaskMCPConfig(sessID, opts.LucodeTools),
		SessionID:          sessID,
		ProjectID:          opts.ProjectID,
		AccountID:          opts.AccountID,
		Message:            opts.Message,
		Model:              opts.Model,
		Provider:           execpkg.ProviderClaude,
		WorkDirs:           opts.WorkDirs,
		Resume:             opts.Resume,
		Name:               opts.AgentName,
		Effort:             opts.Effort,
		Thinking:           opts.Thinking,
		AppendSystemPrompt: opts.SystemPrompt,
		Tools:              opts.Tools,
		ClaudeConfigDir:    opts.ClaudeConfigDir,
		ClaudeSettings:     m.claudeSettings,
		PermissionMode:     permissionModeOf(opts),
		Worktree:           opts.Worktree,
		WorktreeBaseRef:    opts.WorktreeBaseRef,
		NATSSettings:       m.backendNATSURL,
		NATSCreds:          m.natsCreds,
		ImageBase64:        opts.ImageBase64,
		ImageMediaType:     opts.ImageMediaType,
	}

	publish := m.cliPublisher(sessID, opts)
	if opts.Persistent {
		m.runLiveTurn(ctx, req, opts, publish)
		return
	}
	execpkg.Run(ctx, req, publish)
}

// runLiveTurn sends the message to the session's live process, starting one if
// there is none, and returns when the turn ends (or ctx is cancelled).
func (m *Manager) runLiveTurn(ctx context.Context, req execpkg.TurnRequest, opts TurnOptions, publish func(map[string]any)) {
	sessID := req.SessionID

	var live *execpkg.Live
	var done <-chan struct{}
	if l := m.getLive(sessID); l != nil {
		d, err := l.Send(opts.Message, opts.ImageBase64, opts.ImageMediaType)
		if err == nil {
			live, done = l, d
		} else {
			log.Printf("[cli] live process for %s unusable (%v); starting a new one", sessID[:8], err)
			m.dropLive(sessID, l)
			l.Kill()
		}
	}
	if live == nil {
		var started *execpkg.Live
		l, d, err := execpkg.StartLive(req, publish, func() { m.dropLive(sessID, started) })
		if err != nil {
			publish(map[string]any{"type": "error", "session_id": sessID, "message": err.Error()})
			publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
			return
		}
		started, live, done = l, l, d
		m.setLive(sessID, l)
	}

	select {
	case <-done:
	case <-ctx.Done(): // turn cancelled by the user: interrupt it, keep the process
		live.Stop(done)
	}
}

// ---- live process registry ----

func (m *Manager) getLive(sessID string) *execpkg.Live {
	m.liveMu.Lock()
	l := m.lives[sessID]
	dead := l != nil && !l.Alive()
	if dead {
		delete(m.lives, sessID)
	}
	m.liveMu.Unlock()
	if dead {
		m.emitProcess(sessID) // it exited on its own (idle timeout or crash)
		return nil
	}
	return l
}

func (m *Manager) setLive(sessID string, l *execpkg.Live) {
	m.liveMu.Lock()
	m.lives[sessID] = l
	m.liveMu.Unlock()
	m.emitProcess(sessID)
}

// dropLive forgets l if it is still the registered process for the session.
func (m *Manager) dropLive(sessID string, l *execpkg.Live) {
	if l == nil {
		return
	}
	m.liveMu.Lock()
	dropped := m.lives[sessID] == l
	if dropped {
		delete(m.lives, sessID)
	}
	m.liveMu.Unlock()
	if dropped {
		m.emitProcess(sessID)
	}
}

// CloseLive gracefully stops a session's live process, if it has one. The next
// message starts a fresh process with --resume.
func (m *Manager) CloseLive(sessID string) {
	m.liveMu.Lock()
	l := m.lives[sessID]
	delete(m.lives, sessID)
	runner := m.remoteRunnerOf[sessID]
	delete(m.remoteRunnerOf, sessID)
	delete(m.remotePID, sessID)
	m.liveMu.Unlock()
	if runner != "" {
		m.bus.RunnerClose(runner, sessID) // the process lives on that runner
	}
	if l != nil {
		l.Close()
	}
	m.emitProcess(sessID)
}

// cliPublisher returns the event sink for a claude process: it persists
// messages, usage and status, and relays everything to the browsers. It is
// shared by one-shot runs and live processes (which keep calling it across turns).
func (m *Manager) cliPublisher(sessID string, opts TurnOptions) func(map[string]any) {
	return func(ev map[string]any) {
		evType, _ := ev["type"].(string)
		switch evType {
		case "message_ready":
			role, _ := ev["role"].(string)
			content, _ := ev["content"].(string)
			if role != "" && content != "" {
				seq, _ := dbpkg.NextMessageSeq(m.db, sessID)
				if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
					ID: uuid.New().String(), SessionID: sessID, Role: role, Content: content, Seq: seq,
				}); err != nil {
					log.Printf("[cli] message insert sess=%s: %v", sessID[:8], err)
				}
			}
			// The frontend reads message_ready as {message:{role,content}}.
			ev = map[string]any{
				"type":       "message_ready",
				"session_id": sessID,
				"message":    map[string]any{"role": role, "content": content},
			}
		case "claude_workspace":
			m.liveMu.Lock()
			if m.workspaces == nil {
				m.workspaces = map[string]map[string]any{}
			}
			m.workspaces[sessID] = ev
			m.liveMu.Unlock()
		case "session_status":
			if status, _ := ev["status"].(string); status != "" {
				dbpkg.UpdateSessionStatus(m.db, sessID, status)
			}
		case "error":
			if code, _ := ev["code"].(string); code != "" && m.OnTurnFailed != nil {
				msg, _ := ev["message"].(string)
				go m.OnTurnFailed(sessID, code, msg)
			}
		case "turn_usage":
			tu := dbpkg.TurnUsage{
				ID: uuid.New().String(), SessionID: sessID, ProjectID: opts.ProjectID,
				AccountID: opts.AccountID, Model: opts.Model,
			}
			num := func(k string) int {
				switch v := ev[k].(type) {
				case int:
					return v
				case float64:
					return int(v)
				}
				return 0
			}
			tu.InputTokens = num("input_tokens")
			tu.OutputTokens = num("output_tokens")
			tu.CacheCreationInputTokens = num("cache_creation_input_tokens")
			tu.CacheReadInputTokens = num("cache_read_input_tokens")
			if err := dbpkg.InsertTurnUsage(m.db, tu); err != nil {
				log.Printf("[cli] turn_usage insert sess=%s: %v", sessID[:8], err)
			}
		case "context_usage":
			if b, err := json.Marshal(ev); err == nil {
				_ = dbpkg.UpdateSessionContextUsage(m.db, sessID, string(b))
			}
		}
		m.broadcastAndPersist(sessID, ev)
	}
}
