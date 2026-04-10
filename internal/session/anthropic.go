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
	"os"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

const (
	anthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicOAuthScope    = "user:file_upload user:inference user:mcp_servers user:profile user:sessions:claude_code"
	anthropicTokenURL      = "https://platform.claude.com/v1/oauth/token"
	anthropicMessagesURL   = "https://api.anthropic.com/v1/messages"
	anthropicVersion       = "2023-06-01"
	anthropicBeta          = "oauth-2025-04-20"
)

// ── Credentials ──────────────────────────────────────────────────────────────

type oauthCredentials struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"` // Unix ms
	SubscriptionType string `json:"subscriptionType"`
}

type credentialsFile struct {
	ClaudeAiOauth oauthCredentials `json:"claudeAiOauth"`
}

func loadCredentials(claudeConfigDir string) (*oauthCredentials, error) {
	path := claudeConfigDir + "/.credentials.json"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var f credentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if f.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("credentials missing accessToken")
	}
	return &f.ClaudeAiOauth, nil
}

func saveCredentials(claudeConfigDir string, creds *oauthCredentials) error {
	path := claudeConfigDir + "/.credentials.json"
	// Read existing file to preserve other fields
	existing, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read credentials for update: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(existing, &raw); err != nil {
		return fmt.Errorf("parse credentials for update: %w", err)
	}
	updated, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal updated credentials: %w", err)
	}
	raw["claudeAiOauth"] = updated
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials file: %w", err)
	}
	return os.WriteFile(path, out, 0600)
}

// ensureFreshToken returns a valid access token, refreshing if within 5 minutes of expiry.
// Writes updated credentials back to disk on refresh.
func ensureFreshToken(claudeConfigDir string) (string, error) {
	creds, err := loadCredentials(claudeConfigDir)
	if err != nil {
		return "", err
	}

	// Refresh if expiring within 5 minutes
	if time.Now().UnixMilli() > creds.ExpiresAt-5*60*1000 {
		log.Printf("[anthropic] access token expiring, refreshing...")
		newCreds, err := refreshAccessToken(creds)
		if err != nil {
			return "", fmt.Errorf("token refresh: %w", err)
		}
		if err := saveCredentials(claudeConfigDir, newCreds); err != nil {
			log.Printf("[anthropic] warning: failed to save refreshed credentials: %v", err)
		}
		return newCreds.AccessToken, nil
	}
	return creds.AccessToken, nil
}

func refreshAccessToken(creds *oauthCredentials) (*oauthCredentials, error) {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": creds.RefreshToken,
		"client_id":     anthropicOAuthClientID,
		"scope":         anthropicOAuthScope,
	}
	data, _ := json.Marshal(payload)

	resp, err := http.Post(anthropicTokenURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	newCreds := *creds
	newCreds.AccessToken = result.AccessToken
	if result.RefreshToken != "" {
		newCreds.RefreshToken = result.RefreshToken
	}
	newCreds.ExpiresAt = time.Now().UnixMilli() + result.ExpiresIn*1000
	return &newCreds, nil
}

// ── Model mapping ─────────────────────────────────────────────────────────────

func resolveAnthropicModel(short string) string {
	switch short {
	case "haiku":
		return "claude-haiku-4-5-20251001"
	case "sonnet":
		return "claude-sonnet-4-6"
	case "opus":
		return "claude-opus-4-6"
	default:
		// If it already looks like a full model ID, pass through
		return short
	}
}

// ── Anthropic API types ───────────────────────────────────────────────────────

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data"`       // base64-encoded image data
}

type anthropicContent struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Source       *anthropicImageSource  `json:"source,omitempty"`      // image
	ID           string                 `json:"id,omitempty"`          // tool_use
	Name         string                 `json:"name,omitempty"`        // tool_use
	Input        json.RawMessage        `json:"input,omitempty"`       // tool_use
	ToolUseID    string                 `json:"tool_use_id,omitempty"` // tool_result
	Content      string                 `json:"content,omitempty"`     // tool_result
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicToolInputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

type anthropicTool struct {
	Name         string                   `json:"name"`
	Description  string                   `json:"description"`
	InputSchema  anthropicToolInputSchema  `json:"input_schema"`
	CacheControl *anthropicCacheControl    `json:"cache_control,omitempty"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    []anthropicContent `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools"`
	Stream    bool               `json:"stream"`
}

// ── Tool definitions ──────────────────────────────────────────────────────────

