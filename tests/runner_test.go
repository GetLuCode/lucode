// Runner API tests.
// Requirements:
//   - NATS running on nats://127.0.0.1:4222 (override with NATS_URL env var)
//   - claude CLI in PATH (for TestRunnerHaiku)
//
// Run:
//
//	go test ./tests/... -v -run TestRunner -timeout 3m
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// ── runner harness ────────────────────────────────────────────────────────────

type runnerServer struct {
	url     string
	nc      *nats.Conn
	cleanup func()
}

func startRunner(t *testing.T) *runnerServer {
	t.Helper()

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}

	// Verify NATS is reachable before building anything.
	nc, err := nats.Connect(natsURL, nats.Timeout(3*time.Second))
	if err != nil {
		t.Skipf("NATS not reachable at %s — skipping (%v)", natsURL, err)
	}

	bin := filepath.Join(t.TempDir(), "lucode-runner")
	build := exec.Command("go", "build", "-o", bin, "./cmd/runner")
	build.Dir = projectRoot(t)
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		nc.Close()
		t.Fatalf("build runner: %v", err)
	}

	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())

	srv := exec.CommandContext(ctx, bin,
		fmt.Sprintf("--port=%d", port),
		fmt.Sprintf("--nats-url=%s", natsURL),
	)
	srv.Stdout = os.Stderr
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		cancel()
		nc.Close()
		t.Fatalf("start runner: %v", err)
	}

	runnerURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHTTP(t, runnerURL+"/health", 10*time.Second)

	return &runnerServer{
		url: runnerURL,
		nc:  nc,
		cleanup: func() {
			cancel()
			srv.Wait()
			nc.Close()
		},
	}
}

// collectRunnerEvents subscribes to sess.{sessID}.events and collects until
// session_status=idle is received or timeout expires.
func collectRunnerEvents(t *testing.T, nc *nats.Conn, sessID string, timeout time.Duration) []map[string]any {
	t.Helper()
	subj := fmt.Sprintf("sess.%s.events", sessID)
	sub, err := nc.SubscribeSync(subj)
	if err != nil {
		t.Fatalf("subscribe %s: %v", subj, err)
	}
	defer sub.Unsubscribe()

	var events []map[string]any
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		var ev map[string]any
		if json.Unmarshal(msg.Data, &ev) != nil {
			continue
		}
		events = append(events, ev)
		t.Logf("[runner event] %s", summariseEvent(ev))
		if ev["type"] == "session_status" && ev["status"] == "idle" {
			break
		}
	}
	return events
}

// postTurn sends a POST /api/turns to the runner.
func postTurn(t *testing.T, runnerURL string, body map[string]any) int {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(runnerURL+"/api/turns", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST /api/turns: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestRunnerHealth(t *testing.T) {
	r := startRunner(t)
	defer r.cleanup()

	resp, err := http.Get(r.url + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRunnerHaiku(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found in PATH — skipping")
	}

	r := startRunner(t)
	defer r.cleanup()

	sessID := newUUID(t)

	// Subscribe before posting so we don't miss events.
	done := make(chan []map[string]any, 1)
	go func() {
		done <- collectRunnerEvents(t, r.nc, sessID, 2*time.Minute)
	}()
	time.Sleep(50 * time.Millisecond) // let subscription register

	code := postTurn(t, r.url, map[string]any{
		"session_id": sessID,
		"message":    "Reply with exactly: PONG",
		"model":      "claude-haiku-4-5-20251001",
		"provider":   "claude",
		"resume":     false,
	})
	if code != 202 {
		t.Fatalf("expected 202 Accepted, got %d", code)
	}
	t.Logf("turn accepted, waiting for events…")

	events := <-done
	t.Logf("received %d events", len(events))

	if hasErrorEvent(events) {
		for _, ev := range events {
			if ev["type"] == "error" {
				t.Errorf("error event: %v", ev["message"])
			}
		}
		t.FailNow()
	}
	if !hasIdleStatus(events) {
		t.Errorf("never received session_status=idle")
		for _, ev := range events {
			t.Logf("  %s", summariseEvent(ev))
		}
		t.FailNow()
	}
	if !hasEventType(events, "stream_event") {
		t.Errorf("no stream_event — model produced no output")
	}

	// Verify a message_ready event arrived with assistant content.
	var assistantContent string
	for _, ev := range events {
		if ev["type"] == "message_ready" {
			if role, _ := ev["role"].(string); role == "assistant" {
				assistantContent, _ = ev["content"].(string)
				break
			}
		}
	}
	if strings.TrimSpace(assistantContent) == "" {
		t.Errorf("no message_ready(assistant) event with content")
	} else {
		t.Logf("assistant response: %s", preview(assistantContent, 200))
	}
}

func TestRunnerBusyRejection(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found in PATH — skipping")
	}

	r := startRunner(t)
	defer r.cleanup()

	sessID := newUUID(t)

	// First turn — fire and don't wait.
	code := postTurn(t, r.url, map[string]any{
		"session_id": sessID,
		"message":    "Count slowly from 1 to 5, one number per line.",
		"model":      "claude-haiku-4-5-20251001",
		"provider":   "claude",
	})
	if code != 202 {
		t.Fatalf("first turn: expected 202, got %d", code)
	}

	// Second turn on same session while first is running → 409.
	time.Sleep(200 * time.Millisecond)
	code2 := postTurn(t, r.url, map[string]any{
		"session_id": sessID,
		"message":    "hello",
		"model":      "claude-haiku-4-5-20251001",
		"provider":   "claude",
	})
	if code2 != 409 {
		t.Errorf("expected 409 Conflict for busy session, got %d", code2)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newUUID(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		`cat /proc/sys/kernel/random/uuid 2>/dev/null || python3 -c 'import uuid; print(uuid.uuid4())'`).Output()
	if err != nil {
		// fallback: use timestamp-based ID
		return fmt.Sprintf("test-%d", time.Now().UnixNano())
	}
	return strings.TrimSpace(string(out))
}
