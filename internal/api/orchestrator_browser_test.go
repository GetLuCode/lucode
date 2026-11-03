package api

import (
	"testing"

	"claude-orchestrator/internal/aimode"
	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
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

func TestOrchestratorLocalModelRunsNoClaude(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	h.bus = natspkg.NewBus(nil) // only SelfInfo is used
	proj, _ := dbpkg.GetProject(h.db, pid)
	sess := dbpkg.Session{ID: "s1", ProjectID: pid, Model: "sonnet", Kind: dbpkg.SessionKindOrchestrator}
	opts, err := h.orchestratorTurnOpts(sess, proj, "hola", aimode.Model, "kali", "ai-x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Executor != "aimode" || opts.Model != aimode.Model || opts.Message != "hola" || opts.Persistent || opts.RunnerID != "" || len(opts.LucodeTools) != 0 {
		t.Errorf("a local model runs on the hub with no claude, got %+v", opts)
	}
	// any other model still goes to claude
	opts, err = h.orchestratorTurnOpts(sess, proj, "hola", "haiku", "", "", "", "")
	if err != nil || opts.Executor != "claude" || !opts.Persistent {
		t.Errorf("haiku: %+v %v", opts, err)
	}
}
