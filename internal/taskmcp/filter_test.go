package taskmcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func run(t *testing.T, allowed []string, input string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	call := func(string, json.RawMessage) (string, error) { return "ok", nil }
	if err := RunWithTools(strings.NewReader(input), &out, call, allowed); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		json.Unmarshal([]byte(line), &m)
		resps = append(resps, m)
	}
	return resps
}

func listed(t *testing.T, allowed []string) []string {
	resps := run(t, allowed, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var names []string
	for _, x := range resps[0]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func TestRunWithToolsListsOnlyTheAllowedOnes(t *testing.T) {
	if got := listed(t, nil); len(got) != len(Names()) {
		t.Errorf("nil means every tool, got %v", got)
	}
	got := listed(t, []string{"task_done", "task_info"})
	if strings.Join(got, ",") != "task_info,task_done" && strings.Join(got, ",") != "task_done,task_info" {
		t.Errorf("only the allowed tools are listed, got %v", got)
	}
	if got := listed(t, []string{}); len(got) != 0 {
		t.Errorf("an empty list means no tools at all, got %v", got)
	}
}

func TestRunWithToolsRefusesADisabledTool(t *testing.T) {
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"%s","arguments":{}}}`
	allowed := []string{"task_comment"}

	ok := run(t, allowed, strings.Replace(call, "%s", "task_comment", 1))
	if ok[0]["error"] != nil {
		t.Errorf("an allowed tool runs, got %v", ok[0])
	}
	denied := run(t, allowed, strings.Replace(call, "%s", "task_done", 1))
	if denied[0]["error"] == nil {
		t.Error("a tool the agent does not have must be refused even if the model asks for it by name")
	}
}

func TestNamesAreAllTaskTools(t *testing.T) {
	for _, n := range Names() {
		if !IsTaskTool(n) {
			t.Errorf("%s is listed in Names() but IsTaskTool does not know it", n)
		}
	}
}

func TestFlowToolsAreOptInAndCannotPublish(t *testing.T) {
	for _, n := range listed(t, nil) {
		if IsFlowTool(n) {
			t.Errorf("%s must not be listed when no filter is given", n)
		}
	}
	if got := listed(t, FlowNames()); len(got) != len(FlowNames()) {
		t.Errorf("asking for the flow tools lists them all, got %v", got)
	}
	for _, n := range FlowNames() {
		if !IsFlowTool("mcp__lucode__" + n) {
			t.Errorf("%s is not recognised with the mcp prefix", n)
		}
		// what a published flow can do is why an agent must not be able to publish one
		for _, banned := range []string{"publish", "enable", "delete", "pause", "run"} {
			if strings.Contains(n, banned) {
				t.Errorf("%s: no flow tool may %s", n, banned)
			}
		}
	}
}

func TestBrowserToolsAreOptIn(t *testing.T) {
	for _, n := range listed(t, nil) {
		if IsBrowserTool(n) {
			t.Errorf("%s must not be listed when no filter is given", n)
		}
	}
	if got := listed(t, BrowserNames()); len(got) != len(BrowserNames()) {
		t.Errorf("asking for the browser tools lists them all, got %v", got)
	}
	for _, n := range BrowserNames() {
		if IsTaskTool(n) {
			t.Errorf("%s must not be auto-approved like a task tool", n)
		}
		if !IsBrowserTool("mcp__lucode__" + n) {
			t.Errorf("%s is not recognised with the mcp prefix", n)
		}
	}
}
