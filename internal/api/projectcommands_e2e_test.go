package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	dbpkg "claude-orchestrator/internal/db"
)

// A command defined on the hub runs in a terminal on the runner: the hub stores it, asks the runner
// over RPC, and the bytes and the state come back over NATS to the browser's socket.
func TestCommandsTabThroughHubAndRunner(t *testing.T) {
	h, pid, _, repo, _, _ := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	var evMu sync.Mutex
	var events []map[string]any
	h.bus.OnBroadcast = func(data []byte) {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			evMu.Lock()
			events = append(events, m)
			evMu.Unlock()
		}
	}
	mux := http.NewServeMux()
	h.registerCommands(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	call := func(method, path string, body any, out any) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil && resp.StatusCode == 200 {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// a folder outside the work dir is refused when saving
	for _, bad := range []string{"../x", "a/../../x", filepath.Join(os.TempDir(), "abs")} {
		if code := call("POST", "/api/projects/"+pid+"/commands", map[string]string{"name": "x", "command": "echo x", "cwd": bad}, nil); code != 400 {
			t.Fatalf("cwd %q was accepted: %d", bad, code)
		}
	}

	var cmd dbpkg.ProjectCommand
	if code := call("POST", "/api/projects/"+pid+"/commands", map[string]string{"name": "say", "command": "echo marker-xyz", "cwd": "sub"}, &cmd); code != 200 || cmd.ID == "" {
		t.Fatalf("create: %d %+v", code, cmd)
	}

	// attach first: output of a process that has not started yet must still arrive
	var tk struct{ Ticket string }
	if code := call("POST", "/api/projects/"+pid+"/commands/"+cmd.ID+"/ticket", nil, &tk); code != 200 || tk.Ticket == "" {
		t.Fatalf("ticket: %d", code)
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/term?ticket=" + tk.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		t.Fatal("a ticket worked twice")
	}

	var st struct {
		State string `json:"state"`
		Cwd   string `json:"cwd"`
	}
	if code := call("POST", "/api/projects/"+pid+"/commands/"+cmd.ID+"/start", map[string]int{"cols": 100, "rows": 20}, &st); code != 200 || st.State != "running" {
		t.Fatalf("start: %d %+v", code, st)
	}
	if filepath.Base(st.Cwd) != "sub" {
		t.Fatalf("it ran in %q, want the sub folder", st.Cwd)
	}

	var got bytes.Buffer
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(got.String(), "marker-xyz") {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("socket: %v (got %q)", err, got.String())
		}
		if kind == websocket.BinaryMessage {
			got.Write(data)
		}
	}

	// the hub was told the process ended, and with which code
	deadline := time.Now().Add(15 * time.Second)
	for {
		evMu.Lock()
		done := false
		for _, e := range events {
			s, _ := e["status"].(map[string]any)
			if e["type"] == "command_status" && e["command_id"] == cmd.ID && s["state"] == "exited" {
				done = s["exit_code"] == float64(0)
			}
		}
		evMu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no exited status reached the browsers: %v", events)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// a second browser that attaches later gets what was printed
	call("POST", "/api/projects/"+pid+"/commands/"+cmd.ID+"/ticket", nil, &tk)
	late, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?ticket="+tk.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	late.SetReadDeadline(time.Now().Add(10 * time.Second))
	got.Reset()
	for !strings.Contains(got.String(), "marker-xyz") {
		kind, data, err := late.ReadMessage()
		if err != nil {
			t.Fatalf("late socket: %v (got %q)", err, got.String())
		}
		if kind == websocket.BinaryMessage {
			got.Write(data)
		}
	}

	// the list shows the state the runner reports
	var list commandList
	if code := call("GET", "/api/projects/"+pid+"/commands", nil, &list); code != 200 || len(list.Commands) != 1 ||
		list.Commands[0].Status == nil || list.Commands[0].Status.State != "exited" {
		t.Fatalf("list: %d %+v", code, list)
	}

	// another project's command is not reachable through this project
	if code := call("POST", "/api/projects/nope/commands/"+cmd.ID+"/start", nil, nil); code != 404 {
		t.Fatalf("foreign project: %d", code)
	}
	if code := call("DELETE", "/api/projects/"+pid+"/commands/"+cmd.ID, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
}

// An agent's tools drive the same commands: define, run (waiting), and read the errors back, the
// most recent first.
func TestCommandToolsThroughHubAndRunner(t *testing.T) {
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

	if out := mustTool("command_list", nil); !strings.Contains(out, "no commands yet") {
		t.Fatalf("empty list: %s", out)
	}
	if _, err := tool("command_create", map[string]any{"name": "x", "command": "echo x", "cwd": "../out"}); err == nil {
		t.Fatal("a folder outside the work dir was accepted")
	}
	mustTool("command_create", map[string]any{"name": "Fails A", "command": "echo error errA-xyz 1>&2 && exit 3"})
	if _, err := tool("command_create", map[string]any{"name": "fails a", "command": "echo again"}); err == nil {
		t.Fatal("a duplicate name was accepted")
	}

	out := mustTool("command_run", map[string]any{"command": "fails a", "wait_seconds": 30})
	if !strings.Contains(out, "exited with code 3") || !strings.Contains(out, "errA-xyz") {
		t.Fatalf("run and wait: %s", out)
	}

	mustTool("command_create", map[string]any{"name": "Fails B", "command": "echo error errB-xyz 1>&2 && exit 4"})
	if out := mustTool("command_run", map[string]any{"command": "Fails B", "wait_seconds": 30}); !strings.Contains(out, "errB-xyz") {
		t.Fatalf("second run: %s", out)
	}

	all := mustTool("command_errors", nil)
	a, b := strings.Index(all, "errA-xyz"), strings.Index(all, "errB-xyz")
	if a < 0 || b < 0 || b > a {
		t.Fatalf("the most recent command must come first:\n%s", all)
	}
	one := mustTool("command_errors", map[string]any{"command": "Fails A"})
	if !strings.Contains(one, "errA-xyz") || strings.Contains(one, "errB-xyz") {
		t.Fatalf("one command:\n%s", one)
	}
	if out := mustTool("command_output", map[string]any{"command": "Fails B", "lines": 5}); !strings.Contains(out, "errB-xyz") || !strings.Contains(out, "exited with code 4") {
		t.Fatalf("output: %s", out)
	}
	if out := mustTool("command_list", nil); !strings.Contains(out, "Fails A") || !strings.Contains(out, "exited with code 4") {
		t.Fatalf("list: %s", out)
	}
	if out := mustTool("command_stop", map[string]any{"command": "Fails A"}); !strings.Contains(out, "not running") {
		t.Fatalf("stop of an ended one: %s", out)
	}
	if _, err := tool("command_run", map[string]any{"command": "nope"}); err == nil {
		t.Fatal("an unknown command was run")
	}
}
