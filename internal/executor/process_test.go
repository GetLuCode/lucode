package executor

import (
	"os"
	"strings"
	"testing"
)

func TestBuildArgsToolsAlwaysIncludeToolSearch(t *testing.T) {
	args := buildArgs(TurnRequest{SessionID: "s", Model: "haiku", Tools: []string{"Read", "Bash", "ToolSearch"}})
	var got string
	for _, a := range args {
		if strings.HasPrefix(a, "--tools=") {
			got = strings.TrimPrefix(a, "--tools=")
		}
	}
	if got != "Read,Bash,ToolSearch" {
		t.Errorf("--tools = %q, want Read,Bash,ToolSearch (ToolSearch once, at the end)", got)
	}
}

func TestBuildArgsNoToolsMeansNoRestriction(t *testing.T) {
	for _, a := range buildArgs(TurnRequest{SessionID: "s", Model: "haiku"}) {
		if strings.HasPrefix(a, "--tools") {
			t.Errorf("an agent without a tool list must not pass %s", a)
		}
	}
}

func envValue(env []string, key string) (string, bool) {
	val, found := "", false
	for _, e := range env { // last one wins, as in exec
		if strings.HasPrefix(e, key+"=") {
			val, found = strings.TrimPrefix(e, key+"="), true
		}
	}
	return val, found
}

func TestBuildEnvDisablesClaudeAIConnectorsByDefault(t *testing.T) {
	t.Setenv("ENABLE_CLAUDEAI_MCP_SERVERS", "")
	os.Unsetenv("ENABLE_CLAUDEAI_MCP_SERVERS")
	if v, ok := envValue(buildEnv(TurnRequest{SessionID: "s"}), "ENABLE_CLAUDEAI_MCP_SERVERS"); !ok || v != "false" {
		t.Errorf("claude.ai connectors should be off by default, got %q (set=%v)", v, ok)
	}
}

func TestBuildEnvRespectsUserChoice(t *testing.T) {
	t.Setenv("ENABLE_CLAUDEAI_MCP_SERVERS", "true")
	if v, _ := envValue(buildEnv(TurnRequest{SessionID: "s"}), "ENABLE_CLAUDEAI_MCP_SERVERS"); v != "true" {
		t.Errorf("an explicit user setting must win, got %q", v)
	}
}

func TestResumeFailureIsNotATurn(t *testing.T) {
	failed := ParsedEvent{ResultSubtype: "error_during_execution"}
	ok := ParsedEvent{ResultSubtype: "success"}
	empty := newTurnAccum()
	used := newTurnAccum()
	used.outputTokens = 12

	if !resumeFailed(TurnRequest{Resume: true}, failed, empty) {
		t.Error("an errored result with no tokens on --resume is a failed resume")
	}
	if resumeFailed(TurnRequest{Resume: false}, failed, empty) {
		t.Error("without --resume it is some other error, not an unresumable session")
	}
	if resumeFailed(TurnRequest{Resume: true}, ok, empty) {
		t.Error("a successful result is not a failure")
	}
	if resumeFailed(TurnRequest{Resume: true}, failed, used) {
		t.Error("a turn that produced output did not fail to resume")
	}

	var events []map[string]any
	publishResumeFailure(TurnRequest{SessionID: "s"}, func(e map[string]any) { events = append(events, e) })
	if len(events) != 2 || events[0]["code"] != "resume_failed" || events[1]["status"] != "error" {
		t.Errorf("want an error event with code resume_failed then status error, got %v", events)
	}
}

func TestUsageLimitResultIsReported(t *testing.T) {
	rl, _, _ := ParseLine("s", []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1793670000}}`))
	if rl.EventType != "rate_limit" || !rl.LimitRejected || rl.ResetsAt != 1793670000 {
		t.Fatalf("rate limit event not parsed: %+v", rl)
	}
	res, isResult, _ := ParseLine("s", []byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"api_error":"usage_limit_reached","result":"You've hit your 5-hour limit"}`))
	if !isResult || !turnAPIError(res) {
		t.Fatalf("an is_error result must count as a failed turn: %+v", res)
	}
	var events []map[string]any
	code := publishTurnAPIError("s", res, rl.ResetsAt, func(e map[string]any) { events = append(events, e) })
	if code != "usage_limit" || events[0]["code"] != "usage_limit" || events[0]["resets_at"] != int64(1793670000) || events[1]["status"] != "error" {
		t.Errorf("unexpected events: %v", events)
	}
	ok, _, _ := ParseLine("s", []byte(`{"type":"result","subtype":"success","is_error":false}`))
	if turnAPIError(ok) {
		t.Error("a normal result is not an error")
	}
}

func TestAutoModeReplacesTheApprovalHook(t *testing.T) {
	has := func(args []string, want string) bool {
		for _, a := range args {
			if a == want {
				return true
			}
		}
		return false
	}
	def := buildArgs(TurnRequest{SessionID: "s", ClaudeSettings: "hooks.json"})
	if !has(def, "--settings=hooks.json") {
		t.Errorf("by default the approval hook must be passed: %v", def)
	}
	auto := buildArgs(TurnRequest{SessionID: "s", ClaudeSettings: "hooks.json", PermissionMode: "auto"})
	if !has(auto, "--permission-mode=auto") || has(auto, "--settings=hooks.json") {
		t.Errorf("auto mode passes --permission-mode and not the hook: %v", auto)
	}
}