// anthropicTools mirrors lucodeTools but in Anthropic format.
// The last tool has cache_control so the tools block is prompt-cached.
var anthropicTools = []anthropicTool{
	{
		Name:        "read_file",
		Description: "Read the full contents of a file at the given path.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Absolute or relative path to the file.",
				},
			},
			Required: []string{"path"},
		},
	},
	{
		Name:        "write_file",
		Description: "Write (overwrite) a file with the given content. Creates parent directories if needed.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file to write.",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Content to write to the file.",
				},
			},
			Required: []string{"path", "content"},
		},
	},
	{
		Name:        "edit_file",
		Description: "Replace an exact string in a file with a new string. Fails if old_string is not found.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"path":       map[string]interface{}{"type": "string", "description": "Path to the file to edit."},
				"old_string": map[string]interface{}{"type": "string", "description": "Exact string to find and replace."},
				"new_string": map[string]interface{}{"type": "string", "description": "Replacement string."},
			},
			Required: []string{"path", "old_string", "new_string"},
		},
	},
	{
		Name:        "run_command",
		Description: "Run a shell command in the working directory and return stdout+stderr. Use for builds, tests, grep, ls, etc.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "Shell command to execute (runs via sh -c).",
				},
			},
			Required: []string{"command"},
		},
	},
	{
		Name:        "list_directory",
		Description: "List files and subdirectories in a directory.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory path to list.",
				},
			},
			Required: []string{"path"},
		},
	},
	{
		Name:        "search_files",
		Description: "Search for a string pattern in files under a directory (recursive grep).",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"path":         map[string]interface{}{"type": "string", "description": "Root directory to search in."},
				"pattern":      map[string]interface{}{"type": "string", "description": "String or regex pattern to search for."},
				"file_pattern": map[string]interface{}{"type": "string", "description": "Optional glob to filter files (e.g. '*.go', '*.ts'). Defaults to all files."},
			},
			Required: []string{"path", "pattern"},
		},
	},
	{
		Name:        "webfetch",
		Description: "Fetch content from an HTTP/HTTPS URL using curl and return the response body.",
		InputSchema: anthropicToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"url":     map[string]interface{}{"type": "string", "description": "The URL to fetch."},
				"method":  map[string]interface{}{"type": "string", "description": "HTTP method: GET, POST, PUT, DELETE, etc. Defaults to GET."},
				"body":    map[string]interface{}{"type": "string", "description": "Request body for POST/PUT requests."},
				"headers": map[string]interface{}{"type": "object", "description": "Custom HTTP headers as key-value pairs."},
			},
			Required: []string{"url"},
		},
		// Last tool gets cache_control so tools block is prompt-cached
		CacheControl: &anthropicCacheControl{Type: "ephemeral"},
	},
}

// ── History builder ───────────────────────────────────────────────────────────

// buildAnthropicHistory converts stored DB messages to Anthropic API format.
// Mirrors buildOllamaHistory — skips thinking/hook/system/tool_use/tool_result roles
// (those are reconstructed in-loop, not persisted).
func buildAnthropicHistory(dbMessages []dbpkg.Message) []anthropicMessage {
	var out []anthropicMessage
	for _, m := range dbMessages {
		switch m.Role {
		case "user":
			text, images := extractContentAndImages(m.Content)
			content := []anthropicContent{}
			if text != "" {
				content = append(content, anthropicContent{Type: "text", Text: text})
			}
			for _, img := range images {
				// images from extractContentAndImages are raw base64; media type defaults to jpeg
				// The stored format includes media_type in the source block — re-parse to get it.
				content = append(content, anthropicContent{
					Type: "image",
					Source: &anthropicImageSource{
						Type:      "base64",
						MediaType: "image/jpeg", // fallback; overridden below if available
						Data:      img,
					},
				})
			}
			// Re-parse to recover media_type for each image block
			content = reParseUserContentBlocks(m.Content, content)
			if len(content) == 0 {
				content = []anthropicContent{{Type: "text", Text: text}}
			}
			out = append(out, anthropicMessage{Role: "user", Content: content})
		case "assistant":
			out = append(out, anthropicMessage{
				Role:    "assistant",
				Content: []anthropicContent{{Type: "text", Text: m.Content}},
			})
		}
	}
	return out
}

// reParseUserContentBlocks re-parses stored JSON content blocks to recover
// media_type for image blocks. Falls back to the already-built content slice
// if the content is not JSON or has no image blocks.
func reParseUserContentBlocks(raw string, fallback []anthropicContent) []anthropicContent {
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		return fallback
	}
	var out []anthropicContent
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, anthropicContent{Type: "text", Text: b.Text})
			}
		case "image":
			if b.Source.Data != "" {
				mt := b.Source.MediaType
				if mt == "" {
					mt = "image/jpeg"
				}
				out = append(out, anthropicContent{
					Type: "image",
					Source: &anthropicImageSource{
						Type:      "base64",
						MediaType: mt,
						Data:      b.Source.Data,
					},
				})
			}
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// ── Run handle ───────────────────────────────────────────────────────────────

