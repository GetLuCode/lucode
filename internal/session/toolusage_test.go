package session

import (
	"encoding/json"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// The events of testdata/tool_calls.jsonl as the runner reports them, sent through JSON as
// NATS does, end up as the cost of each tool.
func TestRunnerEventsBecomeTheCostOfEachTool(t *testing.T) {
	d, err := dbpkg.Init(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m := &Manager{db: d}

	events := []string{
		`{"type":"api_call","first_in_turn":true,"model":"claude-haiku-5-5","input_tokens":2,"output_tokens":80,
		  "cache_creation_input_tokens":1000,"cache_read_input_tokens":0,"cache_creation_1h_tokens":1000,"cache_creation_5m_tokens":0,
		  "tools":[{"id":"toolu_read","name":"Read"},{"id":"toolu_grep","name":"Grep"}]}`,
		`{"type":"tool_result","tool_use_id":"toolu_read","chars":300,"images":0,"is_error":false}`,
		`{"type":"tool_result","tool_use_id":"toolu_grep","chars":100,"images":0,"is_error":true}`,
		`{"type":"api_call","first_in_turn":false,"model":"claude-haiku-5-5","input_tokens":2,"output_tokens":20,
		  "cache_creation_input_tokens":450,"cache_read_input_tokens":1000,"cache_creation_1h_tokens":450,"cache_creation_5m_tokens":0,"tools":[]}`,
	}
	for _, raw := range events {
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatal(err)
		}
		if !m.saveToolEvent("sess", "proj", ev) {
			t.Fatalf("%v must be handled as a tool event", ev["type"])
		}
	}
	if m.saveToolEvent("sess", "proj", map[string]any{"type": "turn_usage"}) {
		t.Fatal("other events are not tool events: they must go on to the chat")
	}

	rows, err := dbpkg.ToolCosts(d, time.Time{}, time.Time{}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]dbpkg.ToolCostRow{}
	for _, r := range rows {
		got[r.Tool] = r
	}
	// the context grew from 1002 to 1452 between the calls: 450 - 80 (output) = 370, split 3:1
	read, grep := got["Read"], got["Grep"]
	if read.ResultTokens != 277 || grep.ResultTokens != 92 || grep.Errors != 1 || read.Written1h != 277 || read.OutputTokens != 40 {
		t.Fatalf("Read %+v | Grep %+v", read, grep)
	}
}

// The runner's event reaches the hub as JSON over NATS: numbers are float64, lists are []any.
func TestAPICallFromEventReadsAJSONRoundTrippedEvent(t *testing.T) {
	raw := `{"type":"api_call","parent_tool_use_id":"toolu_p","first_in_turn":true,"model":"claude-sonnet-5-5",
		"input_tokens":2,"output_tokens":80,"cache_creation_input_tokens":500,"cache_read_input_tokens":30000,
		"cache_creation_1h_tokens":500,"cache_creation_5m_tokens":0,
		"tools":[{"id":"toolu_1","name":"Read"},{"id":"toolu_2","name":"Grep"},{"name":"no id"}]}`
	var ev map[string]any
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	c := apiCallFromEvent("s1", "p1", ev)
	if c.SessionID != "s1" || c.ProjectID != "p1" || c.Parent != "toolu_p" || !c.FirstInTurn || c.Model != "claude-sonnet-5-5" {
		t.Fatalf("header: %+v", c)
	}
	if c.Input != 2 || c.Output != 80 || c.CacheCreation != 500 || c.CacheRead != 30000 || c.CacheCreation1h != 500 || c.CacheCreation5m != 0 {
		t.Fatalf("usage: %+v", c)
	}
	if len(c.Tools) != 2 || c.Tools[0].ID != "toolu_1" || c.Tools[1].Name != "Grep" {
		t.Fatalf("tools (one without an id is dropped): %+v", c.Tools)
	}

	// a local event keeps its Go types
	local := apiCallFromEvent("s1", "p1", map[string]any{
		"output_tokens": 7, "tools": []map[string]any{{"id": "t", "name": "Bash"}},
	})
	if local.Output != 7 || len(local.Tools) != 1 || local.Tools[0].Name != "Bash" {
		t.Fatalf("local event: %+v", local)
	}
}
