package api

import (
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
)

// team builds an agent with an In Progress task and its session.
type member struct {
	agent dbpkg.Agent
	card  dbpkg.KanbanCard
	sess  dbpkg.Session
}

func addMember(t *testing.T, h *Handler, pid string, lists map[string]dbpkg.KanbanList, name, reportsTo, listKind string) member {
	t.Helper()
	a := dbpkg.Agent{
		ID: uuid.New().String(), ProjectID: pid, Name: name, Model: "haiku",
		Personality: name + " does things.\nSecond line.", Tools: []string{"Read"}, Executor: "claude", Persistent: true, ReportsTo: reportsTo,
	}
	if err := dbpkg.InsertAgent(h.db, a); err != nil {
		t.Fatal(err)
	}
	card := newCard(t, h, pid, lists[listKind], name+" task")
	card.AgentID = a.ID
	if err := dbpkg.UpdateKanbanCard(h.db, card); err != nil {
		t.Fatal(err)
	}
	s := taskSession(t, h, card)
	s.AgentID = a.ID
	if err := dbpkg.UpdateSessionAgent(h.db, s.ID, a.ID, "haiku"); err != nil {
		t.Fatal(err)
	}
	return member{a, card, s}
}

// capture records the turns the handler tries to start.
type capture struct {
	opts []sessionpkg.TurnOptions
	err  error
}

func (c *capture) fn(o sessionpkg.TurnOptions) error {
	if c.err == nil {
		c.opts = append(c.opts, o)
	}
	return c.err
}

func TestAgentMessageIsDeliveredToTheRecipientsTask(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")
	bob := addMember(t, h, pid, lists, "Bob", "", "inprogress")
	c := &capture{}
	h.startTurnFn = c.fn

	out, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"bob","message":"please review the API"}`))
	if err != nil {
		t.Fatalf("agent_message failed: %v", err)
	}
	if !strings.Contains(out, "delivered") {
		t.Errorf("unexpected result %q", out)
	}
	if len(c.opts) != 1 || c.opts[0].SessionID != bob.sess.ID {
		t.Fatalf("a turn should start on Bob's session, got %+v", c.opts)
	}
	if m := c.opts[0].Message; !strings.Contains(m, "please review the API") || !strings.Contains(m, `Message from agent "Alice"`) {
		t.Errorf("message should say who it is from, got %q", m)
	}
	if !c.opts[0].Persistent || c.opts[0].Executor != "claude" {
		t.Error("the turn must use Bob's agent configuration")
	}
}

func TestAgentMessageToABusyAgentIsQueuedAndDrained(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")
	bob := addMember(t, h, pid, lists, "Bob", "", "inprogress")

	busy := &capture{err: sessionpkg.ErrSessionBusy}
	h.startTurnFn = busy.fn
	out, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Bob","message":"ping"}`))
	if err != nil || !strings.Contains(out, "queued") {
		t.Fatalf("a busy recipient should queue the message, got %q %v", out, err)
	}

	free := &capture{}
	h.startTurnFn = free.fn
	h.DrainInbox(bob.sess.ID) // Bob's turn ended
	if len(free.opts) != 1 || !strings.Contains(free.opts[0].Message, "ping") {
		t.Fatalf("the queued message should be delivered when the turn ends, got %+v", free.opts)
	}
	h.DrainInbox(bob.sess.ID)
	if len(free.opts) != 1 {
		t.Error("the inbox must be empty after delivery")
	}
}

func TestAgentMessageErrors(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")
	addMember(t, h, pid, lists, "Carol", "", "todo") // no task in progress
	h.startTurnFn = (&capture{}).fn

	if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Carol","message":"x"}`)); err == nil || !strings.Contains(err.Error(), "no task in progress") {
		t.Errorf("a recipient without a task in progress must be refused, got %v", err)
	}
	if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Nobody","message":"x"}`)); err == nil || !strings.Contains(err.Error(), "Carol") {
		t.Errorf("an unknown agent should list the teammates, got %v", err)
	}
	if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Carol","message":" "}`)); err == nil {
		t.Error("an empty message must be refused")
	}
	if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"supervisor","message":"x"}`)); err == nil {
		t.Error("supervisor must fail for an agent that reports to nobody")
	}
}

func TestAgentMessageRateLimit(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress")
	addMember(t, h, pid, lists, "Bob", "", "inprogress")
	h.startTurnFn = (&capture{}).fn

	for i := 0; i < msgLimit; i++ {
		if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Bob","message":"x"}`)); err != nil {
			t.Fatalf("message %d should pass: %v", i+1, err)
		}
	}
	if _, err := h.HandleTaskTool(alice.sess.ID, "agent_message", []byte(`{"to":"Bob","message":"x"}`)); err == nil {
		t.Error("the message after the limit must be refused (guards against agents answering each other forever)")
	}
}

