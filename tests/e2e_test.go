// Package tests contains end-to-end tests that spin up a real lucode server
// (with embedded NATS) and exercise the full API → session → NATS event loop.
//
// Run:
//
//	go test ./tests/... -v -timeout 3m
//
// Requirements:
//   - claude CLI in PATH (for TestClaudeHaiku)
//   - Ollama running on localhost:11434 with gemma4:e2b pulled (for TestOllama)
//
// Each test can be run individually with -run:
//
//	go test ./tests/... -v -run TestClaudeHaiku
//	go test ./tests/... -v -run TestOllama
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/nats-io/nats.go"
)

// ── server harness ────────────────────────────────────────────────────────────

type testServer struct {
	httpURL string
	natsNC  *nats.Conn
	cleanup func()
}

func startServer(t *testing.T) *testServer {
	t.Helper()

	// Pick free ports
	httpPort := freePort(t)
	natsPort := freePort(t)
	wsPort := freePort(t)

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "lucode.db")

	// Build lucode binary
	bin := filepath.Join(t.TempDir(), "lucode-test")
	build := exec.Command("go", "build", "-o", bin, "./cmd/lucode")
	build.Dir = projectRoot(t)
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build lucode: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	srv := exec.CommandContext(ctx, bin,
		fmt.Sprintf("--port=%d", httpPort),
		fmt.Sprintf("--nats-port=%d", natsPort),
		fmt.Sprintf("--nats-ws-port=%d", wsPort),
		fmt.Sprintf("--data=%s", filepath.Dir(dbPath)),
	)
	srv.Stdout = os.Stderr
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		cancel()
		t.Fatalf("start server: %v", err)
	}

	httpURL := fmt.Sprintf("http://localhost:%d", httpPort)
	natsURL := fmt.Sprintf("nats://127.0.0.1:%d", natsPort)

	// Wait for HTTP + NATS to be ready
	waitHTTP(t, httpURL+"/api/health", 15*time.Second)
	waitNATS(t, natsURL, 10*time.Second)

	nc, err := nats.Connect(natsURL)
	if err != nil {
		cancel()
		t.Fatalf("nats connect: %v", err)
	}

	return &testServer{
		httpURL: httpURL,
		natsNC:  nc,
		cleanup: func() {
			nc.Close()
			cancel()
			srv.Wait()
		},
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func projectRoot(t *testing.T) string {
	t.Helper()
	// tests/ is one level below the module root
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	return dir
}

func waitHTTP(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("server did not become healthy at %s within %s", url, timeout)
}

func waitNATS(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(url, nats.Timeout(500*time.Millisecond))
		if err == nil {
			nc.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("NATS did not become ready at %s within %s", url, timeout)
}

func apiPost(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s → %d: %s", url, resp.StatusCode, string(raw))
	}
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

func apiGet(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

// collectEvents subscribes to sess.{sessID}.events and collects messages until
// a session_status event with status=idle is received or timeout expires.
func collectEvents(t *testing.T, nc *nats.Conn, sessID string, timeout time.Duration) []map[string]any {
	t.Helper()
	subj := natspkg.SessionEventsSubject(sessID)
	sub, err := nc.SubscribeSync(subj)
	if err != nil {
		t.Fatalf("subscribe %s: %v", subj, err)
	}
	defer sub.Unsubscribe()

	var events []map[string]any
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			break
		}
		var ev map[string]any
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			continue
		}
		events = append(events, ev)
		t.Logf("event: %s", summariseEvent(ev))

		// Done when session returns to idle
		if ev["type"] == "session_status" && ev["status"] == "idle" {
			break
		}
	}
	return events
}

func summariseEvent(ev map[string]any) string {
	typ, _ := ev["type"].(string)
	switch typ {
	case "session_status":
		return fmt.Sprintf("session_status status=%v", ev["status"])
	case "stream_event":
		return "stream_event"
	case "error":
		return fmt.Sprintf("error message=%v", ev["message"])
	default:
		return typ
	}
}

func hasEventType(events []map[string]any, typ string) bool {
	for _, ev := range events {
		if ev["type"] == typ {
			return true
		}
	}
	return false
}

func hasIdleStatus(events []map[string]any) bool {
	for _, ev := range events {
		if ev["type"] == "session_status" && ev["status"] == "idle" {
			return true
		}
	}
	return false
}

func hasErrorEvent(events []map[string]any) bool {
	for _, ev := range events {
		if ev["type"] == "error" {
			return true
		}
	}
	return false
}

func getMessages(t *testing.T, s *testServer, sessID string) []map[string]any {
	t.Helper()
	result := apiGet(t, fmt.Sprintf("%s/api/sessions/%s/messages", s.httpURL, sessID))
	raw, _ := json.Marshal(result["messages"])
	var msgs []map[string]any
	json.Unmarshal(raw, &msgs)
	return msgs
}

// ── e2e flow (shared between providers) ───────────────────────────────────────

