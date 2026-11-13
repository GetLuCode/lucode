package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Claude provider: same Anthropic Messages wire format MiniMax already speaks,
// but pointed at api.anthropic.com and authenticated with the Claude Code OAuth
// access token (no API key). Selected per turn by the model name.
const (
	claudeBaseURL       = "https://api.anthropic.com"
	claudeTokenURL      = "https://console.anthropic.com/v1/oauth/token"
	claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeOAuthPrefix   = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeOAuthBeta     = "oauth-2025-04-20"
)

// claudeAliases maps the short names the lucode UI sends to model IDs.
var claudeAliases = map[string]string{
	"haiku":  "claude-haiku-5-5",
	"sonnet": "claude-sonnet-5-5",
	"opus":   "claude-opus-5-5",
}

// ResolveClaudeModel reports whether model targets Anthropic and returns the
// full model ID to send.
func ResolveClaudeModel(model string) (string, bool) {
	if id, ok := claudeAliases[strings.ToLower(model)]; ok {
		return id, true
	}
	if strings.HasPrefix(model, "claude-") {
		return model, true
	}
	return model, false
}

// ClaudeLimits returns the context window and max output tokens for a Claude
// model ID, as reported by GET /v1/models. The 5.x family has a 1M window and
// 128k output; older models are 200k / 64k.
func ClaudeLimits(model string) (contextWindow, maxOutput int) {
	if strings.Contains(model, "-5") && !strings.Contains(model, "-4-5") {
		return 1_000_000, 128_000
	}
	return 200_000, 64_000
}

// claudeThinking builds the thinking block and, for models that need it, the
// output_config. Haiku accepts "disabled"; Sonnet 5.x wants "between_tools" to
// switch reasoning off; Opus 5.x rejects both and is throttled through adaptive
// thinking with a low effort instead.
func claudeThinking(model, thinking string) (map[string]any, map[string]any) {
	if thinking != "" && thinking != "disabled" {
		return map[string]any{"type": "adaptive"}, nil
	}
	switch {
	case strings.Contains(model, "opus-5"):
		return map[string]any{"type": "adaptive"}, map[string]any{"effort": "low"}
	case strings.Contains(model, "sonnet-5"):
		return map[string]any{"type": "between_tools"}, nil
	}
	return map[string]any{"type": "disabled"}, nil
}

var (
	claudeCredMu   sync.Mutex
	claudeCredPath string
)

// SetClaudeCredentialsPath overrides where .credentials.json is read from. The
// path may be the file itself or the directory that contains it.
func SetClaudeCredentialsPath(p string) {
	claudeCredMu.Lock()
	claudeCredPath = p
	claudeCredMu.Unlock()
}

// ClaudeCredentialsPath resolves the credentials file: explicit path, then
// $CLAUDE_CONFIG_DIR, then ~/.claude.
func ClaudeCredentialsPath() string {
	claudeCredMu.Lock()
	defer claudeCredMu.Unlock()
	return claudeCredentialsPathLocked()
}

func claudeCredentialsPathLocked() string {
	p := claudeCredPath
	if p == "" {
		p = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if p == "" {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, ".claude")
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return filepath.Join(p, ".credentials.json")
	}
	return p
}

// claudeAccessToken returns a valid OAuth access token, refreshing it when it
// is about to expire (or when force is set). The refresh token rotates, so the
// new pair is written back to the same file to keep Claude Code logged in.
func claudeAccessToken(ctx context.Context, httpc *http.Client, force bool) (string, error) {
	claudeCredMu.Lock()
	defer claudeCredMu.Unlock()

	path := claudeCredentialsPathLocked()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read claude credentials: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("parse claude credentials: %w", err)
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(root["claudeAiOauth"], &oauth); err != nil {
		return "", fmt.Errorf("claude credentials: missing claudeAiOauth in %s", path)
	}
	var access, refresh string
	var expiresAt int64
	json.Unmarshal(oauth["accessToken"], &access)
	json.Unmarshal(oauth["refreshToken"], &refresh)
	json.Unmarshal(oauth["expiresAt"], &expiresAt)

	expired := expiresAt > 0 && time.Now().UnixMilli() > expiresAt-60_000
	if access != "" && !expired && !force {
		return access, nil
	}
	if refresh == "" {
		if access != "" {
			return access, nil
		}
		return "", fmt.Errorf("claude credentials: no access or refresh token in %s", path)
	}

	payload, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"client_id":     claudeOAuthClientID,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", claudeTokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if !expired && !force {
			return access, nil
		}
		return "", fmt.Errorf("claude token refresh status %d: %s", resp.StatusCode, truncateForLog(string(body)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("claude token refresh: unexpected response")
	}

	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		oauth[k] = b
	}
	set("accessToken", tok.AccessToken)
	if tok.RefreshToken != "" {
		set("refreshToken", tok.RefreshToken)
	}
	if tok.ExpiresIn > 0 {
		set("expiresAt", time.Now().UnixMilli()+tok.ExpiresIn*1000)
	}
	ob, _ := json.Marshal(oauth)
	root["claudeAiOauth"] = ob
	out, _ := json.MarshalIndent(root, "", "  ")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", fmt.Errorf("save refreshed claude credentials: %w", err)
	}
	return tok.AccessToken, nil
}

// ClaudeCredentialsAvailable reports whether an OAuth access token is present.
func ClaudeCredentialsAvailable() bool {
	data, err := os.ReadFile(ClaudeCredentialsPath())
	if err != nil {
		return false
	}
	var f struct {
		O struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	return json.Unmarshal(data, &f) == nil && f.O.AccessToken != ""
}

// claudeCountTokens asks POST /v1/messages/count_tokens how many input tokens a
// request with the given system blocks and tools would use. A one-token dummy
// user message is always included because the endpoint requires messages.
func claudeCountTokens(ctx context.Context, httpc *http.Client, model string, system, tools []map[string]any) (int, error) {
	body := map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": "."}},
	}
	if len(system) > 0 {
		body["system"] = system
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	raw, _ := json.Marshal(body)

	token, err := claudeAccessToken(ctx, httpc, false)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", claudeBaseURL+"/v1/messages/count_tokens", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", claudeOAuthBeta)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("count_tokens status %d: %s", resp.StatusCode, truncateForLog(string(b)))
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	return out.InputTokens, nil
}
