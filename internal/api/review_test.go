package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func rgit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reviewRepo makes a repo on main with a.txt and a linked worktree on branch task/x.
func reviewRepo(t *testing.T) (repo, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	rgit(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	rgit(t, repo, "add", ".")
	rgit(t, repo, "commit", "-q", "-m", "init")
	wt = filepath.Join(root, "wt")
	rgit(t, repo, "worktree", "add", "-q", "-b", "task/x", wt)
	return repo, wt
}

func TestReviewStatusCleanThenMerge(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(wt, "b.txt"), "new\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "add b")

	h, pid, lists := newTaskTestHandler(t)
	if _, ok := lists["review"]; !ok {
		t.Fatal("the default lists have no review list")
	}
	card := newCard(t, h, pid, lists["review"], "Add b")
	rv := dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}
	if err := dbpkg.SaveCardReview(h.db, rv); err != nil {
		t.Fatal(err)
	}

	st := reviewStatus(rv)
	if st.Error != "" || st.Base != "main" || st.Ahead != 1 || st.Conflicts || st.Dirty || st.Merged {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Files) != 1 || st.Files[0] != "A b.txt" {
		t.Errorf("files = %v", st.Files)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", card.ID)
	rec := httptest.NewRecorder()
	h.mergeCardReview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Errorf("b.txt was not merged into the main checkout: %v", err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("the worktree was not removed")
	}
	got, _ := dbpkg.GetKanbanCard(h.db, card.ID)
	if got.ListID != lists["done"].ID {
		t.Errorf("card is not in Done")
	}
	if _, err := dbpkg.GetCardReview(h.db, card.ID); err == nil {
		t.Errorf("review row was kept")
	}
}

// claude leaves its session worktrees locked: merging must still remove the
// worktree and delete the branch.
func TestMergeRemovesLockedWorktree(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(wt, "b.txt"), "new\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "add b")
	rgit(t, repo, "worktree", "lock", "--reason", "claude session test (pid 1)", wt)

	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["review"], "Add b")
	rv := dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}
	if err := dbpkg.SaveCardReview(h.db, rv); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", card.ID)
	rec := httptest.NewRecorder()
	h.mergeCardReview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "could not") {
		t.Errorf("warnings: %s", rec.Body)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("the locked worktree was not removed")
	}
	if _, err := gitIn(repo, "rev-parse", "--verify", "--quiet", "refs/heads/task/x"); err == nil {
		t.Errorf("the branch was not deleted")
	}
}

