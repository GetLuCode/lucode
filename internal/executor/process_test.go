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
