package api

import (
	"testing"

	"claude-orchestrator/internal/taskmcp"
)

func TestOrchestratorHasBrowserToolsButAgentsDoNotByDefault(t *testing.T) {
	has := map[string]bool{}
	for _, n := range orchestratorTools() {
		has[n] = true
	}
	for _, n := range taskmcp.BrowserNames() {
		if !has[n] {
			t.Errorf("the orchestrator should have %s", n)
		}
	}
	if got := sanitizeLucodeTools([]string{"browser_click", "task_info", "rm_rf"}); len(got) != 2 {
		t.Errorf("browser tools are known lucode tools, got %v", got)
	}
}
