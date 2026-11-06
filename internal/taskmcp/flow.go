package taskmcp

import "strings"

// Tools to prepare automations with the user: an agent reads flows and edits their drafts. A
// draft runs nothing, so these need no approval. Nothing here publishes, enables or deletes a
// flow, on purpose: a published flow can approve tools or merge branches, and an agent must not
// be able to raise its own limits. Publishing stays in the user's hands, in the editor.
//
// Like the browser tools they are opt-in: "all the lucode tools" does not include them.
const (
	ToolFlowList    = "flow_list"
	ToolFlowGet     = "flow_get"
	ToolFlowCatalog = "flow_catalog"
	ToolFlowCreate  = "flow_create"
	ToolFlowUpdate  = "flow_update"
)

// FlowNames lists the flow tools, in display order.
func FlowNames() []string {
	return []string{ToolFlowList, ToolFlowGet, ToolFlowCatalog, ToolFlowCreate, ToolFlowUpdate}
}

// IsFlowTool reports whether name is a flow tool, with or without the mcp__lucode__ prefix.
func IsFlowTool(name string) bool {
	n := strings.TrimPrefix(name, "mcp__lucode__")
	for _, f := range FlowNames() {
		if f == n {
			return true
		}
	}
	return false
}

var flowGraphSchema = map[string]any{
	"type":        "object",
	"description": `The whole graph: {"nodes":[{"id":"n1","type":"trigger.manual","x":40,"y":200,"config":{}}],"edges":[{"from":"n1","port":"next","to":"n2"}]}. Ids look like n1, n2. Lay nodes out left to right, about 300 apart on x. Types, ports and config fields come from flow_catalog.`,
	"properties": map[string]any{
		"nodes": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		"edges": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	},
	"required": []string{"nodes"},
}

var flowTools = []tool{
	{
		Name:        ToolFlowList,
		Description: "List the automations (flows) of this project: id, name, published version, whether they are on, and whether their draft waits to be published.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        ToolFlowGet,
		Description: "Read a flow: its draft graph, its state, and the problems that would stop it from being published. Read it again before editing: the user may be changing it in the editor right now.",
		InputSchema: textArg("flow", "Id or name of the flow."),
	},
	{
		Name:        ToolFlowCatalog,
		Description: "List what a flow can be made of: every node type with its ports and config fields, and the base blueprints. Call it before writing a graph.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name: ToolFlowCreate,
		Description: "Create a flow as an unpublished draft: empty, from a blueprint, or from a graph. It runs nothing until the user publishes it in the editor, and you cannot publish it. " +
			"Returns its id and the problems that would stop it from being published.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Name of the flow."},
				"blueprint": map[string]any{"type": "string", "description": "Key of a blueprint to start from (see flow_catalog)."},
				"graph":     flowGraphSchema,
			},
			"required": []string{"name"},
		},
	},
	{
		Name: ToolFlowUpdate,
		Description: "Change the draft of a flow. The user sees it at once in the editor. What is published keeps running as it is until the user publishes this draft: you cannot publish, enable or delete a flow, so tell the user when it is ready. " +
			"The graph replaces the whole draft, and the last save wins over anything the user was changing, so read it with flow_get first. Returns the problems that would stop it from being published.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow":  map[string]any{"type": "string", "description": "Id or name of the flow."},
				"name":  map[string]any{"type": "string", "description": "New name."},
				"graph": flowGraphSchema,
			},
			"required": []string{"flow"},
		},
	},
}

func init() { tools = append(tools, flowTools...) }
