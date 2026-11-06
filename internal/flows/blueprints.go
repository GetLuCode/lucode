package flows

import (
	"database/sql"
	"encoding/json"
	"fmt"

	dbpkg "claude-orchestrator/internal/db"
)

// Base blueprints: flows that ship with lucode as ordinary graphs, which a user can open,
// change or switch off. Seeded ones are published in every project and carry behavior that
// used to be written in Go; the rest are starting points the user copies from the editor.

// Blueprint is a base flow.
type Blueprint struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Seeded      bool   `json:"seeded"`
	Graph       Graph  `json:"-"`
}

func bpNode(id int, typ string, x, y float64, kv ...any) Node {
	config := map[string]any{}
	if def, ok := NodeTypeOf(typ); ok {
		for _, f := range def.Fields {
			if f.Default != nil {
				config[f.Name] = f.Default
			}
		}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		config[kv[i].(string)] = kv[i+1]
	}
	return Node{ID: fmt.Sprintf("n%d", id), Type: typ, X: x, Y: y, Config: config}
}

func bpEdge(from int, port string, to int) Edge {
	return Edge{From: fmt.Sprintf("n%d", from), Port: port, To: fmt.Sprintf("n%d", to)}
}

// BlueprintTaskFinished is the key of the flow that decides where a finished task goes.
const BlueprintTaskFinished = "task_finished"

// BlueprintToolApproval is the key of the flow that answers tool approvals with regexes. It is
// not seeded: it lets tools run with nobody watching, so a project has to choose it.
const BlueprintToolApproval = "tool_approval"

var blueprints = []Blueprint{
	{
		Key: BlueprintTaskFinished, Name: "Task finished", Seeded: true,
		Description: "When an agent finishes a task: unmerged work goes to Review, otherwise to Done. The supervisor is told either way.",
		Graph: Graph{
			Nodes: []Node{
				bpNode(1, TypeTaskDone, 40, 200),
				bpNode(2, TypeGitStatus, 300, 200),
				bpNode(3, TypeMoveCard, 600, 40, "to_kind", "review"),
				bpNode(4, TypeNotifySupervisor, 900, 40, "text", "Task {{card.title}} is ready for review (its branch waits to be merged): {{event.summary}}"),
				bpNode(5, TypeMoveCard, 600, 240, "to_kind", "done"),
				bpNode(6, TypeNotifySupervisor, 900, 240, "text", "Task {{card.title}} is done: {{event.summary}}"),
				bpNode(7, TypeComment, 600, 400, "text", "Could not read the branch of this task, so it waits in Review: {{nodes.n2.error}}"),
				bpNode(8, TypeMoveCard, 900, 400, "to_kind", "review"),
			},
			Edges: []Edge{
				bpEdge(1, PortNext, 2),
				bpEdge(2, PortClean, 3), bpEdge(2, PortConflicts, 3), bpEdge(2, PortDirty, 3),
				bpEdge(3, PortNext, 4),
				bpEdge(2, PortNothing, 5), bpEdge(5, PortNext, 6),
				bpEdge(2, PortFailed, 7), bpEdge(7, PortNext, 8),
			},
		},
	},
	{
		Key: "review_gate", Name: "Review with AI and approval",
		Description: "When a task enters Review: broken branches go back to the agent, Claude rates the risk, and a person approves the merge.",
		Graph: Graph{
			Nodes: []Node{
				bpNode(1, TypeCardMoved, 40, 200, "to_kind", "review"),
				bpNode(2, TypeGitStatus, 300, 200),
				bpNode(3, TypeRunAgent, 600, 40, "prompt", "Your branch {{nodes.n2.branch}} cannot be merged into {{nodes.n2.base}} yet. Merge {{nodes.n2.base}} into your branch, resolve what conflicts, commit everything, run the project's checks and call task_done again."),
				bpNode(4, TypeAIEvaluate, 600, 240, "instruction", "How risky is it to merge these changes without further review?",
					"input", "Task: {{card.title}}\n\n{{card.description}}\n\nFiles: {{nodes.n2.files}}", "outcome1", "low", "outcome2", "high"),
				bpNode(5, TypeApproval, 900, 200, "question", "Risk {{nodes.n4.outcome}} on {{card.title}}: {{nodes.n4.reason}} Merge it?"),
				bpNode(6, TypeMerge, 1200, 200),
				bpNode(7, TypeMoveCard, 900, 420, "to_kind", "blocked"),
				bpNode(8, TypeComment, 1200, 420, "text", "High risk, so it waits for a person: {{nodes.n4.reason}}"),
			},
			Edges: []Edge{
				bpEdge(1, PortNext, 2),
				bpEdge(2, PortConflicts, 3), bpEdge(2, PortDirty, 3),
				bpEdge(2, PortClean, 4),
				bpEdge(4, PortOutcome1, 5),
				bpEdge(5, PortApproved, 6),
				bpEdge(6, PortConflicts, 3),
				bpEdge(4, PortOutcome2, 7), bpEdge(7, PortNext, 8),
			},
		},
	},
	{
		Key: "conflicts_back", Name: "Send conflicts back to the agent",
		Description: "When a task enters Review with a branch that conflicts or has uncommitted work, its agent gets it back with instructions.",
		Graph: Graph{
			Nodes: []Node{
				bpNode(1, TypeCardMoved, 40, 120, "to_kind", "review"),
				bpNode(2, TypeGitStatus, 300, 120),
				bpNode(3, TypeRunAgent, 600, 120, "prompt", "Your branch {{nodes.n2.branch}} cannot be merged into {{nodes.n2.base}}. Merge {{nodes.n2.base}} into your branch, resolve what conflicts, commit everything, run the project's checks and call task_done again."),
			},
			Edges: []Edge{bpEdge(1, PortNext, 2), bpEdge(2, PortConflicts, 3), bpEdge(2, PortDirty, 3)},
		},
	},
	{
		Key: BlueprintToolApproval, Name: "Approve tools with rules",
		Description: "When an agent asks for a tool no rule covers: Read, Glob and Grep pass, Bash commands that match a dangerous pattern are denied, and the rest asks you as usual. Change the regexes to fit your project.",
		Graph: Graph{
			Nodes: []Node{
				bpNode(1, TypeToolRequest, 40, 200),
				bpNode(2, TypeMatchTool, 300, 200, "tool", "Read|Glob|Grep"),
				bpNode(3, TypeToolDecision, 600, 40, "decision", ToolAllow, "reason", "{{event.tool_name}} only reads"),
				bpNode(4, TypeMatchTool, 600, 240, "tool", "Bash",
					"input_regex", `command: (?i)\brm\s+-[a-z]*r|git\s+push\s.*--force|git\s+reset\s+--hard|curl[^|]*\|\s*(ba)?sh`),
				bpNode(5, TypeToolDecision, 900, 120, "decision", ToolDeny, "reason", "this command matches a dangerous pattern of the project's flow"),
				bpNode(6, TypeToolDecision, 900, 360, "decision", ToolAsk),
			},
			Edges: []Edge{
				bpEdge(1, PortNext, 2),
				bpEdge(2, PortMatch, 3), bpEdge(2, PortNoMatch, 4),
				bpEdge(4, PortMatch, 5), bpEdge(4, PortNoMatch, 6),
			},
		},
	},
}

