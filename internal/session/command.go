package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/toolengine"
)

// CommandDetectResult is returned by DetectCommand.
type CommandDetectResult struct {
	ToolCall *ToolCallResult `json:"tool_call"`
	Reason   string          `json:"reason,omitempty"`
}

// ToolCallResult represents a detected tool invocation.
type ToolCallResult struct {
	ToolName  string          `json:"tool_name"`
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
}

// DetectCommand sends a voice transcript to the Anthropic API with the user's
// tool definitions and returns a tool_call if the model detects a command.
// The API is called with tool_choice="any" to force a tool-only response.
func (m *Manager) DetectCommand(transcript string, tools []dbpkg.UserTool, claudeConfigDir string) (*CommandDetectResult, error) {
	accessToken, err := ensureFreshToken(claudeConfigDir)
	if err != nil {
		return nil, fmt.Errorf("token error: %w", err)
	}

	// Build Anthropic tool definitions from user tools
	anthropicToolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		var schema json.RawMessage
		if err := json.Unmarshal([]byte(t.InputSchema), &schema); err != nil {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		anthropicToolDefs = append(anthropicToolDefs, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": schema,
		})
	}

	reqBody := map[string]any{
		"model":      "claude-haiku-4-5-20251001", // fast + cheap for classification
		"max_tokens": 256,
		"system": []map[string]any{{
			"type": "text",
			"text": `You are a voice command classifier. Analyze the user's spoken transcript and determine if it is a command that matches one of your available tools.

Rules:
- If the transcript clearly matches a tool's purpose, call that tool with the appropriate arguments.
- If the transcript is ambiguous or doesn't match any tool, call the special "__no_match" tool.
- Extract arguments precisely from the spoken words.
- Be conservative: only match when intent is clear.`,
		}},
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("Voice transcript: \"%s\"", transcript),
			}},
		}},
		"tools": append(anthropicToolDefs, map[string]any{
			"name":        "__no_match",
			"description": "Use this when the transcript does not match any available command. This means the text is regular speech, not a command.",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}),
		"tool_choice": map[string]any{
			"type": "any", // Force tool call — no text response
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	log.Printf("[command-detect] POST /v1/messages transcript=%q tools=%d", truncate(transcript, 50), len(tools))

	httpReq, err := http.NewRequest(http.MethodPost, anthropicMessagesURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("anthropic-beta", anthropicBeta)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API connect: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Find the tool_use block
	for _, block := range apiResp.Content {
		if block.Type == "tool_use" {
			if block.Name == "__no_match" {
				log.Printf("[command-detect] no match for transcript")
				return &CommandDetectResult{
					ToolCall: nil,
					Reason:   "no matching command detected",
				}, nil
			}

			// Find the tool ID from our database
			var toolID string
			for _, t := range tools {
				if t.Name == block.Name {
					toolID = t.ID
					break
				}
			}

			log.Printf("[command-detect] matched tool=%s args=%s", block.Name, string(block.Input))
			return &CommandDetectResult{
				ToolCall: &ToolCallResult{
					ToolName:  block.Name,
					ToolID:    toolID,
					Arguments: block.Input,
				},
			}, nil
		}
	}

	return &CommandDetectResult{
		ToolCall: nil,
		Reason:   "no tool_use in response",
	}, nil
}

// ExecuteUserTool runs a user-defined tool's JavaScript code in the Goja sandbox.
func (m *Manager) ExecuteUserTool(tool dbpkg.UserTool, args json.RawMessage) (any, error) {
	// Determine work dir — use the first project's work dir if available, else cwd
	workDir := ""
	projects, _ := dbpkg.ListProjects(m.db)
	if len(projects) > 0 && len(projects[0].WorkDirs) > 0 {
		workDir = projects[0].WorkDirs[0]
	}

	result, err := toolengine.Execute(tool.Code, args, workDir)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
