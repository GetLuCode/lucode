package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

const defaultOllamaHost = "http://localhost:11434"

// ollamaRequest is the payload sent to POST /api/chat.
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

// Images contains raw base64 strings (no data-URI prefix), per Ollama spec.
type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Images     []string         `json:"images,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`  // only set on assistant messages
	ToolCallID string           `json:"tool_call_id,omitempty"` // only set on tool result messages
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	Thinking    *bool   `json:"thinking,omitempty"`
}

// ollamaChunk is one line from the NDJSON streaming response.
type ollamaChunk struct {
	Model   string `json:"model"`
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking,omitempty"` // thinking models emit this field
		Images    []string         `json:"images,omitempty"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason,omitempty"`
}

// ollamaRunHandle holds the HTTP response body and a ProcessState placeholder
// (no real OS process — Ollama runs over HTTP).
type ollamaRunHandle struct {
	ps       *ProcessState
	body     io.ReadCloser
	host     string
	model    string
	options  *ollamaOptions
	messages []ollamaMessage // full conversation up to and including the user turn
	workDir  string
}

// buildOllamaHistory converts stored DB messages for a session into the Ollama
// message format. Only user/assistant roles are included; thinking and hook
// messages are skipped because Ollama doesn't use them.
func buildOllamaHistory(dbMessages []dbpkg.Message) []ollamaMessage {
	var out []ollamaMessage
	for _, m := range dbMessages {
		switch m.Role {
		case "user":
			text, images := extractContentAndImages(m.Content)
			msg := ollamaMessage{Role: "user", Content: text}
			if len(images) > 0 {
				msg.Images = images
			}
			out = append(out, msg)
		case "assistant":
			out = append(out, ollamaMessage{Role: "assistant", Content: m.Content})
		}
	}
	return out
}

// extractContentAndImages decodes a stored message content that may be either
// a plain string or a JSON array of content blocks (text + image).
// Returns the concatenated text and a slice of raw base64 image strings.
func extractContentAndImages(content string) (text string, images []string) {
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		return content, nil
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "image":
			if b.Source.Data != "" {
				images = append(images, b.Source.Data)
			}
		}
	}
	if sb.Len() == 0 {
		return content, images
	}
	return sb.String(), images
}

// buildOllamaOptions maps TurnOptions fields to Ollama-specific options.
// thinking: "enabled" → true, "disabled" → false, anything else → omitted.
func buildOllamaOptions(opts TurnOptions) *ollamaOptions {
	o := &ollamaOptions{}
	hasAny := false

	switch opts.Thinking {
	case "enabled":
		t := true
		o.Thinking = &t
		hasAny = true
	case "disabled":
		f := false
		o.Thinking = &f
		hasAny = true
	}

	if hasAny {
		return o
	}
	return nil
}

// startOllamaRun opens a streaming HTTP connection to the Ollama API and
// returns immediately. The caller is responsible for consuming the body via
// streamOllama.
func startOllamaRun(ctx context.Context, opts TurnOptions, history []dbpkg.Message) (*ollamaRunHandle, error) {
	host := opts.OllamaHost
	if host == "" {
		host = defaultOllamaHost
	}

	// Build conversation: system prompt + prior history + current user message.
	messages := []ollamaMessage{
		{Role: "system", Content: "You are a helpful coding assistant. When you need to read, write, or run code use the provided tools."},
	}
	messages = append(messages, buildOllamaHistory(history)...)
	userMsg := ollamaMessage{Role: "user", Content: opts.Message}
	if opts.ImageBase64 != "" {
		userMsg.Images = []string{opts.ImageBase64}
	}
	messages = append(messages, userMsg)

	workDir := ""
	if len(opts.WorkDirs) > 0 {
		workDir = opts.WorkDirs[0]
	}

	ollamaOpts := buildOllamaOptions(opts)
	body, err := doOllamaChat(ctx, host, opts.Model, messages, ollamaOpts, true)
	if err != nil {
		return nil, err
	}

	ps := &ProcessState{
		SessionID: opts.SessionID,
		Cmd:       nil,
		Done:      make(chan struct{}),
		Cancel:    func() { body.Close() },
	}

	return &ollamaRunHandle{
		ps:       ps,
		body:     body,
		host:     host,
		model:    opts.Model,
		options:  ollamaOpts,
		messages: messages,
		workDir:  workDir,
	}, nil
}

