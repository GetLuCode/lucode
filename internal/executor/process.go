package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ProcessState tracks a running claude CLI process.
type ProcessState struct {
	SessionID string
	Cmd       *exec.Cmd
	Done      chan struct{}
	Cancel    context.CancelFunc
	StdinPipe io.WriteCloser
	StdinMu   sync.Mutex
}

type spawnResult struct {
	ps     *ProcessState
	stdout io.ReadCloser
	req    TurnRequest
}

func spawnClaude(ctx context.Context, req TurnRequest, publish func(map[string]any)) (*spawnResult, error) {
	claudePath, err := findClaude()
	if err != nil {
		return nil, err
	}

	args := buildArgs(req)
	log.Printf("[executor] session=%s claude %s", req.SessionID[:8], strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Env = buildEnv(req)

	if len(req.WorkDirs) > 0 {
		cmd.Dir = req.WorkDirs[0]
		log.Printf("[executor] session=%s cwd=%s", req.SessionID[:8], cmd.Dir)
	}
	// tell the hub what was launched, whether or not the process starts
	announce := func(pid int, err error) {
		if publish != nil {
			publish(CommandEvent(req.SessionID, claudePath, args, cmd.Dir, pid, err))
		}
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdinPipe.Close()
		stdoutPipe.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		stdoutPipe.Close()
		stderrPipe.Close()
		announce(0, err)
		return nil, fmt.Errorf("start claude: %w", err)
	}

	log.Printf("[executor] session=%s pid=%d started", req.SessionID[:8], cmd.Process.Pid)
	announce(cmd.Process.Pid, nil)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				log.Printf("[executor/stderr] session=%s: %s", req.SessionID[:8], strings.TrimSpace(string(buf[:n])))
			}
			if err != nil {
				break
			}
		}
	}()

	// Write user message as stream-json to stdin.
	fmt.Fprintf(stdinPipe, "%s\n", userMessageLine(req.Message, req.ImageBase64, req.ImageMediaType))
	// stdin stays open for control_request writes; closed in the stream loop defer.

	ps := &ProcessState{
		SessionID: req.SessionID,
		Cmd:       cmd,
		Done:      make(chan struct{}),
		StdinPipe: stdinPipe,
	}
	return &spawnResult{ps: ps, stdout: stdoutPipe, req: req}, nil
}

// userMessageLine is a user message in claude's stream-json input format.
func userMessageLine(text, imageBase64, imageMediaType string) []byte {
	var content interface{} = text
	if imageBase64 != "" {
		content = []map[string]interface{}{
			{"type": "text", "text": text},
			{"type": "image", "source": map[string]interface{}{
				"type":       "base64",
				"media_type": imageMediaType,
				"data":       imageBase64,
			}},
		}
	}
	data, _ := json.Marshal(map[string]interface{}{
		"type":    "user",
		"message": map[string]interface{}{"role": "user", "content": content},
	})
	return data
}

// SendControlRequest writes an SDK control_request line to the process stdin.
func SendControlRequest(ps *ProcessState, subtype string) error {
	msg := map[string]interface{}{
		"type":       "control_request",
		"request_id": "cr-" + subtype,
		"request":    map[string]string{"subtype": subtype},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ps.StdinMu.Lock()
	defer ps.StdinMu.Unlock()
	if ps.StdinPipe == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err = fmt.Fprintf(ps.StdinPipe, "%s\n", data)
	return err
}

func buildArgs(req TurnRequest) []string {
	args := []string{
		"-p", "--verbose",
		"--output-format=stream-json",
		"--include-hook-events",
		"--include-partial-messages",
		"--input-format=stream-json",
		"--model=" + req.Model,
	}

	switch {
	case req.ForkFrom != "":
		args = append(args, "--fork-session", "--resume="+req.ForkFrom, "--session-id="+req.SessionID)
	case req.Resume:
		args = append(args, "--resume="+req.SessionID)
	default:
		args = append(args, "--session-id="+req.SessionID)
		if req.Name != "" {
			args = append(args, "--name="+req.Name)
		}
		if len(req.WorkDirs) > 1 {
			for _, d := range req.WorkDirs[1:] {
				args = append(args, "--add-dir="+d)
			}
		}
	}

	if req.Effort != "" && req.Effort != "auto" {
		args = append(args, "--effort="+req.Effort)
	}
	if req.Thinking != "" && req.Thinking != "adaptive" {
		args = append(args, "--thinking="+req.Thinking)
	}
	if req.PermissionMode != "" {
		args = append(args, "--permission-mode="+req.PermissionMode)
	}
	if settings := settingsArg(req); settings != "" {
		args = append(args, "--settings="+settings)
	}
	if req.Worktree {
		args = append(args, "--worktree=lucode-"+shortID(req.SessionID))
	}
	if len(req.Tools) > 0 {
		args = append(args, "--tools="+strings.Join(withToolSearch(req.Tools), ","))
	}
	if req.MCPConfig != "" {
		args = append(args, "--mcp-config="+req.MCPConfig)
	}
	if req.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt="+req.AppendSystemPrompt)
	}

	return args
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// settingsArg is the value of --settings. Normally the path of the lucode hook
// settings (not passed with --permission-mode, where claude decides by itself).
// With a worktree it is a JSON string, because --settings takes only one value:
// the hook settings, if any, plus worktree.baseRef ("head" unless "fresh").
func settingsArg(req TurnRequest) string {
	path := req.ClaudeSettings
	if req.PermissionMode != "" {
		path = ""
	}
	if !req.Worktree {
		return path
	}
	settings := map[string]any{}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &settings)
		}
	}
	baseRef := "head"
	if req.WorktreeBaseRef == "fresh" {
		baseRef = "fresh"
	}
	settings["worktree"] = map[string]any{"baseRef": baseRef}
	data, _ := json.Marshal(settings)
	return string(data)
}

// withToolSearch appends ToolSearch: claude needs it to load the schemas of the
// lucode task tools (MCP tools are deferred), so it must never be filtered out.
func withToolSearch(tools []string) []string {
	out := make([]string, 0, len(tools)+1)
	for _, t := range tools {
		if t != "ToolSearch" {
			out = append(out, t)
		}
	}
	return append(out, "ToolSearch")
}

func buildEnv(req TurnRequest) []string {
	env := append(os.Environ(), "LUCODE_SESSION_ID="+req.SessionID)
	// Agents get only the user's own MCP servers: the claude.ai account connectors
	// (Notion, Gmail, Drive, ...) are off unless the user opts back in by setting
	// ENABLE_CLAUDEAI_MCP_SERVERS in lucode's environment.
	if _, set := os.LookupEnv("ENABLE_CLAUDEAI_MCP_SERVERS"); !set {
		env = append(env, "ENABLE_CLAUDEAI_MCP_SERVERS=false")
	}
	if req.NATSSettings != "" {
		env = append(env, "LUCODE_NATS_URL="+req.NATSSettings)
	}
	if req.NATSCreds != "" {
		env = append(env, "LUCODE_NATS_CREDS="+req.NATSCreds)
	}
	if req.ClaudeConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+req.ClaudeConfigDir)
	}
	return env
}

func findClaude() (string, error) {
	// LUCODE_CLAUDE_BIN replaces the CLI, e.g. with internal/fakeclaude to replay a
	// recorded session without spending tokens.
	if p := os.Getenv("LUCODE_CLAUDE_BIN"); p != "" {
		return p, nil
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	for _, p := range []string{"/usr/local/bin/claude", "/usr/bin/claude"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("claude CLI not found in PATH")
}