type anthropicRunHandle struct {
	ps              *ProcessState
	claudeConfigDir string
	model           string
	messages        []anthropicMessage
	workDir         string
}

func startAnthropicRun(_ context.Context, opts TurnOptions, history []dbpkg.Message) (*anthropicRunHandle, error) {
	claudeConfigDir := opts.AccountClaudeConfigDir
	if claudeConfigDir == "" {
		claudeConfigDir = opts.ClaudeConfigDir
	}
	if claudeConfigDir == "" {
		return nil, fmt.Errorf("anthropic provider requires a claude config dir (account or project)")
	}

	// Build conversation history + current user message
	messages := buildAnthropicHistory(history)
	userContent := []anthropicContent{}
	if opts.Message != "" {
		userContent = append(userContent, anthropicContent{Type: "text", Text: opts.Message})
	}
	if opts.ImageBase64 != "" {
		mt := opts.ImageMediaType
		if mt == "" {
			mt = "image/jpeg"
		}
		userContent = append(userContent, anthropicContent{
			Type: "image",
			Source: &anthropicImageSource{
				Type:      "base64",
				MediaType: mt,
				Data:      opts.ImageBase64,
			},
		})
	}
	if len(userContent) == 0 {
		userContent = []anthropicContent{{Type: "text", Text: ""}}
	}
	messages = append(messages, anthropicMessage{Role: "user", Content: userContent})

	workDir := ""
	if len(opts.WorkDirs) > 0 {
		workDir = opts.WorkDirs[0]
	}

	ps := &ProcessState{
		SessionID: opts.SessionID,
		Done:      make(chan struct{}),
	}

	return &anthropicRunHandle{
		ps:              ps,
		claudeConfigDir: claudeConfigDir,
		model:           resolveAnthropicModel(opts.Model),
		messages:        messages,
		workDir:         workDir,
	}, nil
}

// ── SSE streaming ─────────────────────────────────────────────────────────────

func doAnthropicChat(ctx context.Context, accessToken string, req anthropicRequest) (io.ReadCloser, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	// Log request (without full content for brevity)
	log.Printf("[anthropic→api] POST /v1/messages model=%s messages=%d", req.Model, len(req.Messages))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("anthropic-beta", anthropicBeta)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// readAnthropicSSE reads an SSE stream from the Anthropic API.
// It emits content_block_delta events to the WS via emit(), accumulates the
// full text, and collects any tool_use blocks.
// Returns (toolUseBlocks, fullText, error).
func readAnthropicSSE(body io.Reader, sessID string, emit func(any)) ([]anthropicContent, string, error) {
	scanner := bufio.NewScanner(body)
	var sb strings.Builder
	var toolUseBlocks []anthropicContent

	// Tracks partial tool_use blocks being built during streaming
	type partialTool struct {
		id    string
		name  string
		input strings.Builder
	}
	toolMap := map[int]*partialTool{}
	// Tracks content block types by index
	blockTypes := map[int]string{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			break
		}

		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			continue
		}

		typeBytes, _ := event["type"]
		var eventType string
		json.Unmarshal(typeBytes, &eventType)

		switch eventType {
		case "content_block_start":
			var idx int
			json.Unmarshal(event["index"], &idx)
			var block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			json.Unmarshal(event["content_block"], &block)
			blockTypes[idx] = block.Type
			if block.Type == "tool_use" {
				toolMap[idx] = &partialTool{id: block.ID, name: block.Name}
			}

		case "content_block_delta":
			var idx int
			json.Unmarshal(event["index"], &idx)
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			}
			json.Unmarshal(event["delta"], &delta)

			switch delta.Type {
			case "text_delta":
				sb.WriteString(delta.Text)
				// Emit to WS so frontend sees streaming text in real time
				emit(map[string]any{
					"type":       "stream_event",
					"session_id": sessID,
					"event": map[string]any{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]any{
							"type": "text_delta",
							"text": delta.Text,
						},
					},
				})
			case "input_json_delta":
				if t, ok := toolMap[idx]; ok {
					t.input.WriteString(delta.PartialJSON)
				}
			}

		case "content_block_stop":
			var idx int
			json.Unmarshal(event["index"], &idx)
			if blockTypes[idx] == "tool_use" {
				if t, ok := toolMap[idx]; ok {
					inputRaw := json.RawMessage(t.input.String())
					if len(inputRaw) == 0 {
						inputRaw = json.RawMessage("{}")
					}
					toolUseBlocks = append(toolUseBlocks, anthropicContent{
						Type:  "tool_use",
						ID:    t.id,
						Name:  t.name,
						Input: inputRaw,
					})
				}
			}

		case "message_stop":
			return toolUseBlocks, sb.String(), nil

		case "error":
			var errMsg struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal([]byte(raw), &errMsg)
			return nil, sb.String(), fmt.Errorf("anthropic API error: %s", errMsg.Error.Message)
		}
	}

	if err := scanner.Err(); err != nil {
		return toolUseBlocks, sb.String(), err
	}
	return toolUseBlocks, sb.String(), nil
}

