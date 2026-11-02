package api

import (
	"path/filepath"
	"testing"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

// newTaskTestHandler returns a Handler backed by a temp database. The manager
// is nil: these tests only exercise paths that never start a turn (cards have
// no agent), which covers lists, blocking and release.
func newTaskTestHandler(t *testing.T) (*Handler, string, map[string]dbpkg.KanbanList) {
	t.Helper()
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	proj := dbpkg.Project{ID: uuid.New().String(), Name: "p", WorkDirs: []string{"."}, DefaultModel: "haiku"}
	if err := dbpkg.InsertProject(db, proj); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureDefaultKanbanLists(db, proj.ID); err != nil {
		t.Fatal(err)
	}
	lists := map[string]dbpkg.KanbanList{}
	all, _ := dbpkg.ListKanbanLists(db, proj.ID)
	for _, l := range all {
		lists[l.Kind] = l
	}
	return &Handler{db: db}, proj.ID, lists
}

func newCard(t *testing.T, h *Handler, pid string, list dbpkg.KanbanList, title string, deps ...string) dbpkg.KanbanCard {
	t.Helper()
	c := dbpkg.KanbanCard{
		ID: uuid.New().String(), ListID: list.ID, ProjectID: pid, Title: title,
		CardType: "task", Tags: []string{}, DependsOn: deps,
	}
	if err := dbpkg.InsertKanbanCard(h.db, c); err != nil {
		t.Fatal(err)
	}
	got, err := dbpkg.GetKanbanCard(h.db, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDefaultKanbanLists(t *testing.T) {
	_, _, lists := newTaskTestHandler(t)
	for _, kind := range []string{"backlog", "todo", "inprogress", "done", "blocked"} {
		if _, ok := lists[kind]; !ok {
			t.Errorf("missing default list %q", kind)
		}
	}
	if len(lists) != 5 {
		t.Errorf("want 5 default lists, got %d", len(lists))
	}
}

func TestMoveToInProgressBlocksOnUnmetDependency(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	dep := newCard(t, h, pid, lists["todo"], "dep")
	task := newCard(t, h, pid, lists["inprogress"], "task", dep.ID)

	got := h.afterCardSaved(task)
	if got.ListID != lists["blocked"].ID {
		t.Fatalf("task with an unmet dependency should be parked in Blocked, is in list %q", got.ListID)
	}
}

func TestDoneReleasesBlockedDependents(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	dep := newCard(t, h, pid, lists["inprogress"], "dep")
	task := newCard(t, h, pid, lists["inprogress"], "task", dep.ID)
	task = h.afterCardSaved(task) // blocked
	if task.ListID != lists["blocked"].ID {
		t.Fatal("setup: task should start blocked")
	}

	// dep finishes: move it to Done and run the workflow
	dep.ListID = lists["done"].ID
	if err := dbpkg.UpdateKanbanCard(h.db, dep); err != nil {
		t.Fatal(err)
	}
	h.afterCardSaved(dep)

	got, _ := dbpkg.GetKanbanCard(h.db, task.ID)
	if got.ListID != lists["inprogress"].ID {
		t.Fatalf("dependent should move to In Progress once its dependency is Done, is in %q", got.ListID)
	}
}

func TestPartiallyDoneDependenciesStayBlocked(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	a := newCard(t, h, pid, lists["inprogress"], "a")
	b := newCard(t, h, pid, lists["todo"], "b")
	task := h.afterCardSaved(newCard(t, h, pid, lists["inprogress"], "task", a.ID, b.ID))

	a.ListID = lists["done"].ID
	dbpkg.UpdateKanbanCard(h.db, a)
	h.afterCardSaved(a)

	got, _ := dbpkg.GetKanbanCard(h.db, task.ID)
	if got.ListID != lists["blocked"].ID {
		t.Fatalf("task must stay Blocked while b is not Done, is in %q", got.ListID)
	}
}

// taskSession links a session to a card, as ensureCardSession would.
func taskSession(t *testing.T, h *Handler, card dbpkg.KanbanCard) dbpkg.Session {
	t.Helper()
	s := dbpkg.Session{
		ID: uuid.New().String(), ProjectID: card.ProjectID, Name: card.Title,
		Model: "haiku", Status: "idle", CardID: card.ID,
	}
	if err := dbpkg.InsertSession(h.db, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTaskDoneMovesCardPostsSummaryAndReleasesDependents(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	dep := newCard(t, h, pid, lists["inprogress"], "dep")
	waiting := h.afterCardSaved(newCard(t, h, pid, lists["inprogress"], "waiting", dep.ID))
	if waiting.ListID != lists["blocked"].ID {
		t.Fatal("setup: dependent should be blocked")
	}
	sess := taskSession(t, h, dep)

	msg, err := h.HandleTaskTool(sess.ID, "task_done", []byte(`{"summary":"built it"}`))
	if err != nil || msg == "" {
		t.Fatalf("task_done failed: %q %v", msg, err)
	}

	got, _ := dbpkg.GetKanbanCard(h.db, dep.ID)
	if got.ListID != lists["done"].ID {
		t.Errorf("card should be in Done, is in %q", got.ListID)
	}
	released, _ := dbpkg.GetKanbanCard(h.db, waiting.ID)
	if released.ListID != lists["inprogress"].ID {
		t.Errorf("dependent should be released to In Progress, is in %q", released.ListID)
	}
	comments, _ := dbpkg.ListCardComments(h.db, dep.ID)
	if len(comments) != 1 || comments[0].Kind != "done" || comments[0].Body != "built it" {
		t.Errorf("summary should be posted as a done comment, got %+v", comments)
	}
}

func TestTaskBlockedAndCommentAndValidation(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["inprogress"], "task")
	sess := taskSession(t, h, card)

	if _, err := h.HandleTaskTool(sess.ID, "task_comment", []byte(`{"text":"halfway"}`)); err != nil {
		t.Fatal(err)
	}
	if c, _ := dbpkg.GetKanbanCard(h.db, card.ID); c.ListID != lists["inprogress"].ID {
		t.Error("a comment must not change the status")
	}

	if _, err := h.HandleTaskTool(sess.ID, "task_blocked", []byte(`{}`)); err == nil {
		t.Error("task_blocked without a reason must fail")
	}
	if _, err := h.HandleTaskTool(sess.ID, "task_done", []byte(`{"summary":"  "}`)); err == nil {
		t.Error("task_done without a summary must fail")
	}

	if _, err := h.HandleTaskTool(sess.ID, "mcp__lucode__task_blocked", []byte(`{"reason":"need an API key"}`)); err != nil {
		t.Fatal(err)
	}
	if c, _ := dbpkg.GetKanbanCard(h.db, card.ID); c.ListID != lists["blocked"].ID {
		t.Error("task_blocked should move the card to Blocked")
	}
	comments, _ := dbpkg.ListCardComments(h.db, card.ID)
	if len(comments) != 2 {
		t.Errorf("want 2 comments (note + blocked), got %d", len(comments))
	}
}

func TestTaskToolRejectsNonTaskSession(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	s := dbpkg.Session{ID: uuid.New().String(), ProjectID: pid, Model: "haiku", Status: "idle"}
	if err := dbpkg.InsertSession(h.db, s); err != nil {
		t.Fatal(err)
	}
	if _, err := h.HandleTaskTool(s.ID, "task_done", []byte(`{"summary":"x"}`)); err == nil {
		t.Error("a session without a task must be rejected")
	}
}

func TestAgentSystemPromptHasPersonalityAndTaskInstructions(t *testing.T) {
	p := agentSystemPrompt(dbpkg.Agent{Name: "Engineer", Personality: "You write Go."}, dbpkg.KanbanCard{Title: "Fix login"})
	for _, want := range []string{"You write Go.", "Engineer", "Fix login", "task_done", "task_blocked", "task_comment"} {
		if !contains2(p, want) {
			t.Errorf("system prompt is missing %q", want)
		}
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
