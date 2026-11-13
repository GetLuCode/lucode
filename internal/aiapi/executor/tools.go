package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"claude-orchestrator/internal/aiapi/executor/checkpoint"
	"claude-orchestrator/internal/aiapi/executor/fsop"
	"claude-orchestrator/internal/aiapi/executor/worktree"
)

// Tool follows the OpenAI function-calling format. MiniMax accepts the same shape.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the inner "function" object of a tool definition.
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall is a request from the model to invoke a tool. Arguments is a JSON
// string (not an object) — the model streams it token by token.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
	Index    *int         `json:"index,omitempty"`
}

// ToolCallFunc is the "function" object inside a streamed tool call delta.
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Output-size caps. These bound the per-tool result string so a single turn
// can't blow up the model's context window. Documented in the system prompt
// and per-tool descriptions.
const (
	maxGrepMatches    = 1000
	maxReadFileBytes  = 256 * 1024 // 256 KB
	maxListDirEntries = 500
	maxCommandOutput  = 8 * 1024
	maxViewLines      = 500
)

// toolResult is the JSON shape returned by every tool that touches the
// filesystem (and so needs to surface a head checkpoint id).
//
//   - Success:  HeadCheckpoint + Content set.
//   - Failure:  HeadCheckpoint set (so the model can retry with the right
//     expected_head_checkpoint), Error set with an "error: ..." prefix to
//     match the existing convention.
//
// run_command is the exception: it returns a plain string because its
// effects are not tracked by checkpoints.
type toolResult struct {
	HeadCheckpoint string `json:"head_checkpoint,omitempty"`
	Content        string `json:"content,omitempty"`
	Error          string `json:"error,omitempty"`
}

func okResult(head, content string) string {
	b, _ := json.Marshal(toolResult{HeadCheckpoint: head, Content: content})
	return string(b)
}

func errResult(head, msg string) string {
	b, _ := json.Marshal(toolResult{HeadCheckpoint: head, Error: "error: " + msg})
	return string(b)
}

// currentHead returns the current head checkpoint id for workDir, or "" if
// workDir is unset / unreadable. Cheap (one walk + one hash); safe to call
// per tool invocation.
func currentHead(workDir string) string {
	if workDir == "" {
		return ""
	}
	ignore, err := fsop.NewIgnoreSet(workDir)
	if err != nil {
		return ""
	}
	h, err := fsop.ComputeHead(workDir, ignore)
	if err != nil {
		return ""
	}
	return h
}

