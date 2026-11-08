package flows

// The base nodes. The hub owns this list: the validator and the engine use it, and the
// editor builds its palette and its forms from it, so a new node only needs adding here
// and in the engine's execute.
//
// Flows work like a node graph of values: a node gives its answer through one of its
// outputs, and that answer is what reaches the input of the nodes connected to it and
// starts them. There is no separate "next" wire. A node's answer is plain text unless the
// node says it is JSON; a JSON answer is read with a Get JSON prop node, never as a variable.

const (
	CategoryTrigger = "trigger"
	CategoryControl = "control"
	CategoryAction  = "action"
	CategoryData    = "data"

	// The kinds of value a pin carries. "any" accepts and gives anything.
	PinText = "text"
	PinJSON = "json"
	PinAny  = "any"

	// InputPin is the name of the input of every node that is not a trigger.
	InputPin = "input"

	FieldText     = "text"
	FieldTemplate = "template" // text with {{ expressions }}
	FieldExpr     = "expr"     // an expression
	FieldNumber   = "number"
	FieldSelect   = "select"
	FieldAgent    = "agent" // the name of an agent of the project
	FieldList     = "list"  // the id of a list of the project
	FieldBool     = "bool"
	FieldLines    = "lines" // several lines of literal text, such as regexes

	TypeCardMoved   = "trigger.card_moved"
	TypeCardCreated = "trigger.card_created"
	TypeTurnEnded   = "trigger.turn_ended"
	TypeTurnFailed  = "trigger.turn_failed"
	TypeTaskDone    = "trigger.task_done"
	TypeManual      = "trigger.manual"
	TypeSchedule    = "trigger.schedule"
	TypeToolRequest = "trigger.tool_requested"
	TypePluginEvent = "trigger.plugin_event"

	TypeIf        = "control.if"
	TypeSwitch    = "control.switch"
	TypeParallel  = "control.parallel"
	TypeJoin      = "control.join"
	TypeWaitEvent = "control.wait_event"
	TypeDelay     = "control.delay"
	TypeRetry     = "control.retry"
	TypeStop      = "control.stop"
	TypeApproval  = "control.human_approval"
	TypeMatchTool = "control.match_tool"

	TypeRunAgent   = "action.run_agent"
	TypeMoveCard   = "action.move_card"
	TypeComment    = "action.comment"
	TypeCreateTask = "action.create_task"
	TypeAddTag     = "action.add_tag"
	TypeAIEvaluate = "action.ai_evaluate"
	TypeGitStatus  = "action.git_status"
	TypeMerge      = "action.merge"

	TypeNotifySupervisor = "action.notify_supervisor"
	TypeToolDecision     = "action.tool_decision"
	TypePushNotification = "action.push_notification"

	TypeGetJSONProp = "data.get_json_prop"

	PortValue     = "value"   // Get JSON prop: the value read
	PortOutput    = "output"  // the input, passed on unchanged
	PortReply     = "reply"   // Run agent: the agent's last message
	PortText      = "text"    // the text a node wrote
	PortTag       = "tag"     // Add tag: the tag
	PortCardID    = "card_id" // Create task: the new card
	PortResult    = "result"  // a JSON result
	PortMoved     = "moved"   // Move card: the input, passed on
	PortValues    = "values"  // Join: the values that arrived, as a JSON list
	PortTrue      = "true"
	PortFalse     = "false"
	PortAgain     = "again"
	PortExhausted = "exhausted"
	PortTimeout   = "timeout"
	PortEvent     = "event"
	PortFailed    = "failed"
	PortRejected  = "rejected"
	PortDefault   = "default"
	PortApproved  = "approved"
	PortClean     = "clean"
	PortConflicts = "conflicts"
	PortDirty     = "dirty"
	PortNothing   = "nothing"
	PortMerged    = "merged"
	PortOutcome1  = "outcome1"
	PortOutcome2  = "outcome2"
	PortOutcome3  = "outcome3"
	PortMatch     = "match"
	PortNoMatch   = "no_match"
)

