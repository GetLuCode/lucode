package taskmcp

import "strings"

// Tools for the Commands tab: an agent defines commands of the project, runs them in a terminal on
// the project's runner and reads what they printed. Like the browser and flow tools they are opt-in
// ("all the lucode tools" does not include them). The ones that only read are approved by lucode;
// the ones that create, run or stop a process go through the usual approval, because running a
// command is running code on a machine.
const (
	ToolCommandList   = "command_list"
	ToolCommandCreate = "command_create"
	ToolCommandRun    = "command_run"
	ToolCommandStop   = "command_stop"
	ToolCommandOutput = "command_output"
	ToolCommandErrors = "command_errors"
	// the shells a runner offers to commands; registering one is a change on that machine, so it asks
	ToolCommandShells   = "command_shells"
	ToolCommandShellAdd = "command_shell_add"
)

// CommandNames lists the command tools, in display order.
func CommandNames() []string {
	return []string{ToolCommandList, ToolCommandCreate, ToolCommandRun, ToolCommandStop, ToolCommandOutput, ToolCommandErrors, ToolCommandShells, ToolCommandShellAdd}
}

// IsCommandTool reports whether name is a command tool, with or without the mcp__lucode__ prefix.
func IsCommandTool(name string) bool {
	n := strings.TrimPrefix(name, "mcp__lucode__")
	for _, c := range CommandNames() {
		if c == n {
			return true
		}
	}
	return false
}

// IsCommandReadTool is a command tool that changes nothing and runs nothing.
func IsCommandReadTool(name string) bool {
	switch strings.TrimPrefix(name, "mcp__lucode__") {
	case ToolCommandList, ToolCommandOutput, ToolCommandErrors, ToolCommandShells:
		return true
	}
	return false
}

const commandRef = "Id or name of the command (see command_list)."

var commandTools = []tool{
	{
		Name:        ToolCommandList,
		Description: "List the commands defined in this project's Commands tab: id, name, command line, folder, and whether each is running, finished (with its exit code) or never started.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name: ToolCommandCreate,
		Description: "Define a command in the project's Commands tab: a command line that runs in a terminal on the project's runner, in a folder relative to the project's work directory. " +
			"Use it for things you or the user will run more than once (a dev server, the tests, a build). It only saves the definition; set run=true to start it as well. The user sees and can run it in the tab.",
		InputSchema: props(map[string]any{
			"name":      str("Short name, e.g. \"Unit tests\"."),
			"command":   str("The command line, as you would type it in a terminal. Empty opens an interactive shell."),
			"cwd":       str("Folder to run in, relative to the project's work directory (e.g. \"web\"). Empty = the work directory itself. It cannot leave it."),
			"runner_id": str("Runner to run on. Only needed when the project has folders on several runners; defaults to the project's runner."),
			"run":       flag("Start it right away."),
			"shell":     str("Id of one of the runner's shells (see command_shells). Empty = the runner's default shell."),
			"env": map[string]any{
				"type":        "array",
				"description": "Environment variables added to the runner's own for this command. Names are letters, digits and _, unique ignoring case. command_list shows only the names.",
				"items": props(map[string]any{
					"name":  str("Variable name."),
					"value": str("Variable value."),
				}, "name"),
			},
		}, "name", "command"),
	},
	{
		Name:        ToolCommandShells,
		Description: "List the shells a runner offers for commands (id, name, path, and whether the runner found it or someone registered it). Use an id as the shell of command_create.",
		InputSchema: props(map[string]any{"runner_id": str("Runner to ask. Defaults to the project's runner.")}),
	},
	{
		Name: ToolCommandShellAdd,
		Description: "Register a shell on a runner so commands can use it (a bash in a non-standard path, a Git Bash launcher...). The runner keeps it. The user approves it, because it names an executable on that machine. " +
			"Returns the shells of the runner, with the id of the new one.",
		InputSchema: props(map[string]any{
			"name":      str("Name to show, e.g. \"Git Bash\"."),
			"path":      str("Absolute path of the shell executable on the runner."),
			"args":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Arguments put before the command line. Omit them to use the usual ones for that kind of shell."},
			"runner_id": str("Runner to register it on. Defaults to the project's runner."),
		}, "name", "path"),
	},
	{
		Name: ToolCommandRun,
		Description: "Start a command in its terminal on the runner (a command that is already running is an error: stop it first). " +
			"By default it returns at once and the command keeps running: use command_output / command_errors to look at it. " +
			"With wait_seconds it waits up to that long for the command to end and returns its exit code and the errors it printed, which suits tests and builds.",
		InputSchema: props(map[string]any{
			"command":      str(commandRef),
			"wait_seconds": num("Wait up to this many seconds (1-300) for it to finish. Omit it for servers and anything long-lived."),
		}, "command"),
	},
	{
		Name:        ToolCommandStop,
		Description: "Stop a running command and everything it started.",
		InputSchema: props(map[string]any{"command": str(commandRef)}, "command"),
	},
	{
		Name: ToolCommandOutput,
		Description: "Read the last lines a command printed, with terminal colors and cursor codes removed, plus its state. " +
			"The runner keeps about the last 256 KB, and it keeps them after the command ends.",
		InputSchema: props(map[string]any{
			"command": str(commandRef),
			"lines":   num("How many of the last lines (default 80, at most 500)."),
		}, "command"),
	},
	{
		Name: ToolCommandErrors,
		Description: "List the errors a command printed, MOST RECENT FIRST, each with the lines that belong to it (stack trace, source excerpt), and the exit code if it ended badly. " +
			"The terminal mixes stderr and stdout into one stream, so errors are recognised in the text (error, failed, panic, exception, traceback, compiler codes...): a program that fails silently only shows through its exit code. " +
			"Without a command it covers every command of the project, the one that ran most recently first.",
		InputSchema: props(map[string]any{
			"command": str(commandRef + " Omit it to look at all of them."),
			"limit":   num("How many errors at most (default 10, at most 50)."),
		}),
	},
}

func init() { tools = append(tools, commandTools...) }
