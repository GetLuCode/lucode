package executor

import (
	"context"
	"testing"

	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
)

// tool_calls.jsonl is one turn of two API calls: the first asks for Read and Grep, the
// results come back in a user message, the second answers (see testdata/README).

func ofType(ev *events, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range ev.snapshot() {
		if e["type"] == typ {
			out = append(out, e)
		}
	}
	return out
}

func checkToolTurn(t *testing.T, ev *events) {
	t.Helper()
	calls := ofType(ev, "api_call")
	if len(calls) != 2 {
		t.Fatalf("want 2 api_call events, got %d: %v", len(calls), calls)
	}
	c1, c2 := calls[0], calls[1]
	if c1["first_in_turn"] != true || c2["first_in_turn"] != false {
		t.Errorf("only the first call opens the turn: %v %v", c1["first_in_turn"], c2["first_in_turn"])
	}
	tools := c1["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["id"] != "toolu_read" || tools[0]["name"] != "Read" || tools[1]["name"] != "Grep" {
		t.Errorf("call 1 tools: %v", tools)
	}
	if c1["cache_creation_input_tokens"] != 1000 || c1["cache_creation_1h_tokens"] != 1000 || c1["output_tokens"] != 80 {
		t.Errorf("call 1 usage: %v", c1)
	}
	if c2["cache_read_input_tokens"] != 1000 || c2["output_tokens"] != 20 {
		t.Errorf("call 2 usage: %v", c2)
	}

	results := ofType(ev, "tool_result")
	if len(results) != 2 || results[0]["tool_use_id"] != "toolu_read" || results[0]["chars"] != 300 ||
		results[1]["tool_use_id"] != "toolu_grep" || results[1]["chars"] != 100 {
		t.Errorf("tool_result events: %v", results)
	}

	// the turn's usage is the sum of its two calls, not the last one
	u := ev.find("turn_usage")
	if u == nil {
		t.Fatal("no turn_usage")
	}
	if u["cache_creation_input_tokens"] != 1450 || u["cache_read_input_tokens"] != 1000 ||
		u["output_tokens"] != 100 || u["input_tokens"] != 4 {
		t.Errorf("turn_usage must add up both calls: %v", u)
	}
}

func TestRunReportsEveryCallAndToolResult(t *testing.T) {
	fakeclaudetest.Use(t, "tool_calls.jsonl")
	var ev events
	Run(context.Background(), TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "look around", WorkDirs: []string{t.TempDir()},
	}, ev.add)
	checkToolTurn(t, &ev)
}

func TestLiveReportsEveryCallAndToolResult(t *testing.T) {
	fakeclaudetest.Use(t, "tool_calls.jsonl")
	var ev events
	live, first, err := StartLive(TurnRequest{
		SessionID: "11111111-2222-3333-4444-555555555555", Model: "haiku", Provider: ProviderClaude,
		Message: "look around", WorkDirs: []string{t.TempDir()},
	}, ev.add, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	waitFor(t, first)
	checkToolTurn(t, &ev)
}
