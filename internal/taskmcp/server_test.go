package taskmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, input string, call func(string, json.RawMessage) (string, error)) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Run(strings.NewReader(input), &out, call); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestInitializeListAndCall(t *testing.T) {
	var gotTool string
	var gotArgs string
	call := func(tool string, args json.RawMessage) (string, error) {
		gotTool, gotArgs = tool, string(args)
		return "ok, task done", nil
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"task_done","arguments":{"summary":"all good"}}}`,
	}, "\n")
	resps := roundTrip(t, in, call)

	if len(resps) != 3 { // the notification gets no reply
		t.Fatalf("want 3 responses, got %d", len(resps))
	}
	if v := resps[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Errorf("should echo the client protocol version, got %v", v)
	}
	listed := resps[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, x := range listed {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"task_comment", "task_done", "task_blocked", "agent_list", "send_to_agent", "task_info"} {
		if !names[want] {
			t.Errorf("tools/list is missing %s", want)
		}
	}
	if gotTool != "task_done" || !strings.Contains(gotArgs, "all good") {
		t.Errorf("call got tool=%q args=%q", gotTool, gotArgs)
	}
	content := resps[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if content != "ok, task done" {
		t.Errorf("tool result text = %v", content)
	}
}

func TestCallErrorIsReportedToTheAgent(t *testing.T) {
	call := func(string, json.RawMessage) (string, error) { return "", errors.New("not a task session") }
	resps := roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"task_blocked","arguments":{"reason":"x"}}}`, call)
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("a failing tool must set isError, got %v", res)
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	call := func(string, json.RawMessage) (string, error) { t.Fatal("must not be called"); return "", nil }
	resps := roundTrip(t, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm_rf","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"bogus"}`,
	}, "\n"), call)
	for i, r := range resps {
		if r["error"] == nil {
			t.Errorf("response %d should be an error: %v", i, r)
		}
	}
}

func TestIsTaskTool(t *testing.T) {
	for _, n := range []string{"task_done", "mcp__lucode__task_blocked", "task_comment", "agent_list", "mcp__lucode__send_to_agent", "task_info"} {
		if !IsTaskTool(n) {
			t.Errorf("%s should be a task tool", n)
		}
	}
	for _, n := range []string{"Bash", "mcp__other__task_done_x", "task_delete"} {
		if IsTaskTool(n) {
			t.Errorf("%s should not be a task tool", n)
		}
	}
}

func TestImageResultBecomesAnImageBlock(t *testing.T) {
	call := func(string, json.RawMessage) (string, error) { return ImageResult("image/png", "QUJD"), nil }
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_task_image","arguments":{"name":"image1"}}}`
	resps := roundTrip(t, in, call)
	block := resps[0]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["type"] != "image" || block["mimeType"] != "image/png" || block["data"] != "QUJD" {
		t.Errorf("want an image block, got %v", block)
	}
}