// Field is an input of a node.
type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default,omitempty"`
	Help     string   `json:"help,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	// PortLabel: the value of this field is the name shown on that output of the node.
	PortLabel string `json:"port_label,omitempty"`
}

// Pin is an input or an output of a node and the kind of value it carries.
type Pin struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// NodeType describes a kind of node.
type NodeType struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Description string `json:"description"`
	// Outputs: each one carries the node's answer when the node takes it. Ports are their
	// names, in the same order (plugins only give Ports: their outputs carry anything).
	Outputs []Pin    `json:"outputs"`
	Ports   []string `json:"ports"`
	// Input: what starts the node. Triggers have none.
	Input  *Pin    `json:"input,omitempty"`
	Fields []Field `json:"fields"`
	// Repeatable: running it twice does no harm, so it can run again after a hub stop.
	Repeatable bool `json:"-"`
	// A plugin's node. Disabled: its plugin is off, so a run fails on it, but the editor keeps it.
	PluginID       string `json:"plugin_id,omitempty"`
	PluginName     string `json:"plugin_name,omitempty"`
	Icon           string `json:"icon,omitempty"`
	Disabled       bool   `json:"plugin_disabled,omitempty"`
	Target         string `json:"target,omitempty"`
	TimeoutSeconds int    `json:"-"`
}

var kindOptions = []string{"any", "backlog", "todo", "inprogress", "review", "done", "blocked"}
var statusOptions = []string{"backlog", "todo", "inprogress", "review", "done", "blocked"}
var eventOptions = []string{"card.moved", "card.created", "turn.ended", "turn.failed", "task.done"}

func minOf(v float64) *float64 { return &v }

// outs lists the outputs of a node as name, type pairs.
func outs(pairs ...string) []Pin {
	out := make([]Pin, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Pin{Name: pairs[i], Type: pairs[i+1]})
	}
	return out
}

// the output every trigger has: what happened, as JSON
var triggerOut = outs(PortEvent, PinJSON)

const inputHelp = "{{input}} is the value that reached this node, as text."

var nodeTypes = []NodeType{
	{
		Type: TypeCardMoved, Title: "Card moved", Category: CategoryTrigger,
		Description: "A card changes list. Gives the event as JSON: to_kind, from_kind, to_list...",
		Outputs:     triggerOut,
		Fields: []Field{
			{Name: "to_kind", Label: "Enters", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "from_kind", Label: "Comes from", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "to_list", Label: "Only this list", Kind: FieldList, Help: "For lists you created. It replaces \"Enters\"."},
		},
		Repeatable: true,
	},
	{
		Type: TypeCardCreated, Title: "Card created", Category: CategoryTrigger,
		Description: "A new card appears on the board. Gives the event as JSON.",
		Outputs:     triggerOut,
		Fields:      []Field{{Name: "to_kind", Label: "In", Kind: FieldSelect, Options: kindOptions, Default: "any"}},
		Repeatable:  true,
	},
	{
		Type: TypeTurnEnded, Title: "Turn ended", Category: CategoryTrigger,
		Description: "An agent finishes a turn on a card. Gives the event as JSON; reply is the agent's last message.",
		Outputs:     triggerOut,
		Repeatable:  true,
	},
	{
		Type: TypeTurnFailed, Title: "Turn failed", Category: CategoryTrigger,
		Description: "A turn on a card could not run. Gives the event as JSON: code, message.",
		Outputs:     triggerOut,
		Repeatable:  true,
	},
	{
		Type: TypeTaskDone, Title: "Task done", Category: CategoryTrigger,
		Description: "An agent calls task_done. A flow with this trigger decides where the task goes; if none is active, it waits in Review. Gives the event as JSON; summary is what the agent reported.",
		Outputs:     triggerOut,
		Repeatable:  true,
	},
	{
		Type: TypeManual, Title: "Manual", Category: CategoryTrigger,
		Description: "You start it from the editor, optionally on a card.",
		Outputs:     triggerOut,
		Repeatable:  true,
	},
	{
		Type: TypeSchedule, Title: "Schedule", Category: CategoryTrigger,
		Description: "Every so many minutes. It has no card.",
		Outputs:     triggerOut,
		Fields:      []Field{{Name: "minutes", Label: "Every (minutes)", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(1)}},
		Repeatable:  true,
	},
	{
		Type: TypeToolRequest, Title: "Tool requested", Category: CategoryTrigger,
		Description: "An agent wants to use a tool and no permission rule decided it. The flow has a minute to reach a Tool decision. Without one, a person is asked, or Claude's auto mode decides if the agent runs in it. Gives the request as JSON: tool_name, tool_input, tool_input_text, work_dir, task, agent, auto_mode.",
		Outputs:     triggerOut,
		Repeatable:  true,
	},
	{
		Type: TypePluginEvent, Title: "Plugin event", Category: CategoryTrigger,
		Description: "A plugin fires an event with lucode.flows.emit. Gives the event as JSON: plugin, event, payload.",
		Outputs:     triggerOut,
		Fields: []Field{
			{Name: "plugin", Label: "Plugin", Kind: FieldText, Required: true, Help: "The id of the plugin, such as acme.hello."},
			{Name: "event", Label: "Event", Kind: FieldText, Required: true},
		},
		Repeatable: true,
	},

	{
		Type: TypeIf, Title: "If", Category: CategoryControl,
		Description: "Takes one of two paths and passes its input on.",
		Outputs:     outs(PortTrue, PinAny, PortFalse, PinAny),
		Fields:      []Field{{Name: "condition", Label: "Condition", Kind: FieldExpr, Required: true, Help: `For example: input == "yes" or "urgent" in card.tags. input is the value that reached this node, as text.`}},
		Repeatable:  true,
	},
	{
		Type: TypeSwitch, Title: "Switch", Category: CategoryControl,
		Description: "Takes the path of the case equal to a value and passes its input on.",
		Outputs:     outs("case1", PinAny, "case2", PinAny, "case3", PinAny, PortDefault, PinAny),
		Fields: []Field{
			{Name: "value", Label: "Value", Kind: FieldExpr, Required: true, Default: "input", Help: "For example: input or card.type"},
			{Name: "case1", Label: "Case 1", Kind: FieldText, PortLabel: "case1"},
			{Name: "case2", Label: "Case 2", Kind: FieldText, PortLabel: "case2"},
			{Name: "case3", Label: "Case 3", Kind: FieldText, PortLabel: "case3"},
		},
		Repeatable: true,
	},
	{
		Type: TypeParallel, Title: "Parallel", Category: CategoryControl,
		Description: "Starts several paths at once, each with its input.",
		Outputs:     outs("a", PinAny, "b", PinAny, "c", PinAny),
		Repeatable:  true,
	},
	{
		Type: TypeJoin, Title: "Join", Category: CategoryControl,
		Description: "Continues when every connected path has arrived. Gives what they brought as a JSON list, in the order they are connected.",
		Outputs:     outs(PortValues, PinJSON),
		Repeatable:  true,
	},
	{
		Type: TypeWaitEvent, Title: "Wait for event", Category: CategoryControl,
		Description: "Waits for something to happen on the run's card. \"event\" gives that event as JSON; \"timeout\" passes the input on.",
		Outputs:     outs(PortEvent, PinJSON, PortTimeout, PinAny),
		Fields: []Field{
			{Name: "event", Label: "Event", Kind: FieldSelect, Options: eventOptions, Required: true, Default: "card.moved"},
			{Name: "to_kind", Label: "Enters (card moved)", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "timeout_minutes", Label: "Give up after (minutes)", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(1)},
		},
	},
	{
		Type: TypeDelay, Title: "Delay", Category: CategoryControl,
		Description: "Waits, then passes its input on.",
		Outputs:     outs(PortOutput, PinAny),
		Fields:      []Field{{Name: "seconds", Label: "Seconds", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(0)}},
		Repeatable:  true,
	},
	{
		Type: TypeRetry, Title: "Retry", Category: CategoryControl,
		Description: "Goes back up to a number of times, then gives up, passing its input on. It is the only way to make a loop.",
		Outputs:     outs(PortAgain, PinAny, PortExhausted, PinAny),
		Fields:      []Field{{Name: "max", Label: "Times", Kind: FieldNumber, Required: true, Default: 3, Min: minOf(1)}},
		Repeatable:  true,
	},
	{
		Type: TypeStop, Title: "Stop", Category: CategoryControl,
		Description: "Ends the run.",
		Fields: []Field{
			{Name: "result", Label: "Result", Kind: FieldSelect, Options: []string{RunSucceededName, RunFailedName}, Default: RunSucceededName},
			{Name: "message", Label: "Message", Kind: FieldTemplate},
		},
		Repeatable: true,
	},
	{
		Type: TypeApproval, Title: "Human approval", Category: CategoryControl,
		Description: "Stops the flow until someone decides, from the bell or from the run. It can wait for days. \"approved\" and \"rejected\" give the note the person wrote; \"timeout\" passes the input on.",
		Outputs:     outs(PortApproved, PinText, PortRejected, PinText, PortTimeout, PinAny),
		Fields: []Field{
			{Name: "question", Label: "Question", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: "For example: Merge {{card.title}}? " + inputHelp},
			{Name: "timeout_minutes", Label: "Give up after (minutes)", Kind: FieldNumber, Min: minOf(1), Help: "Empty: it waits until someone answers."},
		},
	},

	{
		Type: TypeGetJSONProp, Title: "Get JSON prop", Category: CategoryData,
		Description: "Reads a property of the JSON that reaches it and gives its value. Takes \"failed\" if the input is not JSON or the property is not there.",
		Input:       &Pin{Name: "json", Type: PinJSON},
		Outputs:     outs(PortValue, PinAny, PortFailed, PinText),
		Fields: []Field{
			{Name: "template", Label: "Value", Kind: FieldText, Required: true, Default: "{json.summary}", Help: "{json.myprop} reads a property, {json.a.b} goes deeper and {json.files.0} reads a list. Alone it keeps the value as it is (text, number or JSON); mixed with text, as in Branch {json.branch}, it gives text. {json} is the whole input."},
		},
		Repeatable: true,
	},

	{
		Type: TypeAIEvaluate, Title: "AI evaluate", Category: CategoryAction,
		Description: "Asks Claude through the Anthropic API to choose one answer, and follows that output, which gives the model's reason as text. It reads only the text you give it and spends API tokens.",
		Outputs:     outs(PortOutcome1, PinText, PortOutcome2, PinText, PortOutcome3, PinText, PortFailed, PinText),
		Fields: []Field{
			{Name: "instruction", Label: "Instruction", Kind: FieldTemplate, Required: true, Help: "What to decide. For example: How risky are these changes?"},
			{Name: "input", Label: "Text to evaluate", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: "Claude treats this as data, not as instructions. " + inputHelp},
			{Name: "outcome1", Label: "Answer 1", Kind: FieldText, Required: true, Default: "yes", PortLabel: PortOutcome1},
			{Name: "outcome2", Label: "Answer 2", Kind: FieldText, Required: true, Default: "no", PortLabel: PortOutcome2},
			{Name: "outcome3", Label: "Answer 3", Kind: FieldText, PortLabel: PortOutcome3},
			{Name: "model", Label: "Model", Kind: FieldSelect, Options: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, Default: "claude-opus-5-5", Required: true},
			{Name: "effort", Label: "Effort", Kind: FieldSelect, Options: []string{"low", "medium", "high"}, Default: "medium", Required: true},
		},
	},
	{
		Type: TypeGitStatus, Title: "Git status", Category: CategoryAction,
		Description: "Looks at the card's branch on its runner and changes nothing. Takes \"nothing\" when there is no branch or nothing left to merge. Every output but \"failed\" gives JSON: state, branch, base, ahead, files, conflict_files, worktree_path.",
		Outputs:     outs(PortClean, PinJSON, PortConflicts, PinJSON, PortDirty, PinJSON, PortNothing, PinJSON, PortFailed, PinText),
		Repeatable:  true,
	},
	{
		Type: TypeMerge, Title: "Merge", Category: CategoryAction,
		Description: "Merges the card's branch into the branch of the main checkout, on its runner. If git refuses it changes nothing and takes \"conflicts\". \"merged\" and \"conflicts\" give what the runner reported, as JSON.",
		Outputs:     outs(PortMerged, PinJSON, PortConflicts, PinJSON, PortFailed, PinText),
		Fields: []Field{
			{Name: "strategy", Label: "How", Kind: FieldSelect, Options: []string{"merge", "squash", "ff-only"}, Default: "merge", Required: true, Help: "merge keeps a merge commit, squash leaves one commit with all the work, ff-only refuses if the base branch moved."},
			{Name: "message", Label: "Commit message", Kind: FieldTemplate, Help: "Empty: Merge task: <title>. ff-only makes no commit."},
			{Name: "delete_worktree", Label: "Delete the worktree", Kind: FieldBool, Default: true},
			{Name: "delete_branch", Label: "Delete the branch", Kind: FieldBool, Default: true, Help: "Needs the worktree deleted too: git keeps a branch a worktree is using."},
			{Name: "move_to_done", Label: "Move the card to Done", Kind: FieldBool, Default: true, Help: "This also releases the tasks that depend on it."},
			{Name: "unattended", Label: "Merge without a person", Kind: FieldBool, Default: false, Help: "Off: publishing requires the \"approved\" output of a Human approval on every path to this node."},
		},
	},
	{
		Type: TypeMatchTool, Title: "Match tool", Category: CategoryControl,
		Description: "Tests the requested tool with regexes, the same way permission rules do. Takes \"match\" if the tool and every parameter line match, and passes its input on. Only for Tool requested flows.",
		Outputs:     outs(PortMatch, PinAny, PortNoMatch, PinAny),
		Fields: []Field{
			{Name: "tool", Label: "Tool", Kind: FieldText, Default: "*", Help: "A regex for the whole name: Read|Glob|Grep or mcp__github__.*. * is any tool."},
			{Name: "input_regex", Label: "Parameters", Kind: FieldLines, Help: "One per line, parameter: regex. For example command: ^go test ./[^;&|]*$ . The regex may match part of the text, so without ^ and $, or if it allows ; & |, go test also matches go test && rm -rf /."},
		},
		Repeatable: true,
	},
	{
		Type: TypeToolDecision, Title: "Tool decision", Category: CategoryAction,
		Description: "Answers the Tool requested trigger and ends the run. \"ask\" shows the usual approval to a person, or leaves it to Claude's auto mode if the agent runs in it. A deny also stops the agent's other tools for the rest of the turn.",
		Fields: []Field{
			{Name: "decision", Label: "Decision", Kind: FieldSelect, Options: []string{ToolAllow, ToolDeny, ToolAsk}, Required: true, Default: ToolAsk},
			{Name: "reason", Label: "Reason", Kind: FieldTemplate, Help: "The agent reads it, and it stays in the session's history."},
		},
	},
	{
		Type: TypeNotifySupervisor, Title: "Notify supervisor", Category: CategoryAction,
		Description: "Tells the agent the card's agent reports to. Does nothing if it reports to nobody. Gives the text it sent.",
		Outputs:     outs(PortText, PinText),
		Fields:      []Field{{Name: "text", Label: "Message", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: "For example: {{card.title}} is done. " + inputHelp}},
	},
	{
		Type: TypePushNotification, Title: "Push notification", Category: CategoryAction,
		Description: "Notifies every device that turned on \"Notifications on this device\" in this tab, even with lucode closed. Clicking it opens this run. Takes \"failed\" if no device could be reached. \"result\" gives JSON: sent, failed, removed.",
		Outputs:     outs(PortResult, PinJSON, PortFailed, PinText),
		Fields: []Field{
			{Name: "body", Label: "Message", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: "For example: {{card.title}} is ready for review. Long messages are cut. " + inputHelp},
			{Name: "title", Label: "Title", Kind: FieldTemplate, Required: true, Default: "lucode"},
		},
		// sent again it replaces the first one on the device, because the tag is the step's
		Repeatable: true,
	},
	{
		Type: TypeRunAgent, Title: "Run agent", Category: CategoryAction,
		Description: "Moves the card to In Progress and gives its agent an instruction on a runner. When the turn ends, \"reply\" gives the agent's last message.",
		Outputs:     outs(PortReply, PinText, PortFailed, PinText),
		Fields: []Field{
			{Name: "agent", Label: "Agent", Kind: FieldAgent, Help: "Empty: the agent the card already has."},
			{Name: "prompt", Label: "Instruction", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: inputHelp},
		},
	},
	{
		Type: TypeMoveCard, Title: "Move card", Category: CategoryAction,
		Description: "Moves the card under the board's rules and passes its input on. Takes \"rejected\" if a rule refuses it, with the reason.",
		Outputs:     outs(PortMoved, PinAny, PortRejected, PinText),
		Fields: []Field{
			{Name: "to_kind", Label: "To", Kind: FieldSelect, Options: statusOptions},
			{Name: "to_list", Label: "Or this list", Kind: FieldList, Help: "For lists you created. It replaces \"To\"."},
		},
		Repeatable: true,
	},
	{
		Type: TypeComment, Title: "Comment", Category: CategoryAction,
		Description: "Adds a note to the card. Gives the text it wrote.",
		Outputs:     outs(PortText, PinText),
		Fields:      []Field{{Name: "text", Label: "Text", Kind: FieldTemplate, Required: true, Default: "{{input}}", Help: inputHelp}},
	},
	{
		Type: TypeCreateTask, Title: "Create task", Category: CategoryAction,
		Description: "Adds a card to the board. Gives the id of the new card.",
		Outputs:     outs(PortCardID, PinText),
		Fields: []Field{
			{Name: "title", Label: "Title", Kind: FieldTemplate, Required: true},
			{Name: "description", Label: "Description", Kind: FieldTemplate, Help: inputHelp},
			{Name: "status", Label: "List", Kind: FieldSelect, Options: statusOptions, Default: "todo"},
			{Name: "agent", Label: "Agent", Kind: FieldAgent},
		},
	},
	{
		Type: TypeAddTag, Title: "Add tag", Category: CategoryAction,
		Description: "Adds a tag to the card. Gives the tag.",
		Outputs:     outs(PortTag, PinText),
		Fields:      []Field{{Name: "tag", Label: "Tag", Kind: FieldTemplate, Required: true}},
		Repeatable:  true,
	},
}

func init() {
	for i := range nodeTypes {
		nodeTypes[i] = withPins(nodeTypes[i])
	}
}

// withPins fills what a node type left out: the output names from its outputs (or, for a
// plugin's node, outputs from its names) and the input of every node but a trigger.
func withPins(t NodeType) NodeType {
	if len(t.Outputs) == 0 && len(t.Ports) > 0 {
		for _, p := range t.Ports {
			typ := PinAny
			if p == PortFailed {
				typ = PinText
			}
			t.Outputs = append(t.Outputs, Pin{Name: p, Type: typ})
		}
	}
	if t.Outputs == nil {
		t.Outputs = []Pin{}
	}
	t.Ports = make([]string, 0, len(t.Outputs))
	for _, o := range t.Outputs {
		t.Ports = append(t.Ports, o.Name)
	}
	if t.Input == nil && t.Category != CategoryTrigger {
		t.Input = &Pin{Name: InputPin, Type: PinAny}
	}
	return t
}

// OutputType is the kind of value an output of a node type gives.
func (t NodeType) OutputType(port string) string {
	for _, o := range t.Outputs {
		if o.Name == port {
			return o.Type
		}
	}
	return PinAny
}

// Values of Stop's result field.
const (
	RunSucceededName = "success"
	RunFailedName    = "failure"
)

// NodeTypes lists the base nodes and those of the installed plugins.
func NodeTypes() []NodeType {
	src := currentPlugins()
	if src == nil {
		return nodeTypes
	}
	out := append([]NodeType{}, nodeTypes...)
	for _, n := range src.NodeTypes() {
		out = append(out, withPins(n))
	}
	return out
}

// NodeTypeOf finds a node type.
func NodeTypeOf(t string) (NodeType, bool) {
	if IsPluginNode(t) {
		if src := currentPlugins(); src != nil {
			for _, n := range src.NodeTypes() {
				if n.Type == t {
					return withPins(n), true
				}
			}
		}
		return NodeType{}, false
	}
	for _, n := range nodeTypes {
		if n.Type == t {
			return n, true
		}
	}
	return NodeType{}, false
}
