package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorkspaceEvent is the claude_workspace event of a session: where claude really
// works (its init cwd, which with --worktree is the worktree) and, when that is a
// git checkout, its root, branch and whether it is a linked worktree. It is
// computed where claude runs (a runner's paths mean nothing to the hub) and goes
// through the same event stream as everything else.
func WorkspaceEvent(sessID, cwd string) map[string]any {
	ev := map[string]any{"type": "claude_workspace", "session_id": sessID, "cwd": cwd}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--abbrev-ref", "HEAD", "--git-dir", "--git-common-dir").Output()
	if err != nil {
		return ev // not a git checkout (or no git): the cwd is all there is
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 4 {
		return ev
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	top, branch, gitDir, common := lines[0], lines[1], lines[2], lines[3]
	ev["path"] = filepath.FromSlash(top)
	if branch == "HEAD" { // detached
		if sha, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--short", "HEAD").Output(); err == nil {
			branch = "(detached " + strings.TrimSpace(string(sha)) + ")"
		}
	}
	ev["branch"] = branch
	linked := filepath.Clean(filepath.FromSlash(gitDir)) != filepath.Clean(filepath.FromSlash(common))
	ev["worktree"] = linked
	if linked && filepath.Base(common) == ".git" {
		ev["repo"] = filepath.Dir(filepath.FromSlash(common)) // the main checkout the worktree belongs to
	}
	return ev
}

var enteredWorktreeRe = regexp.MustCompile(`Created worktree at [“"]([^”"]+)[”"] on branch`)

// EnteredWorktree returns the path of the worktree claude just created for the
// session (the result of its EnterWorktree tool), or "" when the line is anything
// else. A session that enters a worktree keeps its init cwd, so without this the
// hub would never know where the work really is.
func EnteredWorktree(line []byte) string {
	if !bytes.Contains(line, []byte("Created worktree at")) {
		return ""
	}
	var v any
	if json.Unmarshal(line, &v) != nil {
		return ""
	}
	var found string
	var walk func(any)
	walk = func(x any) {
		if found != "" {
			return
		}
		switch t := x.(type) {
		case string:
			if m := enteredWorktreeRe.FindStringSubmatch(t); m != nil {
				found = m[1]
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return found
}
