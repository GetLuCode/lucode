package executor

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
)

// The tests below run the executor against recorded claude sessions (see
// internal/fakeclaude), so they spend no tokens.

func TestMain(m *testing.M) {
	fakeclaudetest.Intercept()
	os.Exit(m.Run())
}

type events struct {
	mu  sync.Mutex
	all []map[string]any
}

func (e *events) add(ev map[string]any) {
	e.mu.Lock()
	e.all = append(e.all, ev)
	e.mu.Unlock()
}

func (e *events) snapshot() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.all...)
}

// text joins the assistant messages published so far.
func (e *events) text() string {
	var sb strings.Builder
	for _, ev := range e.snapshot() {
		if ev["type"] == "message_ready" && ev["role"] == "assistant" {
			sb.WriteString(ev["content"].(string))
		}
	}
	return sb.String()
}

func (e *events) find(typ string) map[string]any {
	for _, ev := range e.snapshot() {
		if ev["type"] == typ {
			return ev
		}
	}
	return nil
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the replayed turn")
	}
}

func TestRunReplaysARecordedTurn(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	var ev events
	Run(context.Background(), TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "Remember the secret word PINEAPPLE. Reply with just: ok", WorkDirs: []string{t.TempDir()},
	}, ev.add)

	if got := strings.TrimSpace(ev.text()); got != "ok" {
		t.Errorf("assistant text = %q, want ok", got)
	}
	if ev.find("context_usage") == nil {
		t.Error("the recorded context usage never reached the publisher")
	}
	if u := ev.find("turn_usage"); u == nil || u["output_tokens"] == nil {
		t.Errorf("no turn_usage: %v", u)
	}
	idle := false
	for _, e := range ev.snapshot() {
		if e["type"] == "session_status" && e["status"] == "idle" {
			idle = true
		}
	}
	if !idle {
		t.Error("the turn must report the session idle")
	}
}

func TestLiveReplaysTwoTurnsOnOneProcess(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	var ev events
	live, first, err := StartLive(TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "Remember the secret word PINEAPPLE. Reply with just: ok", WorkDirs: []string{t.TempDir()},
	}, ev.add, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	waitFor(t, first)
	if got := strings.TrimSpace(ev.text()); got != "ok" {
		t.Errorf("turn 1 text = %q", got)
	}

	second, err := live.Send("What was the secret word?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, second)
	if !strings.Contains(ev.text(), "PINEAPPLE") {
		t.Errorf("turn 2 should answer PINEAPPLE, got %q", ev.text())
	}
}

func TestUsageLimitFixtureBlocksTheTurn(t *testing.T) {
	fakeclaudetest.Use(t, "usage_limit.jsonl")
	var ev events
	Run(context.Background(), TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "hi", WorkDirs: []string{t.TempDir()},
	}, ev.add)

	e := ev.find("error")
	if e == nil || e["code"] != "usage_limit" {
		t.Fatalf("want an error event with code usage_limit, got %v", e)
	}
	if e["resets_at"] != int64(1793670000) || !strings.Contains(e["message"].(string), "Available again at") {
		t.Errorf("the reset time is missing: %v", e)
	}
	if ev.find("turn_usage") != nil {
		t.Error("a failed turn must not report usage")
	}
}
