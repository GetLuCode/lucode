package session

import (
	"context"

	dbpkg "claude-orchestrator/internal/db"
)

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
	SessionID string
	ProjectID string
	Message   string
	Model     string
	Name      string
	WorkDirs  []string
	ClaudeConfigDir string
	Resume    bool
	ForkFrom  string
	Effort    string
	Thinking  string
	ImageBase64    string
	ImageMediaType string

	Provider               Provider
	OllamaHost             string
	AccountClaudeConfigDir string // kept for ABI; ignored by aiapi
	AccountID              string // kept for ABI; ignored by aiapi
	TurnStartSeq           int    // kept for ABI; ignored by aiapi

	// History replays the prior conversation. aiapi re-injects these before
	// the new user message so the model has context.
	History []dbpkg.Message

	// AIAPIInstanceID, when non-empty, routes this turn to a remote aiapi via NATS.
	AIAPIInstanceID string

	// Agent-driven turns (task sessions). Executor selects how the agent runs:
	// "claude" spawns `claude -p` locally, anything else goes through aiapi.
	Executor     string
	SystemPrompt string // agent personality, appended to the system prompt
	AgentName    string
	Tools        []string // built-in tools the agent may use (claude names); empty = all
	Persistent   bool     // keep the claude process alive between turns (Executor "claude" only)
	LucodeTools  []string // lucode tools the agent has (task_*, agent_list, ...); nil = all, empty = none
}
