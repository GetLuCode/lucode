package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
)

// gitFlowHub is a hub and a runner on a throwaway NATS. The repository only counts as being on
// the runner: the hub asks it for everything over RPC, as it does between two machines.
func gitFlowHub(t *testing.T, runnerHandler func(*Handler) http.Handler) (*Handler, string, dbpkg.KanbanCard, string, string, map[string]dbpkg.KanbanList) {
	t.Helper()
	url := jetStreamURL(t)

	runnerDB, err := dbpkg.Init(filepath.Join(t.TempDir(), "runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runnerDB.Close() })
	runnerBus := busOn(t, url)
	if err := runnerBus.StartDiscovery(natspkg.InstanceInfo{ID: "git-runner", Type: "lucode"}); err != nil {
		t.Fatal(err)
	}
	runner := New(runnerDB, runnerBus, sessionpkg.NewManager(runnerDB, runnerBus, url, ""), "")
	if err := runnerBus.StartRPCServer(runnerHandler(runner)); err != nil {
		t.Fatal(err)
	}

	hubDB, err := dbpkg.Init(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hubDB.Close() })
	hubBus := busOn(t, url)
	if err := hubBus.StartDiscovery(natspkg.InstanceInfo{ID: "git-hub", Type: "lucode", Extra: map[string]any{"hub_only": true}}); err != nil {
		t.Fatal(err)
	}
	h := New(hubDB, hubBus, sessionpkg.NewManager(hubDB, hubBus, url, ""), "")
	h.SetHubOnly(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.flows = flows.NewEngine(hubDB, flowHost{h})
	h.flows.Start(ctx)

	proj := dbpkg.Project{ID: "p1", Name: "p1", DefaultModel: "haiku", RunnerID: "git-runner"}
	if err := dbpkg.InsertProject(hubDB, proj); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureDefaultKanbanLists(hubDB, proj.ID); err != nil {
		t.Fatal(err)
	}
	lists := map[string]dbpkg.KanbanList{}
	all, _ := dbpkg.ListKanbanLists(hubDB, proj.ID)
	for _, l := range all {
		lists[l.Kind] = l
	}
	repo, wt := reviewRepo(t)
	commitOnBranch(t, wt)
	card := dbpkg.KanbanCard{ID: "c1", ProjectID: proj.ID, ListID: lists["todo"].ID, Title: "Add b", CardType: "task", Tags: []string{}, DependsOn: []string{}}
	if err := dbpkg.InsertKanbanCard(hubDB, card); err != nil {
		t.Fatal(err)
	}
	card, _ = dbpkg.GetKanbanCard(hubDB, card.ID)
	if err := dbpkg.SaveCardReview(hubDB, dbpkg.CardReview{CardID: card.ID, Branch: "task/x", WorktreePath: wt, RepoPath: repo, RunnerID: "git-runner"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		found := false
		for _, p := range hubBus.Peers() {
			found = found || p.ID == "git-runner"
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return h, proj.ID, card, repo, wt, lists
}

func waitRun(t *testing.T, h *Handler, f dbpkg.Flow) dbpkg.FlowRun {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var run dbpkg.FlowRun
	for time.Now().Before(deadline) {
		h.flows.Notify()
		if runs, _ := dbpkg.ListFlowRuns(h.db, f.ID, 1); len(runs) == 1 {
			run = runs[0]
			if run.Status != dbpkg.RunRunning {
				return run
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	steps, _ := dbpkg.ListFlowSteps(h.db, run.ID)
	t.Fatalf("the run never ended: %+v, steps %+v", run, steps)
	return run
}

func gitFlow(t *testing.T, h *Handler, pid string, config map[string]any) dbpkg.Flow {
	t.Helper()
	merge := map[string]any{"strategy": "merge", "unattended": true}
	for k, v := range config {
		merge[k] = v
	}
	return publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeCardMoved, Config: map[string]any{"to_kind": "review"}},
			{ID: "g", Type: flows.TypeGitStatus},
			{ID: "m", Type: flows.TypeMerge, Config: merge},
			{ID: "c", Type: flows.TypeComment, Config: map[string]any{"text": "status {{nodes.g.state}} on {{nodes.g.branch}}, ahead {{nodes.g.ahead}}"}},
		},
		Edges: []flows.Edge{
			{From: "t", Port: "next", To: "g"},
			{From: "g", Port: "clean", To: "m"},
			{From: "m", Port: "merged", To: "c"},
		},
	})
}

func TestGitNodesOnARunnerEndToEnd(t *testing.T) {
	h, pid, card, repo, wt, lists := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	f := gitFlow(t, h, pid, map[string]any{"delete_worktree": false, "delete_branch": false, "message": "feat: {{card.title}}"})
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	if run := waitRun(t, h, f); run.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %q", run.Status, run.Error)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("the runner did not merge")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the worktree was to be kept")
	}
	if got := gitOut(t, repo, "branch", "--list", "task/x"); got == "" {
		t.Fatal("the branch was to be kept")
	}
	if got := gitOut(t, repo, "log", "-1", "--format=%s"); got != "feat: Add b" {
		t.Fatalf("message: %q", got)
	}
	got, _ := dbpkg.GetKanbanCard(h.db, card.ID)
	if got.ListID != lists["done"].ID {
		t.Fatalf("card is in %s", got.ListID)
	}
	comments, _ := dbpkg.ListCardComments(h.db, card.ID)
	raw, _ := json.Marshal(comments)
	if !strings.Contains(string(raw), "status clean on task/x, ahead 1") || !strings.Contains(string(raw), "Merged task/x into main") {
		t.Fatalf("comments: %s", raw)
	}
}

// A runner from before the options must not merge and ignore them: it would delete the worktree
// someone asked to keep. It only understands the plain merge.
func oldRunner(r *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/review/status", r.reviewStatusRPC)
	mux.HandleFunc("POST /api/review/merge", r.reviewMergeRPC)
	return mux
}

func TestAnOldRunnerRefusesMergeOptions(t *testing.T) {
	h, pid, card, repo, wt, _ := gitFlowHub(t, oldRunner)
	f := gitFlow(t, h, pid, map[string]any{"delete_worktree": false, "delete_branch": false})
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	run := waitRun(t, h, f)
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "too old") {
		t.Fatalf("run: %s %q", run.Status, run.Error)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err == nil {
		t.Fatal("it merged anyway")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the worktree is gone")
	}
}

func TestAnOldRunnerStillMergesWithTheDefaults(t *testing.T) {
	h, pid, card, repo, _, _ := gitFlowHub(t, oldRunner)
	f := gitFlow(t, h, pid, nil)
	if _, err := h.moveCardToKind(card, dbpkg.KanbanReview); err != nil {
		t.Fatal(err)
	}
	if run := waitRun(t, h, f); run.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %q", run.Status, run.Error)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("not merged")
	}
}
