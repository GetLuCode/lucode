package executor

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
)

const defaultOllamaHost = "http://localhost:11434"

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Images     []string         `json:"images,omitempty"`
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	Thinking    *bool   `json:"thinking,omitempty"`
}

type ollamaChunk struct {
	Model   string `json:"model"`
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking,omitempty"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason,omitempty"`
}

type ollamaRunHandle struct {
	body    io.ReadCloser
	host    string
	model   string
	options *ollamaOptions
	messages []ollamaMessage
	workDir  string
	// askPermission is called for each tool call before execution.
	askPermission func(toolUseID, toolName string, toolInput json.RawMessage) string
}

// buildOllamaMessages converts HistoryMessages to the Ollama message format and
// appends the current user turn (including optional image).
func buildOllamaMessages(history []HistoryMessage, req TurnRequest) []ollamaMessage {
	msgs := []ollamaMessage{
		{Role: "system", Content: "You are a helpful coding assistant. When you need to read, write, or run code use the provided tools."},
	}
	for _, h := range history {
		switch h.Role {
		case "user", "assistant":
			msgs = append(msgs, ollamaMessage{Role: h.Role, Content: h.Content})
		}
	}
	user := ollamaMessage{Role: "user", Content: req.Message}
	if req.ImageBase64 != "" {
		user.Images = []string{req.ImageBase64}
	}
	return append(msgs, user)
}

func buildOllamaOptions(req TurnRequest) *ollamaOptions {
	o := &ollamaOptions{}
	switch req.Thinking {
	case "enabled":
		t := true
		o.Thinking = &t
		return o
	case "disabled":
		f := false
		o.Thinking = &f
		return o
	}
	return nil
}

func startOllamaRun(ctx context.Context, req TurnRequest, askPermission func(string, string, json.RawMessage) string) (*ollamaRunHandle, error) {
	host := req.OllamaHost
	if host == "" {
		host = defaultOllamaHost
	}

	messages := buildOllamaMessages(req.History, req)
	options := buildOllamaOptions(req)

	workDir := ""
	if len(req.WorkDirs) > 0 {
		workDir = req.WorkDirs[0]
	}

	body, err := doOllamaChat(ctx, host, req.Model, messages, options, true)
	if err != nil {
		return nil, err
	}

	return &ollamaRunHandle{
		body:          body,
		host:          host,
		model:         req.Model,
		options:       options,
		messages:      messages,
		workDir:       workDir,
		askPermission: askPermission,
	}, nil
}

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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("ollama: build req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
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

// streamOllama drives the agentic loop: streaming → tool execution rounds → final text.
func streamOllama(handle *ollamaRunHandle, sessID string, publish func(map[string]any)) {
	defer handle.body.Close()

	startTime := time.Now()
	sid8 := sessID[:8]
	messages := handle.messages
	currentBody := handle.body
	const maxToolRounds = 10

	for round := 0; round < maxToolRounds; round++ {
		toolCalls, assistantText, err := readOllamaStream(currentBody, sessID, publish)
		if round > 0 {
			currentBody.Close()
		}
		log.Printf("[executor/ollama] session=%s round=%d text_len=%d tool_calls=%d err=%v",
			sid8, round, len(assistantText), len(toolCalls), err)
		if err != nil {
			return
		}

		if len(toolCalls) == 0 {
			log.Printf("[executor/ollama] session=%s done in %s (rounds=%d)",
				sid8, time.Since(startTime).Round(time.Millisecond), round+1)

			stopJSON, _ := json.Marshal(map[string]any{"type": "message_stop"})
			publish(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event":      json.RawMessage(stopJSON),
			})
			publish(map[string]any{
				"type":            "message_ready",
				"session_id":      sessID,
				"role":            "assistant",
				"content":         assistantText,
			})
			publish(map[string]any{
				"type":       "turn_complete",
				"session_id": sessID,
			})
			return
		}

		messages = append(messages, ollamaMessage{
			Role:      "assistant",
			Content:   assistantText,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			name := tc.Function.Name
			toolUseID := tc.ID
			if toolUseID == "" {
				toolUseID = fmt.Sprintf("tc-%d-%s", round, name)
			}

			decision := handle.askPermission(toolUseID, name, tc.Function.Arguments)
			var result string
			if decision == "allow" || decision == "approve" {
				result = executeTool(name, tc.Function.Arguments, handle.workDir)
			} else {
				result = "Tool execution was denied by the user."
			}
			messages = append(messages, ollamaMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		nextBody, err := doOllamaChat(context.Background(), handle.host, handle.model, messages, handle.options, false)
		if err != nil {
			log.Printf("[executor/ollama] session=%s tool round %d chat error: %v", sid8, round+1, err)
			return
		}
		currentBody = nextBody
	}

	log.Printf("[executor/ollama] session=%s exceeded max tool rounds", sid8)
}

func readOllamaStream(body io.Reader, sessID string, publish func(map[string]any)) ([]ollamaToolCall, string, error) {
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
			log.Printf("[executor/ollama] session=%s parse: %v", sessID[:8], err)
			continue
		}
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}
		if delta := chunk.Message.Content; delta != "" {
			accumulated.WriteString(delta)
			innerEvent := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": delta},
			}
			innerJSON, _ := json.Marshal(innerEvent)
			publish(map[string]any{
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
