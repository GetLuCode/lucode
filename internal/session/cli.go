package session

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"

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
				// claude runs hook commands through a bash-style shell on Windows, which
				// swallows backslashes, so the path must use forward slashes.
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

// writeTaskMCPConfig writes the MCP config that starts `lucode mcp-task` for
// this session, giving the agent the task_comment/task_done/task_blocked tools.
// Returns "" when it cannot be written (the agent then just has no task tools).
func (m *Manager) writeTaskMCPConfig(sessID string) string {
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
func (m *Manager) runWithCLI(ctx context.Context, ps *ProcessState, opts TurnOptions) {
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

	if len(opts.WorkDirs) > 0 {
		m.SetWorkDir(sessID, opts.WorkDirs[0])
	}

	req := execpkg.TurnRequest{
		MCPConfig:          m.writeTaskMCPConfig(sessID),
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
		NATSSettings:       m.backendNATSURL,
		NATSCreds:          m.natsCreds,
		ImageBase64:        opts.ImageBase64,
		ImageMediaType:     opts.ImageMediaType,
	}

	nextSeq, _ := dbpkg.NextMessageSeq(m.db, sessID)
	execpkg.Run(ctx, req, func(ev map[string]any) {
		evType, _ := ev["type"].(string)
		switch evType {
		case "message_ready":
			role, _ := ev["role"].(string)
			content, _ := ev["content"].(string)
			if role != "" && content != "" {
				if err := dbpkg.InsertMessage(m.db, dbpkg.Message{
					ID: uuid.New().String(), SessionID: sessID, Role: role, Content: content, Seq: nextSeq,
				}); err != nil {
					log.Printf("[cli] message insert sess=%s: %v", sessID[:8], err)
				} else {
					nextSeq++
				}
			}
			// The frontend reads message_ready as {message:{role,content}}.
			ev = map[string]any{
				"type":       "message_ready",
				"session_id": sessID,
				"message":    map[string]any{"role": role, "content": content},
			}
		case "session_status":
			if status, _ := ev["status"].(string); status != "" {
				dbpkg.UpdateSessionStatus(m.db, sessID, status)
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
	})
}
