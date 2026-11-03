package api

import (
	"testing"

	"claude-orchestrator/internal/taskmcp"
)

func TestOrchestratorToolsSkipTaskReporting(t *testing.T) {
	got := map[string]bool{}
	for _, n := range orchestratorTools() {
		got[n] = true
	}
	for _, n := range []string{taskmcp.ToolComment, taskmcp.ToolDone, taskmcp.ToolBlocked} {
		if got[n] {
			t.Errorf("orchestrator should not have %s", n)
		}
	}
	for _, n := range []string{taskmcp.ToolTaskCreate, taskmcp.ToolTaskUpdate, taskmcp.ToolAgentCreate, taskmcp.ToolSendToAgent} {
		if !got[n] {
			t.Errorf("orchestrator should have %s", n)
		}
	}
}