type e2eParams struct {
	model    string
	provider string // "claude" or "ollama"
	prompt   string
	timeout  time.Duration
}

func runE2E(t *testing.T, s *testServer, p e2eParams) {
	t.Helper()

	// 1. Create project (temp dir as work_dir)
	proj := apiPost(t, s.httpURL+"/api/projects", map[string]any{
		"name":      "e2e-test-" + p.provider,
		"work_dirs": []string{t.TempDir()},
	})
	projID, _ := proj["id"].(string)
	t.Logf("project id=%s", projID)

	// 2. Subscribe to NATS events BEFORE creating the session so we don't miss any
	subj := natspkg.SessionEventsSubject("__placeholder__")
	_ = subj // will re-subscribe per-session below

	// 3. Create session — this fires the first turn immediately
	// We need to subscribe first, so do it as two steps:
	// a) create session without message to get the ID
	sessResp := apiPost(t, fmt.Sprintf("%s/api/projects/%s/sessions", s.httpURL, projID), map[string]any{
		"name":     "e2e",
		"model":    p.model,
		"provider": p.provider,
	})
	sessID, _ := sessResp["id"].(string)
	t.Logf("session id=%s", sessID)

	// 4. Subscribe before sending the turn
	eventsDone := make(chan []map[string]any, 1)
	go func() {
		eventsDone <- collectEvents(t, s.natsNC, sessID, p.timeout)
	}()

	// small delay to ensure subscription is active
	time.Sleep(100 * time.Millisecond)

	// 5. Send turn
	reqURL := fmt.Sprintf("%s/api/sessions/%s/turn", s.httpURL, sessID)
	turnData, _ := json.Marshal(map[string]any{
		"message":  p.prompt,
		"model":    p.model,
		"provider": p.provider,
	})
	resp, err := http.Post(reqURL, "application/json", bytes.NewReader(turnData))
	if err != nil {
		t.Fatalf("POST turn: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	t.Logf("turn accepted")

	// 6. Wait for completion
	events := <-eventsDone
	t.Logf("collected %d events", len(events))

	// 7. Assertions
	if hasErrorEvent(events) {
		for _, ev := range events {
			if ev["type"] == "error" {
				t.Errorf("received error event: %v", ev["message"])
			}
		}
		t.FailNow()
	}

	if !hasIdleStatus(events) {
		t.Errorf("never received session_status=idle (turn may still be running or timed out)")
		t.Logf("events received:")
		for _, ev := range events {
			t.Logf("  %s", summariseEvent(ev))
		}
		t.FailNow()
	}

	if !hasEventType(events, "stream_event") {
		t.Errorf("no stream_event received — model produced no output")
	}

	// 8. Verify message was persisted in DB
	msgs := getMessages(t, s, sessID)
	t.Logf("messages in DB: %d", len(msgs))

	var userMsgs, assistantMsgs []map[string]any
	for _, m := range msgs {
		switch m["role"] {
		case "user":
			userMsgs = append(userMsgs, m)
		case "assistant":
			assistantMsgs = append(assistantMsgs, m)
		}
	}

	if len(userMsgs) == 0 {
		t.Errorf("no user message persisted in DB")
	}
	if len(assistantMsgs) == 0 {
		t.Errorf("no assistant message persisted in DB")
	} else {
		content, _ := assistantMsgs[0]["content"].(string)
		t.Logf("assistant response preview: %s", preview(content, 200))
		if strings.TrimSpace(content) == "" {
			t.Errorf("assistant message content is empty")
		}
	}
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestClaudeHaiku(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found in PATH — skipping")
	}

	s := startServer(t)
	defer s.cleanup()

	runE2E(t, s, e2eParams{
		model:    "claude-haiku-4-5-20251001",
		provider: "claude",
		prompt:   "Reply with exactly: PONG",
		timeout:  2 * time.Minute,
	})
}

func TestOllama(t *testing.T) {
	// Check Ollama is reachable
	resp, err := http.Get("http://localhost:11434/api/tags")
	if err != nil || resp.StatusCode != 200 {
		t.Skip("Ollama not running on localhost:11434 — skipping")
	}
	defer resp.Body.Close()

	// Check model is available
	var tags struct {
		Models []struct{ Name string `json:"name"` } `json:"models"`
	}
	json.NewDecoder(resp.Body).Decode(&tags)
	found := false
	for _, m := range tags.Models {
		if strings.HasPrefix(m.Name, "gemma4:e2b") || m.Name == "gemma4:e2b" {
			found = true
			break
		}
	}
	if !found {
		t.Skip("gemma4:e2b not pulled in Ollama — skipping")
	}

	s := startServer(t)
	defer s.cleanup()

	runE2E(t, s, e2eParams{
		model:    "gemma4:e2b",
		provider: "ollama",
		prompt:   "Reply with exactly: PONG",
		timeout:  3 * time.Minute,
	})
}
