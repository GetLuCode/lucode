package executor

// claudeToolMap translates the Claude Code tool names an agent is configured
// with into aiapi's own tools, which are named and grouped differently.
var claudeToolMap = map[string][]string{
	"Read":         {"read_file", "view", "list_directory"},
	"Glob":         {"list_directory"},
	"Grep":         {"grep"},
	"Edit":         {"filesystem"},
	"Write":        {"filesystem"},
	"NotebookEdit": {"filesystem"},
	"Bash":         {"run_command"},
	"PowerShell":   {"run_command"},
}

// toolsForRequest returns the tools offered to the model for this turn.
//
// The agent's Claude Code tool list (req.Tools) restricts aiapi's own tools
// through claudeToolMap; empty means no restriction. The lucode tools (task_*,
// agent_list, send_to_agent, task_info) follow req.LucodeTools: nil means all of
// them, an explicit list only those.
func toolsForRequest(req TurnRequest) []Tool {
	allowed := map[string]bool{}
	for _, name := range req.Tools {
		for _, t := range claudeToolMap[name] {
			allowed[t] = true
		}
	}
	lucode := map[string]bool{}
	for _, name := range req.LucodeTools {
		lucode[name] = true
	}

	out := make([]Tool, 0, len(Tools))
	for _, t := range Tools {
		name := t.Function.Name
		if isTaskTool(name) {
			if req.LucodeTools == nil || lucode[name] {
				out = append(out, t)
			}
			continue
		}
		if len(req.Tools) == 0 || allowed[name] {
			out = append(out, t)
		}
	}
	return out
}
