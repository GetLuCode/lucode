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
	"claude-orchestrator/internal/flows"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitOnBranch(t *testing.T, wt string) {
	t.Helper()
	write(t, filepath.Join(wt, "b.txt"), "new\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "add b")
}

func TestMergeOptionsDoWhatTheySay(t *testing.T) {
	cases := []struct {
		name         string
		opts         mergeOptions
		worktreeGone bool
		branchGone   bool
		commits      string // the subject of the newest commit on main
	}{
		{"defaults", mergeOptions{}, true, true, "Merge task: Add b"},
		{"keep the worktree and the branch", mergeOptions{KeepWorktree: true, KeepBranch: true}, false, false, "Merge task: Add b"},
		{"keep only the branch", mergeOptions{KeepBranch: true}, true, false, "Merge task: Add b"},
		{"own message", mergeOptions{Message: "feat: b"}, true, true, "feat: b"},
		{"squash", mergeOptions{Strategy: "squash", Message: "feat: all of b"}, true, true, "feat: all of b"},
		{"fast-forward", mergeOptions{Strategy: "ff-only"}, true, true, "add b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, wt := reviewRepo(t)
			commitOnBranch(t, wt)
			rgit(t, repo, "config", "user.email", "t@t")
			rgit(t, repo, "config", "user.name", "t")
			res, code, err := mergeWithOptions(dbpkg.CardReview{Branch: "task/x", WorktreePath: wt, RepoPath: repo}, "Add b", c.opts)
			if err != nil {
				t.Fatalf("%d %v", code, err)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings: %v", res.Warnings)
			}
			if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
				t.Error("the work did not reach main")
			}
			if _, err := os.Stat(wt); os.IsNotExist(err) != c.worktreeGone {
				t.Errorf("worktree gone = %v, want %v", os.IsNotExist(err), c.worktreeGone)
			}
			if res.WorktreeRemoved != c.worktreeGone {
				t.Errorf("WorktreeRemoved = %v", res.WorktreeRemoved)
			}
			if got := gitOut(t, repo, "branch", "--list", "task/x") == ""; got != c.branchGone {
				t.Errorf("branch gone = %v, want %v", got, c.branchGone)
			}
			if got := gitOut(t, repo, "log", "-1", "--format=%s"); got != c.commits {
				t.Errorf("newest commit %q, want %q", got, c.commits)
			}
		})
	}
}

func TestFastForwardRefusesIfTheBaseMoved(t *testing.T) {
	repo, wt := reviewRepo(t)
	commitOnBranch(t, wt)
	write(t, filepath.Join(repo, "c.txt"), "other\n")
	rgit(t, repo, "add", ".")
	rgit(t, repo, "commit", "-q", "-m", "moved on")
	before := gitOut(t, repo, "rev-parse", "HEAD")
	_, code, err := mergeWithOptions(dbpkg.CardReview{Branch: "task/x", WorktreePath: wt, RepoPath: repo}, "x", mergeOptions{Strategy: "ff-only"})
	if err == nil || code != http.StatusConflict || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("%d %v", code, err)
	}
	if gitOut(t, repo, "rev-parse", "HEAD") != before {
		t.Fatal("a refused merge changed main")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("a refused merge removed the worktree")
	}
}

func TestRunnerServesMergeOptions(t *testing.T) {
	repo, wt := reviewRepo(t)
	commitOnBranch(t, wt)
	h, _, _ := newTaskTestHandler(t)
	srv := NewRunnerHandler(h.db, h.bus, h.manager, t.TempDir())
	body, _ := json.Marshal(mergeOptionsRequest{reviewRef{RepoPath: repo, Branch: "task/x", WorktreePath: wt}, "Add b", mergeOptions{KeepWorktree: true, KeepBranch: true}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/api/review/merge-options", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the runner removed a worktree it was told to keep")
	}
}

