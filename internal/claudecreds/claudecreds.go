// Package claudecreds keeps the Claude Code OAuth login of this machine alive. The runner runs a Keeper:
// it looks at the .credentials.json of every Claude config dir it uses and, when a token is about to
// expire, trades the refresh token for a new pair and writes it back, so a dir nobody has used for days
// still works when the next turn starts. It replaces the old standalone credfeeder, which also pushed
// the tokens to the hub's Claude accounts (gone); nothing leaves the machine now.
package claudecreds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// TokenURL and ClientID are Claude Code's own OAuth endpoint and client.
	TokenURL = "https://console.anthropic.com/v1/oauth/token"
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// RefreshWithin is how close to its end a token has to be to be renewed.
	RefreshWithin = 5 * time.Minute
)

// Creds is what the keeper needs from a .credentials.json; ExpiresAt is in milliseconds.
type Creds struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// TTL is how long the access token has left.
func (c Creds) TTL() time.Duration { return time.Until(time.UnixMilli(c.ExpiresAt)) }

// File is where a config dir keeps its login.
func File(dir string) string { return filepath.Join(dir, ".credentials.json") }

// DefaultDir is the config dir claude uses when nothing says otherwise.
func DefaultDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

var errNoLogin = errors.New("no OAuth login in this dir")

func readRaw(dir string) (root, oauth map[string]json.RawMessage, err error) {
	data, err := os.ReadFile(File(dir))
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", File(dir), err)
	}
	if raw, ok := root["claudeAiOauth"]; !ok || json.Unmarshal(raw, &oauth) != nil {
		return nil, nil, errNoLogin
	}
	return root, oauth, nil
}

// Read returns the OAuth login of a config dir.
func Read(dir string) (Creds, error) {
	_, oauth, err := readRaw(dir)
	if err != nil {
		return Creds{}, err
	}
	var c Creds
	json.Unmarshal(oauth["accessToken"], &c.AccessToken)
	json.Unmarshal(oauth["refreshToken"], &c.RefreshToken)
	json.Unmarshal(oauth["expiresAt"], &c.ExpiresAt)
	if c.AccessToken == "" && c.RefreshToken == "" {
		return Creds{}, errNoLogin
	}
	return c, nil
}

// Refresh trades the dir's refresh token for a new pair and writes it back, keeping every other field of the
// file. The refresh token rotates, so the new one has to land in the file or the login is lost.
func Refresh(ctx context.Context, httpc *http.Client, tokenURL, dir string) (Creds, error) {
	root, oauth, err := readRaw(dir)
	if err != nil {
		return Creds{}, err
	}
	var refresh string
	json.Unmarshal(oauth["refreshToken"], &refresh)
	if refresh == "" {
		return Creds{}, errors.New("no refresh token: log in again with claude")
	}
	payload, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "client_id": ClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return Creds{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return Creds{}, fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return Creds{}, fmt.Errorf("token refresh: status %d", resp.StatusCode) // the body can hold token text: not logged
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" || tok.ExpiresIn <= 0 {
		return Creds{}, errors.New("token refresh: unexpected response")
	}
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		oauth[k] = b
	}
	set("accessToken", tok.AccessToken)
	if tok.RefreshToken != "" {
		set("refreshToken", tok.RefreshToken)
	}
	set("expiresAt", time.Now().UnixMilli()+tok.ExpiresIn*1000)
	ob, _ := json.Marshal(oauth)
	root["claudeAiOauth"] = ob
	out, _ := json.MarshalIndent(root, "", "  ")
	tmp := File(dir) + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return Creds{}, fmt.Errorf("save refreshed login: %w", err)
	}
	if err := os.Rename(tmp, File(dir)); err != nil {
		os.Remove(tmp)
		return Creds{}, fmt.Errorf("save refreshed login: %w", err)
	}
	return Read(dir)
}

// Keeper renews the logins of the dirs Dirs names, forever. Dirs is asked on every pass, so a dir added
// meanwhile (a new preconfigured AI) is picked up.
type Keeper struct {
	Dirs     func() []string
	HTTP     *http.Client
	TokenURL string // default TokenURL; tests point it at a fake
	Logf     func(format string, args ...any)

	mu       sync.Mutex // one pass at a time
	watching map[string]bool
}

func (k *Keeper) logf(format string, a ...any) {
	if k.Logf != nil {
		k.Logf(format, a...)
	}
}

// Pass looks at every dir once, renews what is close to expiring, and says how long to wait for the next one.
func (k *Keeper) Pass(ctx context.Context) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	httpc, tokenURL := k.HTTP, k.TokenURL
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	if tokenURL == "" {
		tokenURL = TokenURL
	}
	next := time.Duration(0) // the soonest wake of any dir; 0 until one is found
	seen := map[string]bool{}
	for _, dir := range k.Dirs() {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		c, err := Read(dir)
		if err != nil {
			continue // not logged in here: not the keeper's business
		}
		if !k.watching[dir] {
			if k.watching == nil {
				k.watching = map[string]bool{}
			}
			k.watching[dir] = true
			k.logf("[claude-login] watching %s: valid for %v", dir, c.TTL().Round(time.Minute))
		}
		if c.TTL() <= RefreshWithin {
			nc, err := Refresh(ctx, httpc, tokenURL, dir)
			if err != nil {
				k.logf("[claude-login] %s: %v", dir, err)
				continue
			}
			k.logf("[claude-login] %s: renewed, valid for %v", dir, nc.TTL().Round(time.Minute))
			c = nc
		}
		wake := c.TTL() - RefreshWithin
		if wake < time.Minute {
			wake = time.Minute
		}
		if wake > time.Hour {
			wake = time.Hour
		}
		if next == 0 || wake < next {
			next = wake
		}
	}
	if next == 0 {
		next = 10 * time.Minute // nothing to watch yet: look again soon
	}
	return next
}

// Run passes until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	for {
		wait := k.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
