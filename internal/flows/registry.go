package flows

// The base nodes. The hub owns this list: the validator and the engine use it, and the
// editor builds its palette and its forms from it, so a new node only needs adding here
// and in the engine's execute.

const (
	CategoryTrigger = "trigger"
	CategoryControl = "control"
	CategoryAction  = "action"

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

	PortNext      = "next"
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

// NodeType describes a kind of node.
type NodeType struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Ports       []string `json:"ports"`
	Fields      []Field  `json:"fields"`
	// Repeatable: running it twice does no harm, so it can run again after a hub stop.
	Repeatable bool `json:"-"`
}

var kindOptions = []string{"any", "backlog", "todo", "inprogress", "review", "done", "blocked"}
var statusOptions = []string{"backlog", "todo", "inprogress", "review", "done", "blocked"}
var eventOptions = []string{"card.moved", "card.created", "turn.ended", "turn.failed", "task.done"}

func minOf(v float64) *float64 { return &v }

var nodeTypes = []NodeType{
	{
		Type: TypeCardMoved, Title: "Card moved", Category: CategoryTrigger,
		Description: "A card changes list.",
		Ports:       []string{PortNext},
		Fields: []Field{
			{Name: "to_kind", Label: "Enters", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "from_kind", Label: "Comes from", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "to_list", Label: "Only this list", Kind: FieldList, Help: "For lists you created. It replaces \"Enters\"."},
		},
		Repeatable: true,
	},
	{
		Type: TypeCardCreated, Title: "Card created", Category: CategoryTrigger,
		Description: "A new card appears on the board.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "to_kind", Label: "In", Kind: FieldSelect, Options: kindOptions, Default: "any"}},
		Repeatable:  true,
	},
	{
		Type: TypeTurnEnded, Title: "Turn ended", Category: CategoryTrigger,
		Description: "An agent finishes a turn on a card.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},
	{
		Type: TypeTurnFailed, Title: "Turn failed", Category: CategoryTrigger,
		Description: "A turn on a card could not run.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},
	{
		Type: TypeTaskDone, Title: "Task done", Category: CategoryTrigger,
		Description: "An agent calls task_done. A flow with this trigger decides where the task goes; if none is active, it waits in Review. Reads event.summary.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},
	{
		Type: TypeManual, Title: "Manual", Category: CategoryTrigger,
		Description: "You start it from the editor, optionally on a card.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},
	{
		Type: TypeSchedule, Title: "Schedule", Category: CategoryTrigger,
		Description: "Every so many minutes. It has no card.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "minutes", Label: "Every (minutes)", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(1)}},
		Repeatable:  true,
	},
	{
		Type: TypeToolRequest, Title: "Tool requested", Category: CategoryTrigger,
		Description: "An agent wants to use a tool and no permission rule decided it. The flow has a minute to reach a Tool decision. Without one, a person is asked, or Claude's auto mode decides if the agent runs in it. Reads event.tool_name, event.tool_input, event.tool_input_text, event.work_dir, event.task, event.agent, event.auto_mode.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},

	{
		Type: TypeIf, Title: "If", Category: CategoryControl,
		Description: "Takes one of two paths.",
		Ports:       []string{PortTrue, PortFalse},
		Fields:      []Field{{Name: "condition", Label: "Condition", Kind: FieldExpr, Required: true, Help: `For example: "urgent" in card.tags`}},
		Repeatable:  true,
	},
	{
		Type: TypeSwitch, Title: "Switch", Category: CategoryControl,
		Description: "Takes the path of the case equal to a value.",
		Ports:       []string{"case1", "case2", "case3", PortDefault},
		Fields: []Field{
			{Name: "value", Label: "Value", Kind: FieldExpr, Required: true, Help: "For example: card.type"},
			{Name: "case1", Label: "Case 1", Kind: FieldText, PortLabel: "case1"},
			{Name: "case2", Label: "Case 2", Kind: FieldText, PortLabel: "case2"},
			{Name: "case3", Label: "Case 3", Kind: FieldText, PortLabel: "case3"},
		},
		Repeatable: true,
	},
	{
		Type: TypeParallel, Title: "Parallel", Category: CategoryControl,
		Description: "Starts several paths at once.",
		Ports:       []string{"a", "b", "c"},
		Repeatable:  true,
	},
	{
		Type: TypeJoin, Title: "Join", Category: CategoryControl,
		Description: "Continues when every connected path has arrived.",
		Ports:       []string{PortNext},
		Repeatable:  true,
	},
	{
		Type: TypeWaitEvent, Title: "Wait for event", Category: CategoryControl,
		Description: "Waits for something to happen on the run's card.",
		Ports:       []string{PortEvent, PortTimeout},
		Fields: []Field{
			{Name: "event", Label: "Event", Kind: FieldSelect, Options: eventOptions, Required: true, Default: "card.moved"},
			{Name: "to_kind", Label: "Enters (card moved)", Kind: FieldSelect, Options: kindOptions, Default: "any"},
			{Name: "timeout_minutes", Label: "Give up after (minutes)", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(1)},
		},
	},
	{
		Type: TypeDelay, Title: "Delay", Category: CategoryControl,
		Description: "Waits and continues.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "seconds", Label: "Seconds", Kind: FieldNumber, Required: true, Default: 60, Min: minOf(0)}},
		Repeatable:  true,
	},
	{
		Type: TypeRetry, Title: "Retry", Category: CategoryControl,
		Description: "Goes back up to a number of times, then gives up. It is the only way to make a loop.",
		Ports:       []string{PortAgain, PortExhausted},
		Fields:      []Field{{Name: "max", Label: "Times", Kind: FieldNumber, Required: true, Default: 3, Min: minOf(1)}},
		Repeatable:  true,
	},
	{
		Type: TypeStop, Title: "Stop", Category: CategoryControl,
		Description: "Ends the run.",
		Ports:       []string{},
		Fields: []Field{
			{Name: "result", Label: "Result", Kind: FieldSelect, Options: []string{RunSucceededName, RunFailedName}, Default: RunSucceededName},
			{Name: "message", Label: "Message", Kind: FieldTemplate},
		},
		Repeatable: true,
	},
	{
		Type: TypeApproval, Title: "Human approval", Category: CategoryControl,
		Description: "Stops the flow until someone decides, from the bell or from the run. It can wait for days.",
		Ports:       []string{PortApproved, PortRejected, PortTimeout},
		Fields: []Field{
			{Name: "question", Label: "Question", Kind: FieldTemplate, Required: true, Help: "For example: Merge {{card.title}}?"},
			{Name: "timeout_minutes", Label: "Give up after (minutes)", Kind: FieldNumber, Min: minOf(1), Help: "Empty: it waits until someone answers."},
		},
	},

	{
		Type: TypeAIEvaluate, Title: "AI evaluate", Category: CategoryAction,
		Description: "Asks Claude through the Anthropic API to choose one answer, and follows that output. It reads only the text you give it and spends API tokens.",
		Ports:       []string{PortOutcome1, PortOutcome2, PortOutcome3, PortFailed},
		Fields: []Field{
			{Name: "instruction", Label: "Instruction", Kind: FieldTemplate, Required: true, Help: "What to decide. For example: How risky are these changes?"},
			{Name: "input", Label: "Text to evaluate", Kind: FieldTemplate, Required: true, Default: "{{card.title}}\n\n{{card.description}}", Help: "Claude treats this as data, not as instructions."},
			{Name: "outcome1", Label: "Answer 1", Kind: FieldText, Required: true, Default: "yes", PortLabel: PortOutcome1},
			{Name: "outcome2", Label: "Answer 2", Kind: FieldText, Required: true, Default: "no", PortLabel: PortOutcome2},
			{Name: "outcome3", Label: "Answer 3", Kind: FieldText, PortLabel: PortOutcome3},
			{Name: "model", Label: "Model", Kind: FieldSelect, Options: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, Default: "claude-opus-5-5", Required: true},
			{Name: "effort", Label: "Effort", Kind: FieldSelect, Options: []string{"low", "medium", "high"}, Default: "medium", Required: true},
		},
	},
	{
		Type: TypeGitStatus, Title: "Git status", Category: CategoryAction,
		Description: "Looks at the card's branch on its runner and changes nothing. Takes \"nothing\" when there is no branch or nothing left to merge. Reads as nodes.<id>.files, .branch, .base, .ahead.",
		Ports:       []string{PortClean, PortConflicts, PortDirty, PortNothing, PortFailed},
		Repeatable:  true,
	},
	{
		Type: TypeMerge, Title: "Merge", Category: CategoryAction,
		Description: "Merges the card's branch into the branch of the main checkout, on its runner. If git refuses it changes nothing and takes \"conflicts\".",
		Ports:       []string{PortMerged, PortConflicts, PortFailed},
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
		Description: "Tests the requested tool with regexes, the same way permission rules do. Takes \"match\" if the tool and every parameter line match. Only for Tool requested flows.",
		Ports:       []string{PortMatch, PortNoMatch},
		Fields: []Field{
			{Name: "tool", Label: "Tool", Kind: FieldText, Default: "*", Help: "A regex for the whole name: Read|Glob|Grep or mcp__github__.*. * is any tool."},
			{Name: "input_regex", Label: "Parameters", Kind: FieldLines, Help: "One per line, parameter: regex. For example command: ^go test ./[^;&|]*$ . The regex may match part of the text, so without ^ and $, or if it allows ; & |, go test also matches go test && rm -rf /."},
		},
		Repeatable: true,
	},
	{
		Type: TypeToolDecision, Title: "Tool decision", Category: CategoryAction,
		Description: "Answers the Tool requested trigger and ends the run. \"ask\" shows the usual approval to a person, or leaves it to Claude's auto mode if the agent runs in it. A deny also stops the agent's other tools for the rest of the turn.",
		Ports:       []string{},
		Fields: []Field{
			{Name: "decision", Label: "Decision", Kind: FieldSelect, Options: []string{ToolAllow, ToolDeny, ToolAsk}, Required: true, Default: ToolAsk},
			{Name: "reason", Label: "Reason", Kind: FieldTemplate, Help: "The agent reads it, and it stays in the session's history."},
		},
	},
	{
		Type: TypeNotifySupervisor, Title: "Notify supervisor", Category: CategoryAction,
		Description: "Tells the agent the card's agent reports to. Does nothing if it reports to nobody.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "text", Label: "Message", Kind: FieldTemplate, Required: true}},
	},
	{
		Type: TypePushNotification, Title: "Push notification", Category: CategoryAction,
		Description: "Notifies every device that turned on \"Notifications on this device\" in this tab, even with lucode closed. Clicking it opens this run. Takes \"failed\" if no device could be reached. Reads as nodes.<id>.sent, .failed.",
		Ports:       []string{PortNext, PortFailed},
		Fields: []Field{
			{Name: "body", Label: "Message", Kind: FieldTemplate, Required: true, Help: "For example: {{card.title}} is ready for review. Long messages are cut."},
			{Name: "title", Label: "Title", Kind: FieldTemplate, Required: true, Default: "lucode"},
		},
		// sent again it replaces the first one on the device, because the tag is the step's
		Repeatable: true,
	},
	{
		Type: TypeRunAgent, Title: "Run agent", Category: CategoryAction,
		Description: "Moves the card to In Progress and gives its agent an instruction on a runner. Continues when the turn ends.",
		Ports:       []string{PortNext, PortFailed},
		Fields: []Field{
			{Name: "agent", Label: "Agent", Kind: FieldAgent, Help: "Empty: the agent the card already has."},
			{Name: "prompt", Label: "Instruction", Kind: FieldTemplate, Required: true},
		},
	},
	{
		Type: TypeMoveCard, Title: "Move card", Category: CategoryAction,
		Description: "Moves the card under the board's rules. Takes \"rejected\" if a rule refuses it.",
		Ports:       []string{PortNext, PortRejected},
		Fields: []Field{
			{Name: "to_kind", Label: "To", Kind: FieldSelect, Options: statusOptions},
			{Name: "to_list", Label: "Or this list", Kind: FieldList, Help: "For lists you created. It replaces \"To\"."},
		},
		Repeatable: true,
	},
	{
		Type: TypeComment, Title: "Comment", Category: CategoryAction,
		Description: "Adds a note to the card.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "text", Label: "Text", Kind: FieldTemplate, Required: true}},
	},
	{
		Type: TypeCreateTask, Title: "Create task", Category: CategoryAction,
		Description: "Adds a card to the board.",
		Ports:       []string{PortNext},
		Fields: []Field{
			{Name: "title", Label: "Title", Kind: FieldTemplate, Required: true},
			{Name: "description", Label: "Description", Kind: FieldTemplate},
			{Name: "status", Label: "List", Kind: FieldSelect, Options: statusOptions, Default: "todo"},
			{Name: "agent", Label: "Agent", Kind: FieldAgent},
		},
	},
	{
		Type: TypeAddTag, Title: "Add tag", Category: CategoryAction,
		Description: "Adds a tag to the card.",
		Ports:       []string{PortNext},
		Fields:      []Field{{Name: "tag", Label: "Tag", Kind: FieldTemplate, Required: true}},
		Repeatable:  true,
	},
}

// Values of Stop's result field.
const (
	RunSucceededName = "success"
	RunFailedName    = "failure"
)

// NodeTypes lists the base nodes.
func NodeTypes() []NodeType { return nodeTypes }

// NodeTypeOf finds a node type.
func NodeTypeOf(t string) (NodeType, bool) {
	for _, n := range nodeTypes {
		if n.Type == t {
			return n, true
		}
	}
	return NodeType{}, false
}