// Tools is the set of tools exposed to the model. Keep this list small and
// stable: each entry is part of the conversation context on every turn.
//
// Per-tool semantics that are NOT captured here (and live in the system
// prompt built in buildMessages) so they don't get duplicated 6 times:
//   - sequential execution, one tool per round, up to MaxAgenticRounds
//   - all errors come back as JSON with an "error" field prefixed with "error:"
//   - path args are resolved against the working directory and rejected
//     if they escape it (incl. platform-foreign absolute paths)
//   - file-modifying tools leave the file unchanged on any error
//   - read tools (read_file, view, list_directory, grep) return JSON with a
//     head_checkpoint id; pass it back as expected_head_checkpoint to the
//     filesystem tool for stale-read safety
var Tools = []Tool{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "filesystem",
			Description: "Apply a batch of filesystem operations atomically. All ops are validated up front; if any fails, NONE are applied (the workDir is rolled back). Returns JSON with the new head_checkpoint id. Use this for ALL file modifications — there is no write_file or edit_file anymore.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ops": map[string]interface{}{
						"type":        "array",
						"description": "List of operations to apply in order. Atomic: if any op fails, the whole batch is rolled back.",
						"items": map[string]interface{}{
							"oneOf": []interface{}{
								map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"op":      map[string]interface{}{"type": "string", "enum": []string{"create"}, "description": "Create a new file. Fails if the file already exists."},
										"path":    map[string]interface{}{"type": "string", "description": "Path to the new file (relative to working dir or absolute, must be inside the working directory)."},
										"content": map[string]interface{}{"type": "string", "description": "Full file contents."},
									},
									"required":             []string{"op", "path", "content"},
									"additionalProperties": false,
								},
								map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"op":               map[string]interface{}{"type": "string", "enum": []string{"update"}, "description": "Replace an exact substring in an existing file."},
										"path":             map[string]interface{}{"type": "string", "description": "Path to the file (relative to working dir or absolute, must be inside the working directory)."},
										"old_string":       map[string]interface{}{"type": "string", "description": "Exact substring to find (no regex, no fuzzy match). Include surrounding context for uniqueness."},
										"new_string":       map[string]interface{}{"type": "string", "description": "Replacement text."},
										"replace_all":      map[string]interface{}{"type": "boolean", "description": "If true, replace every occurrence. If false (default) and old_string appears N times, the call fails — pass occurrence_index=N for the Nth match (1-indexed)."},
										"occurrence_index": map[string]interface{}{"type": "integer", "minimum": 1, "description": "Replace the Nth (1-indexed) occurrence. Use after grep to disambiguate a known match count."},
									},
									"required":             []string{"op", "path", "old_string", "new_string"},
									"additionalProperties": false,
								},
								map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"op":   map[string]interface{}{"type": "string", "enum": []string{"delete"}, "description": "Delete a file. Fails if it doesn't exist."},
										"path": map[string]interface{}{"type": "string", "description": "Path to the file (relative to working dir or absolute, must be inside the working directory)."},
									},
									"required":             []string{"op", "path"},
									"additionalProperties": false,
								},
								map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"op":   map[string]interface{}{"type": "string", "enum": []string{"rename"}, "description": "Rename (move) a file. Fails if the source doesn't exist or the destination already exists."},
										"from": map[string]interface{}{"type": "string", "description": "Source path."},
										"to":   map[string]interface{}{"type": "string", "description": "Destination path. Parent directories are created as needed."},
									},
									"required":             []string{"op", "from", "to"},
									"additionalProperties": false,
								},
								map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"op":   map[string]interface{}{"type": "string", "enum": []string{"copy"}, "description": "Copy a file. Fails if the source doesn't exist or the destination already exists. Use 'rename' if you want to move (faster, no extra disk)."},
										"from": map[string]interface{}{"type": "string", "description": "Source path."},
										"to":   map[string]interface{}{"type": "string", "description": "Destination path. Parent directories are created as needed."},
									},
									"required":             []string{"op", "from", "to"},
									"additionalProperties": false,
								},
							},
						},
					},
					"expected_head_checkpoint": map[string]interface{}{
						"type":        "string",
						"description": "Optional optimistic concurrency check. If set, the call fails unless the current head_checkpoint matches. Use this after read_file/view/list_directory/grep to guarantee the edit is based on the same version you saw. Omit on the first call of a turn.",
					},
				},
				"required": []string{"ops"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "view",
			Description: "Read a range of lines from a file with line numbers (cat -n style). Use this to extract exact context for filesystem updates on large files. Capped at 500 lines per call. Returns JSON with head_checkpoint (pass it back as expected_head_checkpoint when editing) and content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]interface{}{"type": "string", "description": "Path to the file (relative to working dir or absolute, must be inside the working directory)."},
					"line_start": map[string]interface{}{"type": "integer", "description": "First line to read, 1-indexed.", "minimum": 1},
					"line_end":   map[string]interface{}{"type": "integer", "description": "Last line to read, inclusive. Capped at 500 lines per call.", "minimum": 1},
				},
				"required": []string{"path", "line_start", "line_end"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "read_file",
			Description: "Read the full contents of a file. Capped at 256 KB; if the file is larger, the tail is replaced with a truncation marker. For larger files, use view with a line range. Returns JSON with head_checkpoint (pass it back as expected_head_checkpoint when editing) and content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Path to the file (relative to working dir or absolute, must be inside the working directory)."},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_directory",
			Description: "List files and subdirectories in a directory (non-recursive, filesystem order — usually alphabetical). Capped at 500 entries; if there are more, a '... and N more entries' marker is appended. Hidden files (names starting with '.') are included. For a recursive inventory, use grep with pattern='.*' and glob='*'. Returns JSON with head_checkpoint and content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Directory to list (relative to working dir or absolute, must be inside the working directory)."},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "grep",
			Description: "Search for a regex pattern (Go RE2 syntax) in a file or directory. Output is a header line '(N matches in M files[ truncated at 1000])' followed by 'path:line:content' lines, capped at 1000 matches to protect the context window. When path is a directory: recursive defaults to true; glob defaults to '*' (e.g. '*.go', '*.{ts,tsx}'). With recursive=false, only the top-level directory is scanned. Order is filesystem-walk order; symlinks are not followed. Returns JSON with head_checkpoint and content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":      map[string]interface{}{"type": "string", "description": "File or directory to search in (relative to working dir or absolute, must be inside the working directory)."},
					"pattern":   map[string]interface{}{"type": "string", "description": "Regular expression pattern (Go RE2 syntax — no lookaheads, no backreferences)."},
					"recursive": map[string]interface{}{"type": "boolean", "description": "If path is a directory, recurse into subdirectories (default true). Ignored when path is a single file."},
					"glob":      map[string]interface{}{"type": "string", "description": "When path is a directory, only search files matching this glob (default '*'). Examples: '*.go', '*.{ts,tsx}', 'README*'."},
				},
				"required": []string{"path", "pattern"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "run_command",
			Description: runCommandDescription(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Shell command to execute in the working directory.",
					},
					"runtime": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"sh", "cmd", "pwsh", "git-bash"},
						"description": "Shell to use. 'sh' = POSIX sh, = Windows 'cmd' = Windows cmd /C, 'pwsh' = PowerShell 7+ (cross-platform), 'git-bash' = Git for Windows bash. Omit to use the platform default (cmd on Windows, sh elsewhere).",
					},
				},
				"required": []string{"command"},
			},
		},
	},
}