// ── Agentic loop ──────────────────────────────────────────────────────────────

func streamAnthropic(
	handle *anthropicRunHandle,
	sessID string,
	emit func(any),
	persistText func(string),
	askPermission func(toolUseID, toolName string, toolInput json.RawMessage) string,
) {
	defer close(handle.ps.Done)

	ctx := context.Background()
	messages := handle.messages

	// Emit message_start so frontend shows the assistant is responding
	emit(map[string]any{
		"type":       "stream_event",
		"session_id": sessID,
		"event": map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"role": "assistant",
			},
		},
	})

	systemPrompt := []anthropicContent{
		{
			Type:         "text",
			Text:         "You are a helpful coding assistant. When you need to read, write, or run code use the provided tools.",
			CacheControl: &anthropicCacheControl{Type: "ephemeral"},
		},
	}

	const maxToolRounds = 10

	for round := 0; round < maxToolRounds; round++ {
		// Refresh token if needed each round
		accessToken, err := ensureFreshToken(handle.claudeConfigDir)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to get access token: %v", err)
			log.Printf("[anthropic] %s", errMsg)
			emit(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event": map[string]any{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "\n\n[Error: " + errMsg + "]",
					},
				},
			})
			return
		}

		req := anthropicRequest{
			Model:     handle.model,
			MaxTokens: 8192,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     anthropicTools,
			Stream:    true,
		}

		body, err := doAnthropicChat(ctx, accessToken, req)
		if err != nil {
			log.Printf("[anthropic] round %d: API error: %v", round, err)
			emit(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event": map[string]any{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": fmt.Sprintf("\n\n[Error: %v]", err),
					},
				},
			})
			return
		}

		toolUseBlocks, text, err := readAnthropicSSE(body, sessID, emit)
		body.Close()
		if err != nil {
			log.Printf("[anthropic] round %d: SSE error: %v", round, err)
			return
		}

		log.Printf("[anthropic] round %d: text=%d chars, tool_uses=%d", round, len(text), len(toolUseBlocks))

		if len(toolUseBlocks) == 0 {
			// No more tool calls — final response
			persistText(text)
			emit(map[string]any{
				"type":       "stream_event",
				"session_id": sessID,
				"event":      map[string]any{"type": "message_stop"},
			})
			emit(map[string]any{
				"type":       "turn_complete",
				"session_id": sessID,
			})
			return
		}

		// Build assistant message with text + tool_use blocks
		assistantContent := []anthropicContent{}
		if text != "" {
			assistantContent = append(assistantContent, anthropicContent{Type: "text", Text: text})
		}
		assistantContent = append(assistantContent, toolUseBlocks...)
		messages = append(messages, anthropicMessage{Role: "assistant", Content: assistantContent})

		// Execute tools and collect tool_result blocks
		toolResults := []anthropicContent{}
		for _, tb := range toolUseBlocks {
			decision := askPermission(tb.ID, tb.Name, tb.Input)
			var result string
			if decision == "allow" || decision == "approve" {
				result = executeTool(tb.Name, tb.Input, handle.workDir)
			} else {
				result = "Tool execution was denied by the user."
			}
			log.Printf("[anthropic] tool %s(%s) → decision=%s", tb.Name, tb.ID[:8], decision)
			toolResults = append(toolResults, anthropicContent{
				Type:      "tool_result",
				ToolUseID: tb.ID,
				Content:   result,
			})
		}

		// Append user message with all tool_result blocks
		messages = append(messages, anthropicMessage{
			Role:    "user",
			Content: toolResults,
		})
	}

	// Exceeded max tool rounds
	log.Printf("[anthropic] exceeded max tool rounds (%d)", maxToolRounds)
	persistText("(max tool rounds exceeded)")
	emit(map[string]any{
		"type":       "turn_complete",
		"session_id": sessID,
	})
}
