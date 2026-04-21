package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	anthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicOAuthScope    = "user:file_upload user:inference user:mcp_servers user:profile user:sessions:claude_code"
	anthropicTokenURL      = "https://platform.claude.com/v1/oauth/token"
	anthropicMessagesURL   = "https://api.anthropic.com/v1/messages"
	anthropicVersion       = "2023-06-01"
	anthropicBeta          = "oauth-2025-04-20"
)

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
func ensureFreshToken(claudeConfigDir string) (string, error) {
	creds, err := loadCredentials(claudeConfigDir)
	if err != nil {
		return "", err
	}
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
