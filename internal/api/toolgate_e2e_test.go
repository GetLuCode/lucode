package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/fakeanthropic"
	"claude-orchestrator/internal/flows"
	sessionpkg "claude-orchestrator/internal/session"
)

// toolGateRig is the road of an approval with nothing simulated but the Anthropic API: the real
// hook binary, as a runner's claude would launch it, over a real NATS to a hub with its engine.
type toolGateRig struct {
	t    *testing.T
	h    *Handler
	url  string
	hook string
	home string
	fake *fakeanthropic.Server
}

func newToolGateRig(t *testing.T) *toolGateRig {
	t.Helper()
	url := jetStreamURL(t)
	home := t.TempDir() // the hook's log and the user's permission rules stay out of this test
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	fake := useFakeAnthropic(t)

	hook := filepath.Join(t.TempDir(), "lucodehook.exe")
	if out, err := exec.Command("go", "build", "-o", hook, "claude-orchestrator/cmd/lucodehook").CombinedOutput(); err != nil {
		t.Fatalf("build the hook: %v\n%s", err, out)
	}

	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bus := busOn(t, url)
	manager := sessionpkg.NewManager(db, bus, url, "")
	if err := bus.Start(); err != nil {
		t.Fatal(err)
	}
	h := New(db, bus, manager, "")
	h.SetHubOnly(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.StartFlows(ctx)

	if err := dbpkg.InsertProject(db, dbpkg.Project{ID: "p1", Name: "p1", DefaultModel: "haiku"}); err != nil {
		t.Fatal(err)
	}
	return &toolGateRig{t: t, h: h, url: url, hook: hook, home: home, fake: fake}
}

func (r *toolGateRig) session(id string) {
	r.t.Helper()
	if err := dbpkg.InsertSession(r.h.db, dbpkg.Session{ID: id, ProjectID: "p1", Name: id, Model: "haiku", Status: "running"}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *toolGateRig) publishBlueprint() {
	r.t.Helper()
	bp, ok := flows.BlueprintOf(flows.BlueprintToolApproval)
	if !ok {
		r.t.Fatal("no blueprint")
	}
	r.publish(bp.Name, bp.Graph)
}

func (r *toolGateRig) publish(name string, g flows.Graph) {
	r.t.Helper()
	if problems := flows.Validate(g); len(problems) > 0 {
		r.t.Fatalf("graph does not validate: %+v", problems)
	}
	raw, _ := json.Marshal(g)
	f, err := dbpkg.InsertFlow(r.h.db, dbpkg.Flow{ProjectID: "p1", Name: name, Draft: raw})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := dbpkg.PublishFlow(r.h.db, f.ID, raw); err != nil {
		r.t.Fatal(err)
	}
}

// loggedCall is what the session's tool log keeps of a call.
type loggedCall struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Decision  string          `json:"decision"`
	DecidedBy string          `json:"decided_by"`
	Reason    string          `json:"reason"`
}

func (r *toolGateRig) toolLog(session string) []loggedCall {
	r.t.Helper()
	msgs, err := dbpkg.ListMessages(r.h.db, session)
	if err != nil {
		r.t.Fatal(err)
	}
	out := []loggedCall{}
	for _, m := range msgs {
		if m.Role != "tool_execution" {
			continue
		}
		var c loggedCall
		if err := json.Unmarshal([]byte(m.Content), &c); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

type hookResult struct {
	Decision, Reason string
}

// runHook runs the hook as claude does and returns what it would tell claude.
func (r *toolGateRig) runHook(session, toolUseID, tool, input string) hookResult {
	r.t.Helper()
	return r.runHookIn("default", session, toolUseID, tool, input)
}

// runHookIn is runHook for a claude in a permission mode. An empty Decision means the hook said
// nothing, which leaves the call to claude.
func (r *toolGateRig) runHookIn(mode, session, toolUseID, tool, input string) hookResult {
	r.t.Helper()
	cmd := exec.Command(r.hook)
	cmd.Env = append(os.Environ(), "LUCODE_SESSION_ID="+session, "LUCODE_NATS_URL="+r.url, "USERPROFILE="+r.home, "HOME="+r.home)
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"hook_event_name":"PreToolUse","permission_mode":%q,"tool_name":%q,"tool_use_id":%q,"cwd":"/work","tool_input":%s}`, mode, tool, toolUseID, input))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("hook: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		return hookResult{}
	}
	var reply struct {
		Out struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		r.t.Fatalf("hook output %q: %v", out.String(), err)
	}
	return hookResult{reply.Out.Decision, reply.Out.Reason}
}

func TestToolApprovalThroughTheRealHook(t *testing.T) {
	r := newToolGateRig(t)
	r.publishBlueprint()

	t.Run("a regex allows it and the session logs the call", func(t *testing.T) {
		r.session("read")
		if got := r.runHook("read", "t1", "Read", `{"file_path":"/work/a.go"}`); got.Decision != "allow" {
			t.Fatalf("got %+v", got)
		}
		calls := r.toolLog("read")
		if len(calls) != 1 || calls[0].ToolName != "Read" || calls[0].DecidedBy != "flow" || !strings.Contains(string(calls[0].ToolInput), "/work/a.go") {
			t.Errorf("log: %+v", calls)
		}
	})

	t.Run("a regex denies it and the agent reads why", func(t *testing.T) {
		r.session("bad")
		got := r.runHook("bad", "t3", "Bash", `{"command":"rm -rf ~"}`)
		if got.Decision != "deny" || !strings.Contains(got.Reason, "dangerous pattern") {
			t.Fatalf("got %+v", got)
		}
		if got := r.runHook("bad", "t4", "Bash", `{"command":"ls"}`); got.Decision != "deny" {
			t.Fatalf("the rest of the turn should stop: %+v", got)
		}
		if calls := r.toolLog("bad"); len(calls) != 2 || calls[0].Decision != "deny" {
			t.Errorf("log: %+v", calls)
		}
	})

	t.Run("a deny rule wins before any flow runs", func(t *testing.T) {
		r.session("ruled")
		if err := dbpkg.InsertSessionRule(r.h.db, dbpkg.SessionRuleRow{ID: "rule1", SessionID: "ruled", Tool: "Read", Scope: "deny", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if got := r.runHook("ruled", "t5", "Read", `{"file_path":"/work/a.go"}`); got.Decision != "deny" {
			t.Fatalf("got %+v", got)
		}
		if calls := r.toolLog("ruled"); len(calls) != 1 || calls[0].DecidedBy != "rule" {
			t.Errorf("log: %+v", calls)
		}
	})

	t.Run("an ask rule goes to a person even if the flow would allow it", func(t *testing.T) {
		r.session("askrule")
		if err := dbpkg.InsertSessionRule(r.h.db, dbpkg.SessionRuleRow{ID: "rule2", SessionID: "askrule", Tool: "Read", Scope: "ask", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		r.answerAsAPerson("askrule", "t6", "deny")
		if got := r.runHook("askrule", "t6", "Read", `{"file_path":"/work/a.go"}`); got.Decision != "deny" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("what no regex decides goes to a person", func(t *testing.T) {
		r.session("unsure")
		r.answerAsAPerson("unsure", "t7", "approve")
		if got := r.runHook("unsure", "t7", "Bash", `{"command":"make deploy"}`); got.Decision != "allow" {
			t.Fatalf("got %+v", got)
		}
		if calls := r.toolLog("unsure"); len(calls) != 0 {
			t.Errorf("a person's approval has its own message: %+v", calls)
		}
	})

	if n := len(r.fake.Requests()); n != 0 {
		t.Errorf("the base flow called the API %d times", n)
	}
}

// The AI is a node someone adds, and it only runs where they put it.
func TestToolFlowWithTheOptionalAINode(t *testing.T) {
	r := newToolGateRig(t)
	r.publish("with AI", flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeToolRequest},
			{ID: "ai", Type: flows.TypeAIEvaluate, Config: map[string]any{
				"instruction": "Is it safe?", "input": "{{event.tool_name}} {{event.tool_input_text}}",
				"outcome1": "safe", "outcome2": "ask", "model": "claude-opus-5-5", "effort": "low",
			}},
			{ID: "ok", Type: flows.TypeToolDecision, Config: map[string]any{"decision": "allow", "reason": "{{input}}"}},
			{ID: "person", Type: flows.TypeToolDecision, Config: map[string]any{"decision": "ask"}},
		},
		Edges: []flows.Edge{
			{From: "t", Port: flows.PortEvent, To: "ai"}, {From: "ai", Port: "outcome1", To: "ok"},
			{From: "ai", Port: "outcome2", To: "person"}, {From: "ai", Port: "failed", To: "person"},
		},
	})

	r.session("safe")
	r.fake.Queue(fakeanthropic.Reply{Text: `{"outcome":"safe","reason":"only lists files"}`})
	got := r.runHook("safe", "t1", "Bash", `{"command":"ls"}`)
	if got.Decision != "allow" || !strings.Contains(got.Reason, "only lists files") {
		t.Fatalf("got %+v", got)
	}
	reqs := r.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d API calls", len(reqs))
	}
	if body, _ := json.Marshal(reqs[0].Body["messages"]); !strings.Contains(string(body), "command") {
		t.Fatalf("the API did not see the command: %s", body)
	}

	r.session("down")
	r.fake.Queue(fakeanthropic.Reply{Status: 401, ErrType: "authentication_error", Message: "bad key"})
	r.answerAsAPerson("down", "t2", "deny")
	if got := r.runHook("down", "t2", "Bash", `{"command":"ls"}`); got.Decision != "deny" {
		t.Fatalf("got %+v", got)
	}
}

// A flow can treat each agent differently, whether the session names it or only its card does.
func TestToolFlowSeesTheAgent(t *testing.T) {
	r := newToolGateRig(t)
	for _, a := range []string{"reviewer", "dev"} {
		if err := dbpkg.InsertAgent(r.h.db, dbpkg.Agent{ID: a, ProjectID: "p1", Name: a, Model: "haiku", Executor: "claude", Tools: []string{"Read"}, LucodeTools: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbpkg.EnsureDefaultKanbanLists(r.h.db, "p1"); err != nil {
		t.Fatal(err)
	}
	todo, err := dbpkg.FindKanbanListByKind(r.h.db, "p1", dbpkg.KanbanTodo)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.InsertKanbanCard(r.h.db, dbpkg.KanbanCard{ID: "c1", ProjectID: "p1", ListID: todo.ID, Title: "the task", CardType: "task", Tags: []string{}, DependsOn: []string{}, AgentID: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	r.publish("by agent", flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeToolRequest},
			{ID: "if", Type: flows.TypeIf, Config: map[string]any{"condition": `event.agent == "reviewer"`}},
			{ID: "yes", Type: flows.TypeToolDecision, Config: map[string]any{"decision": "allow", "reason": "for {{event.agent}}"}},
			{ID: "no", Type: flows.TypeToolDecision, Config: map[string]any{"decision": "deny", "reason": "not for [{{event.agent}}]"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "if"}, {From: "if", Port: "true", To: "yes"}, {From: "if", Port: "false", To: "no"}},
	})

	for _, c := range []struct {
		name         string
		sess         dbpkg.Session
		want, reason string
	}{
		{"the session's agent", dbpkg.Session{ID: "s-reviewer", AgentID: "reviewer"}, "allow", "for reviewer"},
		{"another agent", dbpkg.Session{ID: "s-dev", AgentID: "dev"}, "deny", "not for [dev]"},
		{"the card's agent", dbpkg.Session{ID: "s-card", CardID: "c1"}, "allow", "for reviewer"},
		{"no agent at all", dbpkg.Session{ID: "s-none"}, "deny", "not for []"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.sess.ProjectID, c.sess.Name, c.sess.Model, c.sess.Status = "p1", c.sess.ID, "haiku", "running"
			if err := dbpkg.InsertSession(r.h.db, c.sess); err != nil {
				t.Fatal(err)
			}
			got := r.runHook(c.sess.ID, "t-"+c.sess.ID, "Bash", `{"command":"ls"}`)
			if got.Decision != c.want || !strings.Contains(got.Reason, c.reason) {
				t.Errorf("got %+v, want %s with %q", got, c.want, c.reason)
			}
		})
	}
}

// An agent in auto mode goes through the same road: rules and flows decide first, nobody is
// asked, and what stays open is left to claude. Every call stays in the log.
func TestAutoModeGoesThroughTheHook(t *testing.T) {
	r := newToolGateRig(t)

	t.Run("without a flow, claude decides", func(t *testing.T) {
		r.session("plainauto")
		if got := r.runHookIn("auto", "plainauto", "t1", "Bash", `{"command":"make"}`); got.Decision != "" {
			t.Fatalf("the hook should say nothing: %+v", got)
		}
		calls := r.toolLog("plainauto")
		if len(calls) != 1 || calls[0].Decision != "auto" || !strings.Contains(string(calls[0].ToolInput), "make") {
			t.Errorf("log: %+v", calls)
		}
		if _, err := dbpkg.GetPendingHook(r.h.db, "plainauto", "t1"); err == nil {
			t.Error("nobody should be asked")
		}
	})

	r.publishBlueprint()

	t.Run("the flow still allows", func(t *testing.T) {
		r.session("autoread")
		if got := r.runHookIn("auto", "autoread", "t2", "Read", `{"file_path":"/a"}`); got.Decision != "allow" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("the flow still denies", func(t *testing.T) {
		r.session("autobad")
		if got := r.runHookIn("auto", "autobad", "t3", "Bash", `{"command":"rm -rf /"}`); got.Decision != "deny" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("what the flow leaves to a person goes to claude", func(t *testing.T) {
		r.session("autoask")
		if got := r.runHookIn("auto", "autoask", "t4", "Bash", `{"command":"make"}`); got.Decision != "" {
			t.Fatalf("the hook should say nothing: %+v", got)
		}
		if calls := r.toolLog("autoask"); len(calls) != 1 || calls[0].DecidedBy != "auto" {
			t.Errorf("log: %+v", calls)
		}
	})

	t.Run("a deny rule still wins", func(t *testing.T) {
		r.session("autorule")
		if err := dbpkg.InsertSessionRule(r.h.db, dbpkg.SessionRuleRow{ID: "rule3", SessionID: "autorule", Tool: "Bash", Scope: "deny", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if got := r.runHookIn("auto", "autorule", "t5", "Bash", `{"command":"make"}`); got.Decision != "deny" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("an ask rule still reaches a person", func(t *testing.T) {
		r.session("autoaskrule")
		if err := dbpkg.InsertSessionRule(r.h.db, dbpkg.SessionRuleRow{ID: "rule4", SessionID: "autoaskrule", Tool: "Bash", Scope: "ask", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		r.answerAsAPerson("autoaskrule", "t6", "approve")
		if got := r.runHookIn("auto", "autoaskrule", "t6", "Bash", `{"command":"make"}`); got.Decision != "allow" {
			t.Fatalf("got %+v", got)
		}
	})
}

// Without a flow, nothing changes: the person answers, as before.
func TestToolApprovalWithoutAFlowAsksAPerson(t *testing.T) {
	r := newToolGateRig(t)
	r.session("plain")
	r.answerAsAPerson("plain", "t1", "approve")
	if got := r.runHook("plain", "t1", "Bash", `{"command":"ls"}`); got.Decision != "allow" {
		t.Fatalf("got %+v", got)
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Errorf("%d API calls", n)
	}
}

// answerAsAPerson waits for the approval to show up, then answers it like the page does.
func (r *toolGateRig) answerAsAPerson(session, toolUseID, decision string) {
	go func() {
		for i := 0; i < 300; i++ {
			if _, err := dbpkg.GetPendingHook(r.h.db, session, toolUseID); err == nil {
				r.h.bus.OnHookRespond(session, toolUseID, decision, "the person said so", "")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		r.t.Errorf("no approval for %s reached a person", toolUseID)
	}()
}
