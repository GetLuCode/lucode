package executor

import "testing"

func feed(t *toolTracker, lines ...string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		out = append(out, t.observe("sess", []byte(l))...)
	}
	return out
}

const (
	start1 = `{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":500,"cache_read_input_tokens":30000,"cache_creation":{"ephemeral_1h_input_tokens":500,"ephemeral_5m_input_tokens":0}}}}}`
	read   = `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}}`
	grep   = `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_2","name":"Grep"}}}`
	delta1 = `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":80}}}`
)

func TestTrackerReportsEachCallWithItsToolsAndUsage(t *testing.T) {
	tr := newToolTracker()
	evs := feed(tr, start1, read, grep, delta1)
	if len(evs) != 1 {
		t.Fatalf("want one api_call, got %v", evs)
	}
	e := evs[0]
	if e["type"] != "api_call" || e["first_in_turn"] != true || e["model"] != "claude-sonnet-5-5" {
		t.Fatalf("unexpected event %v", e)
	}
	if e["input_tokens"] != 2 || e["output_tokens"] != 80 || e["cache_creation_input_tokens"] != 500 ||
		e["cache_read_input_tokens"] != 30000 || e["cache_creation_1h_tokens"] != 500 || e["cache_creation_5m_tokens"] != 0 {
		t.Fatalf("wrong usage in %v", e)
	}
	tools := e["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["id"] != "toolu_1" || tools[0]["name"] != "Read" || tools[1]["name"] != "Grep" {
		t.Fatalf("wrong tools %v", tools)
	}

	// the second call of the same turn is not the first; a result line ends neither
	evs = feed(tr, start1, delta1)
	if evs[0]["first_in_turn"] != false {
		t.Fatalf("second call of the turn must not be first_in_turn: %v", evs[0])
	}
	// after the turn's result the next call opens a new turn
	feed(tr, `{"type":"result","subtype":"success"}`)
	evs = feed(tr, start1, delta1)
	if evs[0]["first_in_turn"] != true {
		t.Fatalf("the call after a result must be first_in_turn: %v", evs[0])
	}
}

func TestTrackerSizesToolResultsWithoutKeepingContent(t *testing.T) {
	tr := newToolTracker()
	evs := feed(tr, `{"type":"user","message":{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":"héllo"},
		{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":[{"type":"text","text":"abc"},{"type":"image","source":{}},{"type":"text","text":"de"}]}]}}`)
	if len(evs) != 2 {
		t.Fatalf("want two tool_result events, got %v", evs)
	}
	if evs[0]["tool_use_id"] != "toolu_1" || evs[0]["chars"] != 5 || evs[0]["images"] != 0 || evs[0]["is_error"] != false {
		t.Fatalf("string result: %v", evs[0])
	}
	if evs[1]["chars"] != 5 || evs[1]["images"] != 1 || evs[1]["is_error"] != true {
		t.Fatalf("block result: %v", evs[1])
	}
	for _, e := range evs {
		for k := range e {
			if k == "content" || k == "text" {
				t.Fatalf("the content must not be reported: %v", e)
			}
		}
	}
}

func TestTrackerIgnoresPlainUserMessagesAndKeepsSubAgentCallsApart(t *testing.T) {
	tr := newToolTracker()
	if evs := feed(tr, `{"type":"user","message":{"role":"user","content":"hello"}}`); len(evs) != 0 {
		t.Fatalf("a plain user message is not a tool result: %v", evs)
	}
	sub := `{"type":"stream_event","parent_tool_use_id":"toolu_task","event":{"type":"message_start","message":{"usage":{"input_tokens":1}}}}`
	evs := feed(tr, sub, delta1)
	if evs[0]["parent_tool_use_id"] != "toolu_task" || evs[0]["first_in_turn"] != false {
		t.Fatalf("a sub-agent call carries its parent and never opens the turn: %v", evs[0])
	}
}

func TestTurnAccumSumsEveryCallOfATurn(t *testing.T) {
	a := newTurnAccum()
	for _, l := range []string{start1, delta1, start1, delta1} {
		a.process([]byte(l), "sess")
	}
	if a.inputTokens != 4 || a.cacheCreationInputTokens != 1000 || a.cacheReadInputTokens != 60000 || a.outputTokens != 160 {
		t.Fatalf("a turn is the sum of its calls, got %+v", a)
	}
}