// taskDone finishes a card with a branch through the seeded blueprint.
func finishTask(t *testing.T, h *Handler, sessID string) string {
	t.Helper()
	msg, err := h.HandleTaskTool(sessID, "task_done", []byte(`{"summary":"built it"}`))
	if err != nil {
		t.Fatal(err)
	}
	settle(h)
	return msg
}

func TestTaskFinishedBlueprint(t *testing.T) {
	t.Run("unmerged work goes to Review with its branch", func(t *testing.T) {
		h, pid, lists, _ := flowHarness(t)
		if err := flows.SeedBlueprints(h.db, pid); err != nil {
			t.Fatal(err)
		}
		lead := addMember(t, h, pid, lists, "Lead", "", "inprogress")
		dev := addMember(t, h, pid, lists, "Dev", lead.agent.ID, "inprogress")
		c := &capture{}
		h.startTurnFn = c.fn
		repo, wt := reviewRepo(t)
		commitOnBranch(t, wt)
		if err := dbpkg.SaveCardReview(h.db, dbpkg.CardReview{CardID: dev.card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}); err != nil {
			t.Fatal(err)
		}
		finishTask(t, h, dev.sess.ID)
		card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
		if card.ListID != lists["review"].ID {
			t.Fatalf("card is in %s", card.ListID)
		}
		if _, err := dbpkg.GetCardReview(h.db, card.ID); err != nil {
			t.Fatal("the branch was forgotten")
		}
		if len(c.opts) != 1 || !strings.Contains(c.opts[0].Message, "ready for review") || !strings.Contains(c.opts[0].Message, "built it") {
			t.Fatalf("supervisor: %+v", c.opts)
		}
	})

	t.Run("no branch goes to Done and releases what depends on it", func(t *testing.T) {
		h, pid, lists, _ := flowHarness(t)
		if err := flows.SeedBlueprints(h.db, pid); err != nil {
			t.Fatal(err)
		}
		lead := addMember(t, h, pid, lists, "Lead", "", "inprogress")
		dev := addMember(t, h, pid, lists, "Dev", lead.agent.ID, "inprogress")
		c := &capture{}
		h.startTurnFn = c.fn
		waiting := newCard(t, h, pid, lists["blocked"], "waiting", dev.card.ID)
		finishTask(t, h, dev.sess.ID)
		card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
		if card.ListID != lists["done"].ID {
			t.Fatalf("card is in %s", card.ListID)
		}
		waiting, _ = dbpkg.GetKanbanCard(h.db, waiting.ID)
		if waiting.ListID != lists["inprogress"].ID {
			t.Fatalf("the dependent task is in %s", waiting.ListID)
		}
		told := false
		for _, o := range c.opts {
			told = told || strings.Contains(o.Message, "is done: built it")
		}
		if !told {
			t.Fatalf("supervisor: %+v", c.opts)
		}
	})

	t.Run("a repository it cannot read waits in Review with the reason", func(t *testing.T) {
		h, pid, lists, _ := flowHarness(t)
		if err := flows.SeedBlueprints(h.db, pid); err != nil {
			t.Fatal(err)
		}
		dev := addMember(t, h, pid, lists, "Dev", "", "inprogress")
		c := &capture{}
		h.startTurnFn = c.fn
		if err := dbpkg.SaveCardReview(h.db, dbpkg.CardReview{CardID: dev.card.ID, Branch: "task/x", WorktreePath: "/nope", RepoPath: filepath.Join(t.TempDir(), "gone")}); err != nil {
			t.Fatal(err)
		}
		finishTask(t, h, dev.sess.ID)
		card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
		if card.ListID != lists["review"].ID {
			t.Fatalf("card is in %s", card.ListID)
		}
		comments, _ := dbpkg.ListCardComments(h.db, card.ID)
		found := false
		for _, cm := range comments {
			found = found || strings.Contains(cm.Body, "not reachable")
		}
		if !found {
			t.Fatalf("comments: %+v", comments)
		}
	})
}