// doOllamaChat sends a chat request to Ollama and returns the streaming body.
// withTools controls whether to include the tool definitions — set to false
// when asking the model to summarise tool results (prevents infinite tool loops).
func doOllamaChat(ctx context.Context, host, model string, messages []ollamaMessage, options *ollamaOptions, withTools bool) (io.ReadCloser, error) {
	payload := ollamaRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
		Options:  options,
	}
	if withTools {
		payload.Tools = lucodeTools
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal: %w", err)
	}
	url := strings.TrimRight(host, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("ollama: build req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// streamOllama drives the full agentic loop for an Ollama turn:
//
//  1. Read the streaming response.
//  2. If the model requests tool calls, execute them locally and feed results
//     back as tool messages, then request another completion (loop).
//  3. When the model produces a final text response, emit it and finish.
//
// emit        — broadcasts WS events to all clients for the session
// persistText — called once with the final assistant text
// workDir     — working directory for tool execution (first WorkDir from opts)
// host        — Ollama base URL
// model       — Ollama model name
// options     — per-request options (thinking, temperature)
func streamOllama(
	handle *ollamaRunHandle,
	sessID string,
	emit func(any),
	persistText func(string),
	askPermission func(toolUseID, toolName string, toolInput json.RawMessage) string,
	workDir, host, model string,
	options *ollamaOptions,
	initialMessages []ollamaMessage,
) {
	defer func() {
		handle.body.Close()
		close(handle.ps.Done)
	}()

	startTime := time.Now()
	sid8 := sessID[:8]

	// messages accumulates the full conversation including tool rounds.
	messages := initialMessages

	// The current response body to read (starts with the one from startOllamaRun).
	currentBody := handle.body

	const maxToolRounds = 10

	for round := 0; round < maxToolRounds; round++ {
		toolCalls, assistantText, err := readOllamaStream(currentBody, sessID, emit)
		if round > 0 {
			currentBody.Close()
		}
		log.Printf("[ollama] session=%s round=%d text_len=%d tool_calls=%d err=%v",
			sid8, round, len(assistantText), len(toolCalls), err)
		if err != nil {
			log.Printf("[ollama] session=%s stream error: %v", sid8, err)
			return
		}

		if len(toolCalls) == 0 {
			// No tool calls — this is the final text response.
			log.Printf("[ollama] session=%s done in %s (rounds=%d)", sid8, time.Since(startTime).Round(time.Millisecond), round+1)

			stopJSON, _ := json.Marshal(map[string]any{"type": "message_stop"})
			emit(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event":      json.RawMessage(stopJSON),
			})

			if assistantText != "" {
				persistText(assistantText)
			}
			emit(map[string]any{
				"type":       "turn_complete",
				"session_id": sessID,
			})
			return
		}

		// Model requested tool calls — execute each and build tool result messages.
		log.Printf("[ollama] session=%s round=%d executing %d tool(s)", sid8, round+1, len(toolCalls))

		// Add assistant message with tool_calls to history (required by Ollama protocol).
		messages = append(messages, ollamaMessage{
			Role:      "assistant",
			Content:   assistantText,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			name := tc.Function.Name
			log.Printf("[ollama] session=%s tool=%s args=%s", sid8, name, string(tc.Function.Arguments))

			// Generate a stable tool_use_id for permission tracking.
			toolUseID := tc.ID
			if toolUseID == "" {
				toolUseID = uuid.New().String()
			}

			// Ask the user for permission before executing.
			decision := askPermission(toolUseID, name, tc.Function.Arguments)
			log.Printf("[ollama] session=%s tool=%s decision=%s", sid8, name, decision)

			var result string
			if decision == "allow" {
				result = executeTool(name, tc.Function.Arguments, workDir)
				log.Printf("[ollama] session=%s tool=%s result_len=%d", sid8, name, len(result))
			} else {
				result = "Tool execution was denied by the user."
			}

			// Tool result message — role "tool" with tool_call_id for correlation.
			messages = append(messages, ollamaMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// Request next completion without tools so the model summarises results as text.
		nextBody, err := doOllamaChat(context.Background(), host, model, messages, options, false)
		if err != nil {
			log.Printf("[ollama] session=%s tool round %d chat error: %v", sid8, round+1, err)
			return
		}
		currentBody = nextBody
	}

	log.Printf("[ollama] session=%s exceeded max tool rounds (%d)", sid8, maxToolRounds)
}

// readOllamaStream reads one streaming response from Ollama.
// Returns any tool_calls requested by the model and the accumulated text.
// Text deltas are emitted to the frontend as stream_events in real time.
func readOllamaStream(body io.Reader, sessID string, emit func(any)) ([]ollamaToolCall, string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	var accumulated strings.Builder
	var toolCalls []ollamaToolCall

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk ollamaChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			log.Printf("[ollama] session=%s parse: %v", sessID[:8], err)
			continue
		}

		// Collect tool calls from the final chunk.
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}

		if delta := chunk.Message.Content; delta != "" {
			accumulated.WriteString(delta)
			innerEvent := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta,
				},
			}
			innerJSON, _ := json.Marshal(innerEvent)
			emit(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event":      json.RawMessage(innerJSON),
			})
		}

		if chunk.Done {
			break
		}
	}

	return toolCalls, accumulated.String(), scanner.Err()
}