// Once the merge deleted the worktree, the task's chat is closed: a turn would
// start claude in a directory that no longer exists.
func TestMergeClosesTheTaskChat(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(wt, "b.txt"), "new\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "add b")

	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["review"], "Add b")
	sess := dbpkg.Session{ID: "s1", ProjectID: pid, Name: "s", Model: "haiku", Status: "idle", CardID: card.ID}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		t.Fatal(err)
	}
	rv := dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}
	if err := dbpkg.SaveCardReview(h.db, rv); err != nil {
		t.Fatal(err)
	}

	turn := func() int {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"message":"hi"}`))
		req.SetPathValue("id", "s1")
		rec := httptest.NewRecorder()
		h.sendTurn(rec, req)
		return rec.Code
	}
	if got, _ := dbpkg.GetSession(h.db, "s1"); got.WorktreeRemoved {
		t.Fatal("chat closed before the merge")
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", card.ID)
	rec := httptest.NewRecorder()
	h.mergeCardReview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	if got, _ := dbpkg.GetSession(h.db, "s1"); !got.WorktreeRemoved {
		t.Errorf("session not marked after the worktree was deleted")
	}
	if code := turn(); code != http.StatusConflict {
		t.Errorf("turn on the closed chat: %d, want 409", code)
	}
}

func TestReviewStatusConflictAndDirty(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(wt, "a.txt"), "from branch\n")
	rgit(t, wt, "commit", "-q", "-am", "branch edit")
	write(t, filepath.Join(repo, "a.txt"), "from main\n")
	rgit(t, repo, "commit", "-q", "-am", "main edit")

	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["review"], "Conflict")
	rv := dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}
	_ = dbpkg.SaveCardReview(h.db, rv)

	st := reviewStatus(rv)
	if !st.Conflicts || len(st.ConflictFiles) != 1 || st.ConflictFiles[0] != "a.txt" {
		t.Fatalf("status = %+v", st)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", card.ID)
	rec := httptest.NewRecorder()
	h.mergeCardReview(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("merge with conflicts: %d, want 409", rec.Code)
	}
	if out, _ := gitIn(repo, "status", "--porcelain"); out != "" {
		t.Errorf("the main checkout was touched: %q", out)
	}

	write(t, filepath.Join(wt, "c.txt"), "uncommitted\n")
	if !reviewStatus(rv).Dirty {
		t.Errorf("uncommitted file not reported")
	}
}

func TestEnsureReviewListOnOldProject(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	if err := dbpkg.DeleteKanbanList(h.db, lists["review"].ID); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureReviewList(h.db, pid); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureReviewList(h.db, pid); err != nil { // idempotent
		t.Fatal(err)
	}
	all, _ := dbpkg.ListKanbanLists(h.db, pid)
	var kinds []string
	for _, l := range all {
		kinds = append(kinds, l.Kind)
	}
	want := []string{"backlog", "todo", "inprogress", "review", "done", "blocked"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}

func TestListedCardCarriesItsSessionError(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	card := newCard(t, h, pid, lists["inprogress"], "Fails")
	sess := dbpkg.Session{ID: "s1", ProjectID: pid, Name: "s", Model: "haiku", Status: "error", CardID: card.ID}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"old failure", "start claude: chdir lucode: not found"} {
		if err := dbpkg.InsertCardComment(h.db, dbpkg.CardComment{ID: body, CardID: card.ID, SessionID: "s1", Author: "lucode", Kind: "error", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if got := dbpkg.LatestCardErrors(h.db, pid)[card.ID]; got != "start claude: chdir lucode: not found" {
		t.Errorf("last error = %q", got)
	}
	dbpkg.UpdateSessionStatus(h.db, "s1", "idle") // recovered: no error shown
	if got := dbpkg.LatestCardErrors(h.db, pid)[card.ID]; got != "" {
		t.Errorf("after recovering: %q", got)
	}
}

func TestCardMovedToAListGoesOnTop(t *testing.T) {
	h, pid, lists := newTaskTestHandler(t)
	older := newCard(t, h, pid, lists["done"], "older")
	newer := newCard(t, h, pid, lists["inprogress"], "newer")
	moved, err := h.moveCardToKind(newer, dbpkg.KanbanDone)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ListID != lists["done"].ID || moved.Position >= older.Position {
		t.Errorf("moved card at %d, the existing one at %d: it should be first", moved.Position, older.Position)
	}
	// moving into an empty list works too
	alone, err := h.moveCardToKind(older, dbpkg.KanbanBlocked)
	if err != nil || alone.ListID != lists["blocked"].ID {
		t.Fatalf("empty list: %v %+v", err, alone)
	}
}

func TestRunnerAnswersItsVersion(t *testing.T) {
	h, _, _ := newTaskTestHandler(t)
	rec := httptest.NewRecorder()
	h.version("runner")(rec, httptest.NewRequest("GET", "/api/version", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"runner"`) || !strings.Contains(rec.Body.String(), `"short"`) {
		t.Errorf("version: %d %s", rec.Code, rec.Body)
	}
}

func TestCommandHistoryAndSessionCommand(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	sess := dbpkg.Session{ID: "s1", ProjectID: pid, Name: "s", Model: "haiku", Status: "idle"}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		t.Fatal(err)
	}
	// one launched here, one reported by a runner
	local := dbpkg.ClaudeCommandFromEvent(map[string]any{"session_id": "s1", "exe": "claude", "args": []string{"-p", "--model=haiku"}, "cwd": "E:/dev/lucode", "pid": 42, "at": "2026-11-04T10:00:00Z"}, "")
	remote := dbpkg.ClaudeCommandFromEvent(map[string]any{"session_id": "s1", "exe": "/usr/bin/claude", "args": []any{"-p", "--resume=x"}, "cwd": "/home/m4rk/dev", "pid": float64(7), "at": "2026-11-04T11:00:00Z"}, "runner-1")
	for _, c := range []dbpkg.ClaudeCommand{local, remote} {
		if err := dbpkg.InsertClaudeCommand(h.db, c); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	h.listCommands(rec, httptest.NewRequest("GET", "/api/commands", nil))
	var list []dbpkg.ClaudeCommand
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].PID != 42 || list[0].Args[1] != "--model=haiku" {
		t.Errorf("this instance's history = %v (%v)", rec.Body, err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", "s1")
	rec = httptest.NewRecorder()
	h.getSessionCommand(rec, req)
	var got dbpkg.ClaudeCommand
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.RunnerID != "runner-1" || got.PID != 7 || got.Args[1] != "--resume=x" {
		t.Errorf("session command = %v (%v)", rec.Body, err)
	}

	req.SetPathValue("id", "none")
	rec = httptest.NewRecorder()
	h.getSessionCommand(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: %d", rec.Code)
	}
}