// executeTool dispatches a tool call to the matching implementation. workDir
// is the first user-supplied working directory; relative paths are resolved
// against it (and must stay within it — see fsop.ResolvePath). Returns a
// string suitable for sending back as a tool message.
//
// All tools that touch the filesystem return a JSON toolResult; run_command
// returns a plain string because its effects are not checkpointed.
func executeTool(name string, argsJSON json.RawMessage, workDir string, wm *worktree.Manager) string {
	// When the worktree manager is active, all filesystem operations
	// target the worktree path (not the user's workDir). The worktree
	// is a private scratch dir for the model; at end of turn the
	// 3-way merge phase in minimax.go applies it to the user workDir.
	if wm != nil && wm.Active() {
		workDir = wm.Path()
	}

	var args map[string]interface{}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return errResult("", fmt.Sprintf("could not parse arguments: %v", err))
	}

	str := func(key string) string { v, _ := args[key].(string); return v }
	intArg := func(key string) (int, bool) {
		v, ok := args[key].(float64) // JSON numbers come as float64
		if !ok {
			return 0, false
		}
		return int(v), true
	}
	boolArg := func(key string, def bool) bool {
		if v, ok := args[key].(bool); ok {
			return v
		}
		return def
	}

	head := currentHead(workDir)

	switch name {
	case "filesystem":
		var fa struct {
			Ops          []fsop.FSOp `json:"ops"`
			ExpectedHead string      `json:"expected_head_checkpoint"`
		}
		// Re-parse from raw argsJSON so we get the typed []FSOp.
		if err := json.Unmarshal(argsJSON, &fa); err != nil {
			return errResult(head, fmt.Sprintf("could not parse arguments: %v", err))
		}

		ignore, err := fsop.NewIgnoreSet(workDir)
		if err != nil {
			return errResult(head, fmt.Sprintf("load .agentignore: %v", err))
		}

		result, err := fsop.ApplyBatch(fa.Ops, workDir, ignore, fa.ExpectedHead)
		if err != nil {
			return errResult(head, err.Error())
		}

		// Persist a checkpoint so the user can revert. Done best-effort: a
		// snapshot failure (disk full, permission denied) does NOT roll back
		// the ops — the filesystem already changed, and the model has its
		// result. We just log and move on; the user loses the ability to
		// revert THIS turn but the next turn will snapshot fine.
		if workDir != "" {
			if store, sErr := checkpoint.NewStore(workDir); sErr == nil {
				summary := summarizeOps(fa.Ops)
				if _, snapErr := store.Snapshot("", fa.Ops, summary); snapErr != nil {
					log.Printf("[executor] checkpoint snapshot failed: %v", snapErr)
				}
			} else {
				log.Printf("[executor] checkpoint store open failed: %v", sErr)
			}
		}

		return okResult(result.NewHead, fmt.Sprintf("applied %d op(s), new head: %s", result.OpsRun, result.NewHead))

	case "view":
		path, err := fsop.ResolvePath(str("path"), workDir)
		if err != nil {
			return errResult(head, err.Error())
		}
		start, ok1 := intArg("line_start")
		end, ok2 := intArg("line_end")
		if !ok1 || !ok2 {
			return errResult(head, "line_start and line_end are required integers")
		}
		if start < 1 {
			return errResult(head, "line_start must be >= 1")
		}
		if end < start {
			return errResult(head, "line_end must be >= line_start")
		}
		if end-start+1 > maxViewLines {
			return errResult(head, fmt.Sprintf("line range capped at %d lines (requested %d)", maxViewLines, end-start+1))
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return errResult(head, err.Error())
		}
		// Split on \n. Files written with CRLF will have a trailing \r on
		// each line; we leave that in place so output is faithful to disk.
		lines := strings.Split(string(data), "\n")
		total := len(lines)
		if end > total {
			end = total
		}
		if start > total {
			return errResult(head, fmt.Sprintf("line_start %d is past end of file (%d lines)", start, total))
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "(%d-%d of %d lines)\n", start, end, total)
		width := len(fmt.Sprintf("%d", end))
		for i := start - 1; i < end; i++ {
			fmt.Fprintf(&sb, "%*d\t%s\n", width, i+1, lines[i])
		}
		return okResult(head, sb.String())

	case "read_file":
		path, err := fsop.ResolvePath(str("path"), workDir)
		if err != nil {
			return errResult(head, err.Error())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errResult(head, err.Error())
		}
		if len(data) > maxReadFileBytes {
			return okResult(head, string(data[:maxReadFileBytes])+fmt.Sprintf("\n... (truncated, file is %d bytes)", len(data)))
		}
		return okResult(head, string(data))

	case "list_directory":
		path, err := fsop.ResolvePath(str("path"), workDir)
		if err != nil {
			return errResult(head, err.Error())
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return errResult(head, err.Error())
		}
		var sb strings.Builder
		written := 0
		truncated := 0
		for _, e := range entries {
			if written >= maxListDirEntries {
				truncated++
				continue
			}
			if e.IsDir() {
				sb.WriteString(e.Name() + "/\n")
			} else if info, _ := e.Info(); info != nil {
				sb.WriteString(fmt.Sprintf("%s (%d bytes)\n", e.Name(), info.Size()))
			}
			written++
		}
		if truncated > 0 {
			sb.WriteString(fmt.Sprintf("... and %d more entries\n", truncated))
		}
		if sb.Len() == 0 {
			return okResult(head, "(empty directory)")
		}
		return okResult(head, sb.String())

	case "grep":
		path, err := fsop.ResolvePath(str("path"), workDir)
		if err != nil {
			return errResult(head, err.Error())
		}
		pattern := str("pattern")
		if pattern == "" {
			return errResult(head, "pattern is empty")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return errResult(head, fmt.Sprintf("invalid regex: %v", err))
		}
		recursive := boolArg("recursive", true)
		glob := "*"
		if v, ok := args["glob"].(string); ok && v != "" {
			glob = v
		}

		info, err := os.Stat(path)
		if err != nil {
			return errResult(head, err.Error())
		}

		var sb strings.Builder
		matchCount := 0
		fileCount := 0
		truncatedMatches := false

		add := func(fpath string, n int) {
			if n == 0 || truncatedMatches {
				return
			}
			fileCount++
			matchCount += n
			if matchCount > maxGrepMatches {
				truncatedMatches = true
			}
		}

		if !info.IsDir() {
			n, err := grepFile(path, re, &sb)
			if err != nil {
				return errResult(head, err.Error())
			}
			if n > maxGrepMatches {
				truncatedMatches = true
			}
			fileCount = 1
			matchCount = n
		} else {
			walk := func(fpath string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				if !matchGlob(glob, d.Name()) {
					return nil
				}
				n, err := grepFile(fpath, re, &sb)
				if err != nil {
					return err
				}
				add(fpath, n)
				return nil
			}

			if recursive {
				if err := filepath.WalkDir(path, walk); err != nil {
					return errResult(head, fmt.Sprintf("walking %s: %v", path, err))
				}
			} else {
				entries, err := os.ReadDir(path)
				if err != nil {
					return errResult(head, fmt.Sprintf("listing %s: %v", path, err))
				}
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					if !matchGlob(glob, e.Name()) {
						continue
					}
					n, err := grepFile(filepath.Join(path, e.Name()), re, &sb)
					if err != nil {
						return errResult(head, err.Error())
					}
					add(filepath.Join(path, e.Name()), n)
				}
			}
		}

		if truncatedMatches {
			matchCount = maxGrepMatches
		}
		if matchCount == 0 {
			return okResult(head, fmt.Sprintf("no matches for pattern %q", pattern))
		}
		header := fmt.Sprintf("(%d matches in %d files", matchCount, fileCount)
		if truncatedMatches {
			header += fmt.Sprintf(", truncated at %d", maxGrepMatches)
		}
		return okResult(head, header+")\n"+sb.String())

	case "run_command":
		rt := str("runtime")
		if rt == "" {
			if runtime.GOOS == "windows" {
				rt = "cmd"
			} else {
				rt = "sh"
			}
		}
		cmd, err := buildShellCmd(rt, str("command"))
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		cmd.Dir = workDir
		out, cmdErr := cmd.CombinedOutput()
		result := string(out)
		if cmdErr != nil && result == "" {
			return fmt.Sprintf("error: exit error: %v", cmdErr)
		}
		if result == "" {
			return "(no output)"
		}
		if len(result) > maxCommandOutput {
			result = result[:maxCommandOutput] + fmt.Sprintf("\n... (truncated, %d bytes)", len(result))
		}
		return result

	default:
		return errResult(head, fmt.Sprintf("unknown tool %q", name))
	}
}

