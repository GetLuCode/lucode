package executor

import "testing"

func names(tools []Tool) map[string]bool {
	m := map[string]bool{}
	for _, t := range tools {
		m[t.Function.Name] = true
	}
	return m
}

func TestToolsForRequest(t *testing.T) {
	if got := toolsForRequest(TurnRequest{}); len(got) != len(Tools) {
		t.Errorf("no restriction at all must offer every tool: got %d of %d", len(got), len(Tools))
	}

	// Claude tool names restrict aiapi's own tools; lucode tools (nil list) stay
	got := names(toolsForRequest(TurnRequest{Tools: []string{"Read", "Grep"}}))
	for _, want := range []string{"read_file", "view", "list_directory", "grep", "task_done", "task_comment", "task_blocked", "agent_list", "send_to_agent", "task_info"} {
		if !got[want] {
			t.Errorf("Read+Grep should offer %s", want)
		}
	}
	for _, banned := range []string{"filesystem", "run_command"} {
		if got[banned] {
			t.Errorf("Read+Grep must not offer %s", banned)
		}
	}

	got = names(toolsForRequest(TurnRequest{Tools: []string{"Edit", "PowerShell"}}))
	if !got["filesystem"] || !got["run_command"] || got["grep"] {
		t.Errorf("Edit+PowerShell should offer filesystem and run_command only (plus lucode tools): %v", got)
	}
}

func TestLucodeToolsFollowTheAgentsList(t *testing.T) {
	got := names(toolsForRequest(TurnRequest{LucodeTools: []string{"task_done", "task_info"}}))
	if !got["task_done"] || !got["task_info"] {
		t.Errorf("the listed lucode tools are offered: %v", got)
	}
	for _, banned := range []string{"task_comment", "task_blocked", "agent_list", "send_to_agent"} {
		if got[banned] {
			t.Errorf("%s is not in the agent's lucode tools and must not be offered", banned)
		}
	}
	if !got["filesystem"] || !got["grep"] {
		t.Error("the lucode list must not affect aiapi's own tools")
	}

	got = names(toolsForRequest(TurnRequest{LucodeTools: []string{}}))
	for _, banned := range []string{"task_done", "task_comment", "task_blocked", "agent_list", "send_to_agent", "task_info"} {
		if got[banned] {
			t.Errorf("an empty list means no lucode tools at all, but %s is offered", banned)
		}
	}

	// the Claude list and the lucode list combine
	got = names(toolsForRequest(TurnRequest{Tools: []string{"Read"}, LucodeTools: []string{"task_done"}}))
	if !got["read_file"] || !got["task_done"] || got["filesystem"] || got["task_info"] {
		t.Errorf("both lists apply together: %v", got)
	}
}
