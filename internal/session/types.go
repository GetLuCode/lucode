package session

import "context"

type Status string

const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusWaitingHook Status = "waiting_hook"
	StatusError       Status = "error"
)

// ProcessState tracks an active turn for busy detection and cancellation.
type ProcessState struct {
	SessionID string
	Done      chan struct{}
	Cancel    context.CancelFunc
}

// Provider selects the AI backend for a turn.
type Provider string

const (
	ProviderClaude    Provider = "claude"    // claude CLI via aiapi
	ProviderOllama    Provider = "ollama"    // Ollama API via aiapi
	ProviderAnthropic Provider = "anthropic" // Anthropic Messages API via aiapi
)

type TurnOptions struct {
	SessionID       string
	ProjectID       string
	Message         string
	Model           string
	Name            string
	WorkDirs        []string
	ClaudeConfigDir string
	ClaudeSettings  string
	Resume          bool
	ForkFrom        string
	Effort          string
	Thinking        string
	ImageBase64     string
	ImageMediaType  string

	Provider               Provider
	OllamaHost             string
	AccountClaudeConfigDir string
	AccountID              string
	TurnStartSeq           int
}