// Blueprints lists the base flows.
func Blueprints() []Blueprint { return blueprints }

// BlueprintOf finds a base flow.
func BlueprintOf(key string) (Blueprint, bool) {
	for _, b := range blueprints {
		if b.Key == key {
			return b, true
		}
	}
	return Blueprint{}, false
}

// SeedBlueprints publishes the seeded flows in a project, once: a flow the user deleted does
// not come back.
func SeedBlueprints(db *sql.DB, projectID string) error {
	for _, b := range blueprints {
		if !b.Seeded {
			continue
		}
		first, err := dbpkg.ClaimFlowSeed(db, projectID, b.Key)
		if err != nil {
			return err
		}
		if !first {
			continue
		}
		raw, err := json.Marshal(b.Graph)
		if err != nil {
			return err
		}
		f, err := dbpkg.InsertFlow(db, dbpkg.Flow{ProjectID: projectID, Name: b.Name, Draft: raw})
		if err != nil {
			return err
		}
		if _, err := dbpkg.PublishFlow(db, f.ID, raw); err != nil {
			return err
		}
	}
	return nil
}

// Handles reports whether something will react to a trigger type in a project right now: a
// published, enabled flow has that trigger and the project is not paused.
func (e *Engine) Handles(projectID, triggerType string) bool {
	if dbpkg.FlowsPaused(e.db, projectID) {
		return false
	}
	active, err := dbpkg.ActiveFlows(e.db, projectID)
	if err != nil {
		return false
	}
	for _, f := range active {
		g, err := e.graph(f.ID, f.PublishedVersion)
		if err != nil {
			continue
		}
		for _, n := range g.Nodes {
			if n.Type == triggerType {
				return true
			}
		}
	}
	return false
}