// grepFile scans a single file line by line, appending matches in
// "path:line:content" format to sb. Returns the number of matches.
// Lines longer than 10 MB are skipped (unlikely in practice).
func grepFile(path string, re *regexp.Regexp, sb *strings.Builder) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	count := 0
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			sb.WriteString(fmt.Sprintf("%s:%d:%s\n", path, lineNum, line))
			count++
		}
	}
	return count, scanner.Err()
}

// matchGlob is a thin wrapper around filepath.Match that never errors.
// Used for the grep tool's glob filter; unmatchable patterns behave as "match
// nothing", which is the right default for a search filter.
func matchGlob(pattern, name string) bool {
	ok, err := filepath.Match(pattern, name)
	if err != nil {
		return false
	}
	return ok
}

// buildShellCmd constructs an exec.Cmd for the requested runtime.
// Valid values: "sh", "cmd", "pwsh", "git-bash".
func buildShellCmd(rt, command string) (*exec.Cmd, error) {
	switch rt {
	case "sh":
		sh, err := findSh()
		if err != nil {
			return nil, err
		}
		return exec.Command(sh, "-c", command), nil
	case "cmd":
		return exec.Command("cmd", "/C", command), nil
	case "pwsh":
		pwsh, err := exec.LookPath("pwsh")
		if err != nil {
			pwsh, err = exec.LookPath("powershell")
			if err != nil {
				return nil, fmt.Errorf("pwsh / powershell not found in PATH")
			}
		}
		return exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", command), nil
	case "git-bash":
		bash, err := findGitBash()
		if err != nil {
			return nil, err
		}
		return exec.Command(bash, "-c", command), nil
	default:
		return nil, fmt.Errorf("unknown runtime %q; valid values: sh, cmd, pwsh, git-bash", rt)
	}
}

