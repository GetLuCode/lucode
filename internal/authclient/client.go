package authclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const cacheTTL = 60 * time.Second

type entry struct {
	UserID   string
	IsAdmin  bool
	cachedAt time.Time
}

type Client struct {
	baseURL string
	mu      sync.Mutex
	tokens  map[string]entry
	apiKeys map[string]entry
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		tokens:  make(map[string]entry),
		apiKeys: make(map[string]entry),
	}
}

func (c *Client) BaseURL() string { return c.baseURL }

type ValidateResult struct {
	UserID  string
	IsAdmin bool
}

func (c *Client) ValidateToken(token string) (ValidateResult, error) {
	if e, ok := c.getCache(c.tokens, token); ok {
		return ValidateResult{e.UserID, e.IsAdmin}, nil
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	res, err := c.post("/auth/validate", body)
	if err != nil {
		return ValidateResult{}, err
	}
	c.setCache(c.tokens, token, entry{res.UserID, res.IsAdmin, time.Now()})
	return res, nil
}

func (c *Client) ValidateAPIKey(key string) (ValidateResult, error) {
	if e, ok := c.getCache(c.apiKeys, key); ok {
		return ValidateResult{e.UserID, e.IsAdmin}, nil
	}
	body, _ := json.Marshal(map[string]string{"api_key": key})
	res, err := c.post("/auth/validate", body)
	if err != nil {
		return ValidateResult{}, err
	}
	c.setCache(c.apiKeys, key, entry{res.UserID, res.IsAdmin, time.Now()})
	return res, nil
}

func (c *Client) InvalidateToken(token string) {
	c.mu.Lock()
	delete(c.tokens, token)
	c.mu.Unlock()
}

func (c *Client) post(path string, body []byte) (ValidateResult, error) {
	resp, err := http.Post(c.baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return ValidateResult{}, fmt.Errorf("authapi unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ValidateResult{}, fmt.Errorf("unauthorized")
	}
	var r ValidateResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ValidateResult{}, err
	}
	return r, nil
}

func (c *Client) getCache(m map[string]entry, key string) (entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := m[key]
	if !ok || time.Since(e.cachedAt) > cacheTTL {
		return entry{}, false
	}
	return e, true
}

func (c *Client) setCache(m map[string]entry, key string, e entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m[key] = e
}
