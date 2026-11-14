package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/termrun"
)

// An agent can choose the shell and the environment of a command, ask the runner for its shells and
// register one. The hub keeps neither the shells nor their paths: it asks the runner.
func TestCommandToolsShellAndEnv(t *testing.T) {
	h, pid, _, repo, _, _ := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	tool := func(name string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return h.handleCommandTool(pid, name, raw)
	}
	mustTool := func(name string, args map[string]any) string {
		t.Helper()
		out, err := tool(name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	shellID, line := "sh", "echo V=$LUCODE_E2E"
	if runtime.GOOS == "windows" {
		shellID, line = "cmd", "echo V=%LUCODE_E2E%"
	}
	var exe string
	for _, s := range termrun.Detect() {
		if s.ID == shellID {
			exe = s.Path
		}
	}
	if exe == "" {
		t.Skipf("this machine has no %s to run the command in", shellID)
	}

	if out := mustTool("command_shells", nil); !strings.Contains(out, "id="+shellID) {
		t.Fatalf("the runner's shells: %s", out)
	}

	// registering is checked on the runner
	if _, err := tool("command_shell_add", map[string]any{"name": "rel", "path": "bin/sh"}); err == nil {
		t.Fatal("a relative path was registered")
	}
	out := mustTool("command_shell_add", map[string]any{"name": "Mine", "path": exe})
	m := regexp.MustCompile(`id (custom-[^)]+)\)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no id in the answer: %s", out)
	}
	if _, err := tool("command_shell_add", map[string]any{"name": "mine", "path": exe}); err == nil {
		t.Fatal("a second shell with the same name was registered")
	}
	if !strings.Contains(mustTool("command_shells", nil), "id="+m[1]) {
		t.Fatal("the registered shell is not listed")
	}

	// the shape of shell and env is checked before saving
	for what, args := range map[string]map[string]any{
		"shell with a path": {"name": "x", "command": "echo x", "shell": "../evil"},
		"bad variable name": {"name": "x", "command": "echo x", "env": []map[string]string{{"name": "1A", "value": "v"}}},
		"duplicate":         {"name": "x", "command": "echo x", "env": []map[string]string{{"name": "A", "value": "1"}, {"name": "a", "value": "2"}}},
	} {
		if _, err := tool("command_create", args); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}

	// a command with both: the list shows the shell and the variable NAMES, never the value
	mustTool("command_create", map[string]any{
		"name": "env", "command": line, "shell": m[1],
		"env": []map[string]string{{"name": "LUCODE_E2E", "value": "val-123"}},
	})
	list := mustTool("command_list", nil)
	if !strings.Contains(list, "shell: "+m[1]) || !strings.Contains(list, "env: LUCODE_E2E") || strings.Contains(list, "val-123") {
		t.Fatalf("list:\n%s", list)
	}
	mustTool("command_run", map[string]any{"command": "env", "wait_seconds": 30})
	if got := mustTool("command_output", map[string]any{"command": "env"}); !strings.Contains(got, "V=val-123") {
		t.Fatalf("the command did not see its variable:\n%s", got)
	}
}
