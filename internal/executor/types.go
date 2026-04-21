// Package executor handles AI backend communication (Claude CLI and Ollama).
// It has no dependency on the database layer; all results are delivered
// through a typed publish callback.
package executor

// Provider selects the AI backend for a turn.
type Provider string

const (
	ProviderClaude  Provider = "claude"  // claude CLI (default)
	ProviderOllama  Provider = "ollama"  // local Ollama API
)

// TurnRequest is the unit of work: everything needed to run an AI turn.
type TurnRequest struct {
	SessionID   string   `json:"session_id"`
	ProjectID   string   `json:"project_id"`
	AccountID   string   `json:"account_id"`
	Message     string   `json:"message"`
	Model       string   `json:"model"`
	Provider    Provider `json:"provider"`
	WorkDirs    []string `json:"work_dirs"`
	Resume      bool     `json:"resume"`
	ForkFrom    string   `json:"fork_from,omitempty"`
	Name        string   `json:"name,omitempty"`
	Effort      string   `json:"effort,omitempty"`
	Thinking    string   `json:"thinking,omitempty"`

	ClaudeConfigDir string `json:"claude_config_dir,omitempty"`
	ClaudeSettings  string `json:"claude_settings,omitempty"` // --settings path
	NATSSettings    string `json:"nats_settings,omitempty"`   // injected as LUCODE_NATS_URL

	ImageBase64    string `json:"image_base64,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`

	OllamaHost string `json:"ollama_host,omitempty"`

	// History is required for Ollama (claude CLI manages its own session history).
	History []HistoryMessage `json:"history,omitempty"`

	// TurnStartSeq is the seq of the user message that started this turn.
	// Propagated in message_ready events so the relay can compute correct seqs.
	TurnStartSeq int `json:"turn_start_seq,omitempty"`
}

// HistoryMessage is a simplified message used for provider history replay.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
