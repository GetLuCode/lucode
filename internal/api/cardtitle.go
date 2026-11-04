package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// titleModel is the model used to name new cards from their description.
const titleModel = "claude-haiku-5-5"

const titleSystemPrompt = "Write a short title (max 8 words) for the task described by the user. " +
	"Use the same language as the description. Reply with the title only: no quotes, no trailing period."

// generateCardTitle asks the Anthropic Messages API (no tools, minimal system
// prompt) for a title based only on the description text. Attachments and images
// are never sent. If the API key is missing or the call fails it falls back to a
// truncated first line of the description, so card creation never fails.
func generateCardTitle(ctx context.Context, description string) string {
	description = strings.TrimSpace(description)
	if t, err := callTitleAPI(ctx, description); err == nil && t != "" {
		return t
	}
	return fallbackTitle(description)
}

func callTitleAPI(ctx context.Context, description string) (string, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	base := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{
		"model":      titleModel,
		"max_tokens": 64,
		"system":     titleSystemPrompt,
		"messages":   []map[string]any{{"role": "user", "content": description}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic: %s", resp.Status)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	t := strings.TrimSpace(sb.String())
	t = strings.Trim(t, "\"'`")
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t), nil
}

func fallbackTitle(description string) string {
	line := strings.TrimSpace(strings.SplitN(description, "\n", 2)[0])
	r := []rune(line)
	if len(r) > 60 {
		line = string(r[:60]) + "…"
	}
	return line
}
