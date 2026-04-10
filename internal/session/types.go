package session

import (
	"context"
	"os/exec"
)

type Status string

const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusWaitingHook Status = "waiting_hook"
	StatusError       Status = "error"
)

type ProcessState struct {
	SessionID string
	Cmd       *exec.Cmd
	Done      chan struct{}
	Cancel    context.CancelFunc
}

// Provider selects the AI backend for a turn.
type Provider string

const (
	ProviderClaude     Provider = "claude"    // claude CLI (default)
	ProviderOllama     Provider = "ollama"    // local Ollama API
	ProviderAnthropic  Provider = "anthropic" // direct Messages API via OAuth
)

type TurnOptions struct {
	SessionID       string
	ProjectID       string
	Message         string
	Model           string
	Name            string
	WorkDirs        []string
	ClaudeConfigDir string
	ClaudeSettings  string // path to --settings file passed to claude CLI
	Resume          bool   // true → --resume=SessionID
	ForkFrom        string // non-empty → --fork-session --resume=ForkFrom --session-id=SessionID
	Effort          string // low|medium|high|max|auto
	Thinking        string // enabled|disabled|adaptive
	ImageBase64     string // base64-encoded image
	ImageMediaType  string // image/jpeg, image/png, etc.

	// Provider selects the AI backend. Defaults to ProviderClaude when empty.
	Provider Provider
	// OllamaHost is the base URL of the Ollama API (default: http://localhost:11434).
	OllamaHost string
	// AccountClaudeConfigDir is the resolved claude config dir from a registered account.
	// Used by ProviderAnthropic to locate .credentials.json.
	AccountClaudeConfigDir string
}
