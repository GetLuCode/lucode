package api

import (
	"path/filepath"
	"strings"
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
	p := agentSystemPrompt(dbpkg.Agent{Name: "Engineer", Personality: "You write Go."}, dbpkg.KanbanCard{Title: "Fix login"}, "Lead")
	for _, want := range []string{"You write Go.", "Engineer", "Fix login", "task_done", "task_blocked", "task_comment", "send_to_agent", "task_info", "agent_list", "#RequiresAnswer", "Your supervisor is Lead"} {
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

func TestGetTaskImageReturnsAnImageAndListsOnMiss(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["inprogress"], "task")
	sess := taskSession(t, h, card)

	im, err := dbpkg.InsertCardImage(h.db, card.ID, "image/png", []byte("PNGDATA"))
	if err != nil || im.Name != "image1" {
		t.Fatalf("first image should be image1, got %q %v", im.Name, err)
	}
	if im2, _ := dbpkg.InsertCardImage(h.db, card.ID, "image/png", []byte("x")); im2.Name != "image2" {
		t.Errorf("second image should be image2, got %q", im2.Name)
	}

	for _, name := range []string{"image1", "@image1"} {
		out, err := h.HandleTaskTool(sess.ID, "get_task_image", []byte(`{"name":"`+name+`"}`))
		if err != nil || !strings.HasPrefix(out, "lucode-image:image/png\n") {
			t.Fatalf("get_task_image(%s) = %q, %v", name, out, err)
		}
	}
	if _, err := h.HandleTaskTool(sess.ID, "get_task_image", []byte(`{"name":"image9"}`)); err == nil || !strings.Contains(err.Error(), "image1, image2") {
		t.Errorf("a missing image should list the available ones, got %v", err)
	}
	if p := h.taskPrompt(card); !strings.Contains(p, "Attached images: image1, image2") {
		t.Errorf("the task prompt should list the images, got %q", p)
	}
}

func TestTaskManagementTools(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	mine := newCard(t, h, pid, lists["inprogress"], "mine")
	sess := taskSession(t, h, mine)
	call := func(tool, args string) (string, error) { return h.HandleTaskTool(sess.ID, tool, []byte(args)) }

	// create: Backlog by default, any status by name
	if _, err := call("task_create", `{"title":"  "}`); err == nil {
		t.Error("task_create without a title must fail")
	}
	out, err := call("task_create", `{"title":"Backlog one","status":"backlog","description":"d","tags":["x"]}`)
	if err != nil || !strings.Contains(out, "Backlog") {
		t.Fatalf("task_create = %q, %v", out, err)
	}
	if _, err := call("task_create", `{"title":"To do one","status":"To Do"}`); err != nil {
		t.Fatalf("status by list name must work: %v", err)
	}
	if _, err := call("task_create", `{"title":"x","status":"nowhere"}`); err == nil || !strings.Contains(err.Error(), "Backlog") {
		t.Errorf("an unknown status should list the lists, got %v", err)
	}
	if _, err := call("task_create", `{"title":"x","agent":"ghost"}`); err == nil {
		t.Error("an unknown agent must fail")
	}
	if _, err := call("task_create", `{"title":"x","depends_on":["nope"]}`); err == nil {
		t.Error("an unknown dependency must fail")
	}

	// list
	all, _ := call("task_list", `{}`)
	if !strings.Contains(all, "Backlog one") || !strings.Contains(all, "To do one") || !strings.Contains(all, "mine") {
		t.Errorf("task_list should show every task, got:\n%s", all)
	}
	onlyTodo, _ := call("task_list", `{"status":"todo"}`)
	if strings.Contains(onlyTodo, "Backlog one") || !strings.Contains(onlyTodo, "To do one") {
		t.Errorf("task_list(status=todo) = %s", onlyTodo)
	}

	// update: fields, status through the workflow, own-task guard, validation
	cards, _ := dbpkg.ListKanbanCards(h.db, pid, false)
	var backlogID string
	for _, c := range cards {
		if c.Title == "Backlog one" {
			backlogID = c.ID
		}
	}
	if _, err := call("task_update", `{"task_id":"`+backlogID+`","title":"Renamed","status":"done"}`); err != nil {
		t.Fatalf("task_update: %v", err)
	}
	got, _ := dbpkg.GetKanbanCard(h.db, backlogID)
	if got.Title != "Renamed" || got.ListID != lists["done"].ID {
		t.Errorf("update should rename and move to Done, got %q in %q", got.Title, got.ListID)
	}
	if _, err := call("task_update", `{"task_id":"`+mine.ID+`","status":"done"}`); err == nil {
		t.Error("an agent must not change its own task's status with task_update")
	}
	if _, err := call("task_update", `{"task_id":"`+mine.ID+`","description":"more detail"}`); err != nil {
		t.Errorf("other fields of the own task may change: %v", err)
	}
	if _, err := call("task_update", `{"task_id":"`+backlogID+`","depends_on":["`+backlogID+`"]}`); err == nil {
		t.Error("a task cannot depend on itself")
	}
	if _, err := call("task_update", `{"task_id":"`+backlogID+`"}`); err == nil {
		t.Error("update with no fields must fail")
	}
	if _, err := call("task_update", `{"task_id":"missing","title":"x"}`); err == nil {
		t.Error("an unknown task must fail")
	}
}

func TestTaskCreateDefaultsToTodoOrInProgressWhenTheAgentIsFree(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	alice := addMember(t, h, pid, lists, "Alice", "", "inprogress") // busy: a task in progress
	carol := addMember(t, h, pid, lists, "Carol", "", "todo")       // free: her task waits in Todo
	c := &capture{}
	h.startTurnFn = c.fn
	create := func(args string) dbpkg.KanbanCard {
		t.Helper()
		out, err := h.HandleTaskTool(alice.sess.ID, "task_create", []byte(args))
		if err != nil {
			t.Fatalf("task_create %s: %v", args, err)
		}
		cards, _ := dbpkg.ListKanbanCards(h.db, pid, false)
		for _, card := range cards {
			if strings.Contains(out, card.ID) {
				return card
			}
		}
		t.Fatalf("created task not found in %q", out)
		return dbpkg.KanbanCard{}
	}
	inList := func(card dbpkg.KanbanCard, kind string) bool { return card.ListID == lists[kind].ID }

	if got := create(`{"title":"no agent"}`); !inList(got, "todo") {
		t.Error("a task with no agent goes to Todo")
	}
	if got := create(`{"title":"busy","agent":"Alice"}`); !inList(got, "todo") {
		t.Error("a task for a busy agent goes to Todo")
	}
	dep := newCard(t, h, pid, lists["backlog"], "dep")
	if got := create(`{"title":"waits","agent":"Carol","depends_on":["` + dep.ID + `"]}`); !inList(got, "todo") {
		t.Error("a task with pending dependencies goes to Todo")
	}
	if len(c.opts) != 0 {
		t.Fatalf("nothing should have started yet, got %d turns", len(c.opts))
	}
	if got := create(`{"title":"now","agent":"Carol","status":"backlog"}`); !inList(got, "backlog") {
		t.Error("an explicit status wins")
	}
	if got := create(`{"title":"go","agent":"Carol"}`); !inList(got, "inprogress") {
		t.Errorf("a free agent starts the task in In Progress, got list %q", got.ListID)
	}
	if len(c.opts) != 1 {
		t.Errorf("the free agent should have started a turn, got %d", len(c.opts))
	}
	_ = carol
}