func TestAgentList(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	lead := addMember(t, h, pid, lists, "Lead", "", "inprogress")
	dev := addMember(t, h, pid, lists, "Dev", lead.agent.ID, "inprogress")
	addMember(t, h, pid, lists, "Idle", "", "backlog")

	out, err := h.HandleTaskTool(dev.sess.ID, "agent_list", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Your supervisor: Lead", "Lead", "Idle", `working on "Lead task"`, "idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent_list should mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "- Dev") {
		t.Error("agent_list must not list the caller")
	}
}

func TestSupervisorIsToldWhenSubordinateIsDoneOrBlocked(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	lead := addMember(t, h, pid, lists, "Lead", "", "inprogress")
	dev := addMember(t, h, pid, lists, "Dev", lead.agent.ID, "inprogress")
	c := &capture{}
	h.startTurnFn = c.fn

	if _, err := h.HandleTaskTool(dev.sess.ID, "task_done", []byte(`{"summary":"shipped it"}`)); err != nil {
		t.Fatal(err)
	}
	if len(c.opts) != 1 || c.opts[0].SessionID != lead.sess.ID || !strings.Contains(c.opts[0].Message, "is done: shipped it") {
		t.Fatalf("the supervisor should hear about the completion, got %+v", c.opts)
	}

	other := addMember(t, h, pid, lists, "Dev2", lead.agent.ID, "inprogress")
	if _, err := h.HandleTaskTool(other.sess.ID, "task_blocked", []byte(`{"reason":"need a key"}`)); err != nil {
		t.Fatal(err)
	}
	if len(c.opts) != 2 || !strings.Contains(c.opts[1].Message, "is blocked: need a key") {
		t.Fatalf("the supervisor should hear about the blocker, got %+v", c.opts)
	}

	// "supervisor" is an alias for the agent it reports to
	if _, err := h.HandleTaskTool(dev.sess.ID, "agent_message", []byte(`{"to":"supervisor","message":"question"}`)); err != nil {
		t.Fatalf("to=supervisor should resolve: %v", err)
	}
	if len(c.opts) != 3 || c.opts[2].SessionID != lead.sess.ID {
		t.Error("to=supervisor should reach the lead")
	}
}

func TestUnresumableSessionBlocksTaskAndAlertsSupervisor(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	lead := addMember(t, h, pid, lists, "Lead", "", "inprogress")
	dev := addMember(t, h, pid, lists, "Dev", lead.agent.ID, "inprogress")
	c := &capture{}
	h.startTurnFn = c.fn

	h.HandleTurnFailed(dev.sess.ID, "resume_failed", "This session can no longer be resumed")

	card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
	if card.ListID != lists["blocked"].ID {
		t.Errorf("the task should be Blocked, is in %q", card.ListID)
	}
	comments, _ := dbpkg.ListCardComments(h.db, dev.card.ID)
	if len(comments) != 1 || comments[0].Kind != "error" || !strings.Contains(comments[0].Body, "no longer be resumed") {
		t.Errorf("the failure should be recorded on the task, got %+v", comments)
	}
	if len(c.opts) != 1 || c.opts[0].SessionID != lead.sess.ID || !strings.Contains(c.opts[0].Message, "failed") {
		t.Errorf("the supervisor should be alerted, got %+v", c.opts)
	}
}

func TestValidateReportsTo(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	a := addMember(t, h, pid, lists, "A", "", "todo")
	b := addMember(t, h, pid, lists, "B", a.agent.ID, "todo")

	if err := h.validateReportsTo(a.agent.ID, pid, ""); err != nil {
		t.Errorf("no supervisor is fine: %v", err)
	}
	if err := h.validateReportsTo(a.agent.ID, pid, a.agent.ID); err == nil {
		t.Error("an agent cannot report to itself")
	}
	if err := h.validateReportsTo(a.agent.ID, pid, b.agent.ID); err == nil {
		t.Error("A -> B while B -> A is a loop")
	}
	if err := h.validateReportsTo(b.agent.ID, "another-project", a.agent.ID); err == nil {
		t.Error("the supervisor must belong to the same project")
	}
	if err := h.validateReportsTo("", pid, a.agent.ID); err != nil {
		t.Errorf("a new agent can report to an existing one: %v", err)
	}
}