func findSh() (string, error) {
	if runtime.GOOS != "windows" {
		return "sh", nil
	}
	return findGitBash()
}

func findGitBash() (string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("git-bash not found; install Git for Windows or add bash.exe to PATH")
}

func runCommandDescription() string {
	if runtime.GOOS == "windows" {
		return "Run a shell command with cwd set to the working directory. Returns combined stdout+stderr, truncated to 8 KB. No timeout, no interactive input, and no undo — destructive commands take effect immediately. Exit non-zero status is reported as 'error: exit error: ...'. NOT covered by checkpoints. Default runtime on Windows is 'cmd'; use the 'runtime' parameter to select 'sh' (Git Bash), 'pwsh' (PowerShell 7+), or 'git-bash' explicitly."
	}
	return "Run a shell command via 'sh -c' with cwd set to the working directory. Returns combined stdout+stderr, truncated to 8 KB. No timeout, no interactive input, and no undo — destructive commands take effect immediately. Exit non-zero status is reported as 'error: exit error: ...'. NOT covered by checkpoints. Use the 'runtime' parameter to select 'pwsh' (PowerShell 7+) or 'git-bash' if needed."
}

// summarizeOps returns a short human-readable string describing a batch of
// ops, for the checkpoint summary. Examples:
//   - 3 ops (create a.txt, update src/main.go, delete old/legacy.go)
//   - 1 op (update src/main.go)
//   - 5 ops (create x, create y, create z, create w, create v)
func summarizeOps(ops []fsop.FSOp) string {
	if len(ops) == 0 {
		return "0 ops"
	}
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		var target string
		switch op.Op {
		case fsop.OpCreate, fsop.OpUpdate, fsop.OpDelete:
			target = op.Path
		case fsop.OpRename, fsop.OpCopy:
			target = op.From + " → " + op.To
		default:
			target = "?"
		}
		parts = append(parts, string(op.Op)+" "+target)
	}
	return fmt.Sprintf("%d op(s): %s", len(ops), strings.Join(parts, ", "))
}
