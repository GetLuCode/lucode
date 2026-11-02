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
