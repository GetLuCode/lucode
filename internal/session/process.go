package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

type stdinInput struct {
	Type    string   `json:"type"`
	Message stdinMsg `json:"message"`
}

type stdinMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []map[string]interface{} for content with images
}

// spawnResult holds the started process and its stdout reader.
type spawnResult struct {
	ps     *ProcessState
	stdout io.ReadCloser
}

// spawnClaude creates and starts the claude CLI process.
// Stdout pipe is created before Start; user message is written then stdin is closed.
func spawnClaude(ctx context.Context, opts TurnOptions, backendWSURL string) (*spawnResult, error) {
	claudePath, err := findClaude()
	if err != nil {
		return nil, err
	}

	args := buildArgs(opts)
	log.Printf("[spawn] session=%s claude %s", opts.SessionID[:8], strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Env = buildEnv(opts, backendWSURL)

	// Set working directory to the first work_dir (primary project dir)
	if len(opts.WorkDirs) > 0 {
		cmd.Dir = opts.WorkDirs[0]
		log.Printf("[spawn] session=%s cwd=%s", opts.SessionID[:8], cmd.Dir)
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

	// Capture stderr for logging
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
		return nil, fmt.Errorf("start claude: %w", err)
	}

	log.Printf("[spawn] session=%s pid=%d started", opts.SessionID[:8], cmd.Process.Pid)

	// Stream stderr to log in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				log.Printf("[stderr] session=%s: %s", opts.SessionID[:8], strings.TrimSpace(string(buf[:n])))
			}
			if err != nil {
				break
			}
		}
	}()

	// Write user message as stream-json, then close stdin
	// Build content: text only or text + image
	var content interface{} = opts.Message
	if opts.ImageBase64 != "" {
		content = []map[string]interface{}{
			{
				"type": "text",
				"text": opts.Message,
			},
			{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": opts.ImageMediaType,
					"data":       opts.ImageBase64,
				},
			},
		}
	}

	msg := stdinInput{
		Type: "user",
		Message: stdinMsg{
			Role:    "user",
			Content: content,
		},
	}
	data, _ := json.Marshal(msg)
	log.Printf("[stdin] session=%s: %s", opts.SessionID[:8], string(data))
	fmt.Fprintf(stdinPipe, "%s\n", data)
	stdinPipe.Close()

	ps := &ProcessState{
		SessionID: opts.SessionID,
		Cmd:       cmd,
		Done:      make(chan struct{}),
	}
	return &spawnResult{ps: ps, stdout: stdoutPipe}, nil
}

func buildArgs(opts TurnOptions) []string {
	args := []string{
		"-p",
		"--verbose",
		"--output-format=stream-json",
		"--include-hook-events",
		"--include-partial-messages",
		"--input-format=stream-json",
		"--permission-mode=bypassPermissions",
		"--model=" + opts.Model,
	}

	switch {
	case opts.ForkFrom != "":
		args = append(args,
			"--fork-session",
			"--resume="+opts.ForkFrom,
			"--session-id="+opts.SessionID,
		)
	case opts.Resume:
		args = append(args, "--resume="+opts.SessionID)
	default:
		args = append(args, "--session-id="+opts.SessionID)
		if opts.Name != "" {
			args = append(args, "--name="+opts.Name)
		}
		// First dir is set as cmd.Dir (cwd); extra dirs added via --add-dir
		for _, d := range opts.WorkDirs[1:] {
			args = append(args, "--add-dir="+d)
		}
	}

	// Add effort and thinking flags if provided
	if opts.Effort != "" && opts.Effort != "auto" {
		args = append(args, "--effort="+opts.Effort)
	}
	if opts.Thinking != "" && opts.Thinking != "adaptive" {
		args = append(args, "--thinking="+opts.Thinking)
	}

	return args
}

func buildEnv(opts TurnOptions, backendWSURL string) []string {
	env := append(os.Environ(),
		"LUCODE_SESSION_ID="+opts.SessionID,
		"LUCODE_WS_URL="+backendWSURL,
	)
	if opts.ClaudeConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+opts.ClaudeConfigDir)
	}
	return env
}

func findClaude() (string, error) {
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
