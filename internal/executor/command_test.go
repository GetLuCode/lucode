package executor

import (
	"context"
	"strings"
	"testing"

	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
)

// A runner tells the hub what it launched through the claude_command event.
func TestRunPublishesTheCommandItLaunched(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	var ev events
	dir := t.TempDir()
	Run(context.Background(), TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "hi", WorkDirs: []string{dir}, Worktree: true, WorktreeBaseRef: "head", Name: "Engineer",
	}, ev.add)

	c := ev.find("claude_command")
	if c == nil {
		t.Fatal("no claude_command event")
	}
	args, _ := c["args"].([]string)
	joined := strings.Join(args, " ")
	if c["cwd"] != dir || c["session_id"] != "11111111-2222-3333-4444-555555555555" || c["error"] != nil {
		t.Errorf("event = %v", c)
	}
	if pid, _ := c["pid"].(int); pid <= 0 {
		t.Errorf("pid = %v", c["pid"])
	}
	for _, want := range []string{"-p", "--model=haiku", "--name=Engineer", "--worktree=lucode-11111111"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
}

// A process that does not start is exactly when the command is wanted.
func TestRunPublishesTheCommandThatFailedToStart(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	var ev events
	Run(context.Background(), TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "hi", WorkDirs: []string{"definitely-not-a-folder"},
	}, ev.add)

	c := ev.find("claude_command")
	if c == nil || c["error"] == nil || c["pid"] != 0 || c["cwd"] != "definitely-not-a-folder" {
		t.Fatalf("event = %v", c)
	}
	if ev.find("error") == nil {
		t.Error("the failure must still be reported as an error")
	}
}
