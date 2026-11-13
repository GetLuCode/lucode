// Package executor runs AI turns against MiniMax. Stateless HTTP — no
// persistent processes, no per-session state. The conversation is replayed
// from the caller on every turn via TurnRequest.History.
package executor

import "encoding/json"

// TurnRequest is the unit of work passed to Run. Kept JSON-serializable for
// the HTTP and NATS bridges.
type TurnRequest struct {
	SessionID string   `json:"session_id"`
	Message   string   `json:"message"`
	Model     string   `json:"model"`
	WorkDirs  []string `json:"work_dirs,omitempty"`

	// Fork from an existing session id (resume by replaying history).
	Resume   bool   `json:"resume,omitempty"`
	ForkFrom string `json:"fork_from,omitempty"`
	Name     string `json:"name,omitempty"`

	// Multimodal (OpenAI image_url content part).
	ImageBase64    string `json:"image_base64,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`

	// Thinking mode: "adaptive" or "disabled". Only honoured for MiniMax-M3;
	// M2.7 always runs with thinking on regardless of this field.
	Thinking string `json:"thinking,omitempty"`

	// Tools lists the agent's allowed built-in tools by Claude Code name (Read,
	// Edit, Bash, ...). Empty means no restriction. See toolsForRequest.
	Tools []string `json:"tools,omitempty"`

	// LucodeTools lists which lucode tools (task_*, agent_list, send_to_agent,
	// task_info) the agent has. nil means all of them; an empty list means none.
	LucodeTools []string `json:"lucode_tools,omitempty"`

	// SystemPrompt is the agent's personality/instructions, added to the
	// system blocks after the runtime rules.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Conversation history replayed before the current user message.
	History []HistoryMessage `json:"history,omitempty"`
}

// HistoryMessage is one prior turn to replay into the conversation.
type HistoryMessage struct {
	Role           string `json:"role"`
	Content        string `json:"content"`
	ImageBase64    string `json:"image_base64,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`
}

// JSONQuote returns s as a JSON-encoded string literal, suitable for
// assigning to a ChatMessage.Content (json.RawMessage) field.
func JSONQuote(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// ChatMessage is one message in the conversation. Content uses json.RawMessage
// so the same type can carry a plain string (text-only messages) or a JSON
// array of content parts (text + image_url for multimodal user messages).
// Use textContent(msg.Content) to extract the plain string when needed.
type ChatMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

// Usage is the token-usage block. Anthropic's Messages API returns
// input_tokens / output_tokens; we also track cache hits and reasoning tokens
// when the model emits thinking.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheRead        int `json:"cache_read_input_tokens,omitempty"`
	CacheWrite       int `json:"cache_creation_input_tokens,omitempty"`
}

// UnmarshalJSON accepts both OpenAI-style (prompt_tokens/completion_tokens) and
// Anthropic-style (input_tokens/output_tokens) usage objects.
func (u *Usage) UnmarshalJSON(b []byte) error {
	type plain Usage
	aux := struct {
		plain
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}{}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*u = Usage(aux.plain)
	if u.PromptTokens == 0 {
		u.PromptTokens = aux.InputTokens
	}
	if u.CompletionTokens == 0 {
		u.CompletionTokens = aux.OutputTokens
	}
	return nil
}

// StreamChunk is one SSE `data: {...}` payload from the API. The UI's
// stream_event handler can dump it verbatim for debug. Shape mirrors
// Anthropic's Messages streaming events so the existing UI handler
// (built originally for Claude Code) keeps working.
type StreamChunk struct {
	Type         string          `json:"type,omitempty"`
	Message      *AnthropicMsg   `json:"message,omitempty"`
	Index        int             `json:"index,omitempty"`
	ContentBlock *AnthropicBlock `json:"content_block,omitempty"`
	Delta        *AnthropicDelta `json:"delta,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	Error        *AnthropicErr   `json:"error,omitempty"`
	StopReason   string          `json:"stop_reason,omitempty"`
}

// AnthropicMsg is the assistant message snapshot at message_start / message_delta.
type AnthropicMsg struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Model      string           `json:"model"`
	StopReason string           `json:"stop_reason,omitempty"`
	Usage      *Usage           `json:"usage,omitempty"`
	Content    []AnthropicBlock `json:"content,omitempty"`
}

// AnthropicBlock is one content block. Type is "text", "thinking", "tool_use", or "tool_result".
type AnthropicBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Think string          `json:"thinking,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// AnthropicDelta is the incremental update on content_block_delta and message_delta events.
// For content_block_delta: Type is "text_delta" / "thinking_delta" / "input_json_delta".
// For message_delta: Type is empty, StopReason is set, and Usage may be present.
type AnthropicDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// AnthropicErr is the structured error block in an SSE error event.
type AnthropicErr struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
