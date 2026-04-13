package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	configDirs := flag.String("config-dir", filepath.Join(os.Getenv("HOME"), ".claude"), "Colon-separated Claude config directories")
	apiKey := flag.String("api-key", os.Getenv("LUCODE_API_KEY"), "API key for lucode server")
	server := flag.String("server", "http://localhost:8090", "lucode server URL")
	accountID := flag.String("account-id", "", "Account ID to update (required)")
	dryRun := flag.Bool("dry-run", false, "Print what would be done without making changes")
	once := flag.Bool("once", false, "Run once and exit (don't loop)")
	flag.Parse()

	if *apiKey == "" {
		log.Fatal("--api-key or LUCODE_API_KEY required")
	}
	if *accountID == "" {
		log.Fatal("--account-id required")
	}

	dirs := strings.Split(*configDirs, ":")
	// Filter empty strings (e.g. from trailing colon)
	var validDirs []string
	for _, d := range dirs {
		if d != "" {
			validDirs = append(validDirs, d)
		}
	}
	if len(validDirs) == 0 {
		log.Fatal("no valid config directories specified")
	}

	log.Printf("credfeeder starting")
	log.Printf("  config dirs : %v", validDirs)
	log.Printf("  account ID  : %s", *accountID)
	log.Printf("  server      : %s", *server)

	if *once {
		for _, dir := range validDirs {
			processDir(dir, *server, *accountID, *apiKey, *dryRun, true)
		}
		return
	}

	// Smart loop: push now, then sleep until 5 min before expiry (or 5 min, whichever is sooner)
	for {
		var nextWake time.Duration

		for _, dir := range validDirs {
			wake := processDir(dir, *server, *accountID, *apiKey, *dryRun, false)
			if nextWake == 0 || (wake > 0 && wake < nextWake) {
				nextWake = wake
			}
		}

		if nextWake <= 0 {
			nextWake = 5 * time.Minute
		}

		log.Printf("Next check in %v", nextWake.Round(time.Second))
		time.Sleep(nextWake)
	}
}

// processDir reads credentials from dir, optionally refreshes them, pushes to lucode,
// and returns how long to sleep before checking again.
// Returns 0 if sleep duration could not be determined.
func processDir(dir, server, accountID, apiKey string, dryRun, once bool) time.Duration {
	log.Printf("[%s] Reading credentials", dir)

	credPath := filepath.Join(dir, ".credentials.json")
	creds, err := readCredentials(credPath)
	if err != nil {
		log.Printf("[%s]   Error reading .credentials.json: %v", dir, err)
		return 0
	}

	if creds.AccessToken == "" {
		log.Printf("[%s]   No access token found", dir)
		return 0
	}

	expiry := time.UnixMilli(creds.ExpiresAt)
	ttl := time.Until(expiry)
	log.Printf("[%s]   Token expires at %s (in %v)", dir, expiry.Format(time.RFC3339), ttl.Round(time.Second))

	// Refresh if token expires within 5 minutes (or already expired)
	const refreshThreshold = 5 * time.Minute
	if ttl <= refreshThreshold {
		log.Printf("[%s]   Token expires soon — refreshing", dir)
		if dryRun {
			log.Printf("[%s]   [DRY RUN] Would run: claude auth login", dir)
		} else {
			if err := refreshToken(dir, creds); err != nil {
				log.Printf("[%s]   Refresh failed: %v", dir, err)
			} else {
				log.Printf("[%s]   Token refreshed successfully", dir)
				// Re-read credentials after refresh
				creds, err = readCredentials(credPath)
				if err != nil {
					log.Printf("[%s]   Error re-reading after refresh: %v", dir, err)
					return 0
				}
				expiry = time.UnixMilli(creds.ExpiresAt)
				ttl = time.Until(expiry)
				log.Printf("[%s]   New expiry: %s (in %v)", dir, expiry.Format(time.RFC3339), ttl.Round(time.Second))
			}
		}
	}

	if dryRun {
		log.Printf("[%s]   [DRY RUN] Would push credentials to %s", dir, server)
	} else {
		log.Printf("[%s]   Pushing credentials to lucode", dir)
		if err := updateCredentials(server, accountID, apiKey, creds); err != nil {
			log.Printf("[%s]   Push failed: %v", dir, err)
			return 0
		}
		log.Printf("[%s]   Credentials updated successfully", dir)
	}

	// Wake up 5 minutes before next expiry (min 1 minute, max 1 hour)
	sleepDur := ttl - refreshThreshold
	if sleepDur < time.Minute {
		sleepDur = time.Minute
	}
	if sleepDur > time.Hour {
		sleepDur = time.Hour
	}
	return sleepDur
}

// refreshToken runs `claude auth login` with the right env vars to refresh the OAuth token.
// After this call, the .credentials.json file in dir will be updated by the claude CLI.
func refreshToken(dir string, creds credentials) error {
	// Build scopes string
	scopes := "user:file_upload user:inference user:mcp_servers user:profile user:sessions:claude_code"

	cmd := exec.Command("claude", "auth", "login")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CLAUDE_CONFIG_DIR=%s", dir),
		fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", creds.AccessToken),
		fmt.Sprintf("CLAUDE_CODE_OAUTH_REFRESH_TOKEN=%s", creds.RefreshToken),
		fmt.Sprintf("CLAUDE_CODE_OAUTH_SCOPES=%s", scopes),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("  Running: CLAUDE_CONFIG_DIR=%s claude auth login", dir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude auth login: %w", err)
	}
	return nil
}

// ---- types ----

type credentials struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	SubscriptionType string `json:"subscriptionType"`
}

func readCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("read file: %w", err)
	}

	var wrapper struct {
		ClaudeAiOauth credentials `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return credentials{}, fmt.Errorf("parse json: %w", err)
	}

	return wrapper.ClaudeAiOauth, nil
}

func updateCredentials(server, accountID, apiKey string, creds credentials) error {
	body := map[string]interface{}{
		"access_token":  creds.AccessToken,
		"refresh_token": creds.RefreshToken,
		"expires_at":    creds.ExpiresAt,
	}

	bodyJSON, _ := json.Marshal(body)
	req, err := http.NewRequest(
		http.MethodPut,
		fmt.Sprintf("%s/api/accounts/%s/credentials", server, accountID),
		bytes.NewReader(bodyJSON),
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