// With the blueprint deleted, disabled or paused, nothing is lost: a person gets the task.
func TestTaskDoneWithoutAFlowWaitsInReview(t *testing.T) {
	for name, off := range map[string]func(*Handler, string, dbpkg.Flow){
		"deleted":  func(h *Handler, _ string, f dbpkg.Flow) { _ = dbpkg.DeleteFlow(h.db, f.ID) },
		"disabled": func(h *Handler, _ string, f dbpkg.Flow) { _ = dbpkg.SetFlowEnabled(h.db, f.ID, false) },
		"paused":   func(h *Handler, pid string, _ dbpkg.Flow) { _ = dbpkg.SetFlowsPaused(h.db, pid, true) },
	} {
		t.Run(name, func(t *testing.T) {
			h, pid, lists, _ := flowHarness(t)
			if err := flows.SeedBlueprints(h.db, pid); err != nil {
				t.Fatal(err)
			}
			list, _ := dbpkg.ListFlows(h.db, pid)
			off(h, pid, list[0])
			dev := addMember(t, h, pid, lists, "Dev", "", "inprogress")
			finishTask(t, h, dev.sess.ID)
			card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
			if card.ListID != lists["review"].ID {
				t.Fatalf("card is in %s", card.ListID)
			}
		})
	}
}

// The user can change it: this flow sends every finished task straight to Done.
func TestAUserCanChangeWhereAFinishedTaskGoes(t *testing.T) {
	h, pid, lists, _ := flowHarness(t)
	publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeTaskDone},
			{ID: "m", Type: flows.TypeMoveCard, Config: map[string]any{"to_kind": "backlog"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "m"}},
	})
	dev := addMember(t, h, pid, lists, "Dev", "", "inprogress")
	finishTask(t, h, dev.sess.ID)
	card, _ := dbpkg.GetKanbanCard(h.db, dev.card.ID)
	if card.ListID != lists["backlog"].ID {
		t.Fatalf("card is in %s", card.ListID)
	}
}

func TestStartingAFlowFromABlueprint(t *testing.T) {
	h, pid, _, _ := flowHarness(t)
	mux := http.NewServeMux()
	h.registerFlows(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/"+pid+"/flows", strings.NewReader(`{"name":"Gate","blueprint":"review_gate"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var f dbpkg.Flow
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if f.PublishedVersion != 0 || f.Enabled {
		t.Fatalf("a copy must start as a draft: %+v", f)
	}
	g, err := flows.ParseGraph(f.Draft)
	if err != nil || len(g.Nodes) == 0 {
		t.Fatalf("%v %+v", err, g)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/"+pid+"/flows", strings.NewReader(`{"name":"x","blueprint":"nope"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown blueprint: %d", rec.Code)
	}
}

// The review flow in full, with real git, the real SDK and the fake API.
func TestReviewGateBlueprintMergesAfterApproval(t *testing.T) {
	h, pid, lists, turns := flowHarness(t)
	useFakeAnthropic(t) // answers low, the first option
	b, _ := flows.BlueprintOf("review_gate")
	publishFlow(t, h, pid, b.Graph)
	repo, wt := reviewRepo(t)
	commitOnBranch(t, wt)
	card := newCard(t, h, pid, lists["todo"], "Add b")
	if err := dbpkg.SaveCardReview(h.db, dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	settle(h)
	settle(h)
	pending := approvalsOf(t, h, pid)
	if len(pending) != 1 || !strings.Contains(pending[0].Question, "Low risk on Add b: fake answer") {
		t.Fatalf("approvals: %+v", pending)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err == nil {
		t.Fatal("it merged before anyone approved")
	}
	decide(h, pending[0].Seq, `{"decision":"approved"}`)
	settle(h)
	settle(h)
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("approved, but not merged")
	}
	card, _ = dbpkg.GetKanbanCard(h.db, card.ID)
	if card.ListID != lists["done"].ID {
		t.Fatalf("card is in %s", card.ListID)
	}
	if _, err := dbpkg.GetCardReview(h.db, card.ID); err == nil {
		t.Fatal("the review row was kept")
	}
	if len(*turns) != 0 {
		t.Fatal("no agent turn was needed")
	}
}
