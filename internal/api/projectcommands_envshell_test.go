package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	dbpkg "claude-orchestrator/internal/db"
)

func TestCleanEnv(t *testing.T) {
	got, err := cleanEnv([]dbpkg.EnvVar{{Name: " A_1 ", Value: "x y"}, {}, {Name: "_b", Value: ""}})
	if err != nil || len(got) != 2 || got[0].Name != "A_1" || got[0].Value != "x y" || got[1].Name != "_b" {
		t.Fatalf("got %+v %v", got, err)
	}
	for what, in := range map[string][]dbpkg.EnvVar{
		"digit first":  {{Name: "1A", Value: "x"}},
		"has a dash":   {{Name: "A-B", Value: "x"}},
		"has an equal": {{Name: "A=B", Value: "x"}},
		"value only":   {{Name: "", Value: "x"}},
		"duplicate":    {{Name: "PATH", Value: "1"}, {Name: "path", Value: "2"}},
		"nul":          {{Name: "A", Value: "a\x00b"}},
		"long value":   {{Name: "A", Value: strings.Repeat("x", maxEnvValueLen+1)}},
	} {
		if _, err := cleanEnv(in); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

// The environment and the shell of a command: the hub stores them and checks the shape, the RUNNER
// lists its shells, registers new ones and resolves the id when the command starts.
func TestCommandEnvAndShellThroughHubAndRunner(t *testing.T) {
	h, pid, _, repo, _, _ := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerCommands(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path string, body any, out any) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		if out != nil && resp.StatusCode == 200 {
			_ = json.Unmarshal(buf.Bytes(), out)
		}
		return resp.StatusCode, buf.String()
	}
	base := "/api/projects/" + pid

	// the runner's shells
	var list struct {
		Shells []struct {
			ID      string `json:"id"`
			Path    string `json:"path"`
			Builtin bool   `json:"builtin"`
		} `json:"shells"`
	}
	if code, body := call("GET", base+"/command-shells", nil, &list); code != 200 || len(list.Shells) == 0 {
		t.Fatalf("shells: %d %s", code, body)
	}
	shellID, line := "sh", "echo V=$LUCODE_E2E"
	if runtime.GOOS == "windows" {
		shellID, line = "cmd", "echo V=%LUCODE_E2E%"
	}
	found := false
	for _, s := range list.Shells {
		found = found || s.ID == shellID
	}
	if !found {
		t.Skipf("this machine has no %s to run the command in", shellID)
	}

	// shape checks when saving
	for what, body := range map[string]map[string]any{
		"bad variable name": {"name": "x", "env": []map[string]string{{"name": "1A", "value": "v"}}},
		"duplicate":         {"name": "x", "env": []map[string]string{{"name": "A", "value": "1"}, {"name": "a", "value": "2"}}},
		"shell with a path": {"name": "x", "shell": "../evil"},
	} {
		if code, _ := call("POST", base+"/commands", body, nil); code != 400 {
			t.Errorf("%s: accepted (%d)", what, code)
		}
	}

	// saved, listed and kept on update; an older page that sends no env does not wipe it
	var cmd dbpkg.ProjectCommand
	code, body := call("POST", base+"/commands", map[string]any{
		"name": "env", "command": line, "shell": shellID,
		"env": []map[string]string{{"name": "LUCODE_E2E", "value": "val-123"}, {"name": "", "value": ""}},
	}, &cmd)
	if code != 200 || cmd.Shell != shellID || len(cmd.Env) != 1 || cmd.Env[0].Value != "val-123" {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, body := call("PUT", base+"/commands/"+cmd.ID, map[string]any{"name": "env", "command": line, "shell": shellID}, &cmd); code != 200 || len(cmd.Env) != 1 {
		t.Fatalf("update without env: %d %s", code, body)
	}
	stored, err := dbpkg.GetProjectCommand(h.db, cmd.ID)
	if err != nil || len(stored.Env) != 1 || stored.Shell != shellID {
		t.Fatalf("stored: %+v %v", stored, err)
	}

	// it runs in that shell with that variable
	var tk struct{ Ticket string }
	if code, _ := call("POST", base+"/commands/"+cmd.ID+"/ticket", nil, &tk); code != 200 {
		t.Fatalf("ticket: %d", code)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?ticket="+tk.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if code, body := call("POST", base+"/commands/"+cmd.ID+"/start", map[string]int{"cols": 100, "rows": 20}, nil); code != 200 {
		t.Fatalf("start: %d %s", code, body)
	}
	var got bytes.Buffer
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for !strings.Contains(got.String(), "V=val-123") {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("socket: %v (got %q)", err, got.String())
		}
		if kind == websocket.BinaryMessage {
			got.Write(data)
		}
	}

	// a shell the runner does not have fails at start, saying so: it never falls back to another
	var ghost dbpkg.ProjectCommand
	call("POST", base+"/commands", map[string]any{"name": "ghost", "command": line, "shell": "nope"}, &ghost)
	if code, body := call("POST", base+"/commands/"+ghost.ID+"/start", map[string]int{}, nil); code == 200 || !strings.Contains(body, "no shell") {
		t.Fatalf("an unknown shell started: %d %s", code, body)
	}

	// registering is the runner's: checked there, kept there, usable by id
	exe := list.Shells[0].Path
	if code, _ := call("POST", base+"/command-shells", map[string]any{"name": "rel", "path": "bin/sh"}, nil); code != 400 {
		t.Errorf("a relative path was registered: %d", code)
	}
	var mine struct{ ID string }
	if code, body := call("POST", base+"/command-shells", map[string]any{"name": "Mine", "path": exe}, &mine); code != 200 || !strings.HasPrefix(mine.ID, "custom-") {
		t.Fatalf("register: %d %s", code, body)
	}
	if code, _ := call("POST", base+"/command-shells", map[string]any{"name": "mine", "path": exe}, nil); code != 409 {
		t.Errorf("a second shell with the same name: %d", code)
	}
	// the hub stored nothing: the shell is in the runner's own database
	if _, err := dbpkg.GetTermShell(h.db, mine.ID); err == nil {
		t.Error("the hub's database holds a shell registered on the runner")
	}
	var usesMine dbpkg.ProjectCommand
	call("POST", base+"/commands", map[string]any{"name": "usesmine", "command": line, "shell": mine.ID}, &usesMine)
	if code, body := call("POST", base+"/commands/"+usesMine.ID+"/start", map[string]int{}, nil); code != 200 {
		t.Fatalf("a command in the registered shell did not start: %d %s", code, body)
	}
	call("POST", base+"/commands/"+usesMine.ID+"/stop", map[string]int{}, nil)
	waitStopped := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitStopped) {
		if code, _ := call("POST", base+"/commands/"+usesMine.ID+"/start", map[string]int{}, nil); code == 200 {
			break // it had already ended, so it can start again
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, _ := call("DELETE", base+"/command-shells/sh", nil, nil); code != 404 {
		t.Errorf("a detected shell was deleted: %d", code)
	}
	if code, _ := call("DELETE", base+"/command-shells/"+mine.ID, nil, nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	time.Sleep(500 * time.Millisecond)
	call("POST", base+"/commands/"+usesMine.ID+"/stop", map[string]int{}, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body := call("POST", base+"/commands/"+usesMine.ID+"/start", map[string]int{}, nil)
		if code != 200 && strings.Contains(body, "no shell") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a deleted shell still starts: %d %s", code, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
