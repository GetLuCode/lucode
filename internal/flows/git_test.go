package flows

import (
	"errors"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func (h *harness) settleCalls() {
	h.e.Drain()
	h.e.WaitForEvaluations()
	h.e.Drain()
}

func TestGitStatusTakesTheOutputOfTheState(t *testing.T) {
	for _, state := range []string{PortClean, PortConflicts, PortDirty, PortNothing} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			h.host.git = GitState{State: state, Branch: "task/x", Base: "main", Ahead: 2, Files: []string{"M a.go"}}
			nodes := []Node{reviewTrigger(), {ID: "g", Type: TypeGitStatus}}
			edges := []Edge{{"t", PortEvent, "g"}}
			for _, p := range []string{PortClean, PortConflicts, PortDirty, PortNothing} {
				// the JSON answer is read with Get JSON prop, never as a variable
				nodes = append(nodes,
					Node{ID: "j" + p, Type: TypeGetJSONProp, Config: cfg("template", p+" {json.branch} {json.ahead} {json.files.0}")},
					Node{ID: p, Type: TypeComment, Config: cfg("text", "{{input}}")})
				edges = append(edges, Edge{"g", p, "j" + p}, Edge{"j" + p, PortValue, p})
			}
			f := h.publish(Graph{Nodes: nodes, Edges: edges})
			h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
			h.settleCalls()
			if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
				t.Fatalf("run: %s %s", got.Status, got.Error)
			}
			if want := state + " task/x 2 M a.go"; len(h.host.comments) != 1 || h.host.comments[0] != want {
				t.Fatalf("comments %q, want %q", h.host.comments, want)
			}
		})
	}
}

func TestGitStatusFailureFailsTheRunWhenNothingHandlesIt(t *testing.T) {
	h := newHarness(t)
	h.host.gitErr = errors.New("runner unavailable")
	f := h.publish(Graph{Nodes: []Node{reviewTrigger(), {ID: "g", Type: TypeGitStatus}}, Edges: []Edge{{"t", PortEvent, "g"}}})
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunFailed || got.Error != "runner unavailable" {
		t.Fatalf("run: %s %q", got.Status, got.Error)
	}
}

func approved(merge Node) Graph {
	return Graph{
		Nodes: []Node{reviewTrigger(), {ID: "ask", Type: TypeApproval, Config: cfg("question", "?")}, merge},
		Edges: []Edge{{"t", PortEvent, "ask"}, {"ask", PortApproved, merge.ID}},
	}
}

func TestMergeRequiresAnApprovalOnEveryPath(t *testing.T) {
	merge := Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge")}
	problems := func(g Graph) string {
		var all []string
		for _, p := range Validate(g) {
			all = append(all, p.Message)
		}
		return strings.Join(all, " | ")
	}

	if got := problems(approved(merge)); got != "" {
		t.Fatalf("approved path: %s", got)
	}
	direct := Graph{Nodes: []Node{reviewTrigger(), merge}, Edges: []Edge{{"t", PortEvent, "m"}}}
	if !strings.Contains(problems(direct), "without a person") {
		t.Fatal("a Merge right after a trigger was accepted")
	}
	// the wrong output of the approval does not count
	rejected := Graph{
		Nodes: []Node{reviewTrigger(), {ID: "ask", Type: TypeApproval, Config: cfg("question", "?")}, merge},
		Edges: []Edge{{"t", PortEvent, "ask"}, {"ask", PortRejected, "m"}},
	}
	if !strings.Contains(problems(rejected), "without a person") {
		t.Fatal("a Merge after a rejection was accepted")
	}
	// one good path does not excuse another that goes around it
	around := approved(merge)
	around.Edges = append(around.Edges, Edge{"t", PortEvent, "m"})
	if !strings.Contains(problems(around), "without a person") {
		t.Fatal("a second path around the approval was accepted")
	}
	// someone said so, on that node
	unattended := Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge", "unattended", true)}
	if got := problems(Graph{Nodes: []Node{reviewTrigger(), unattended}, Edges: []Edge{{"t", PortEvent, "m"}}}); got != "" {
		t.Fatalf("unattended: %s", got)
	}
	keep := Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge", "delete_worktree", false)}
	if !strings.Contains(problems(approved(keep)), "git keeps a branch") {
		t.Fatal("deleting a branch a kept worktree uses was accepted")
	}
	keepBoth := Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge", "delete_worktree", false, "delete_branch", false)}
	if got := problems(approved(keepBoth)); got != "" {
		t.Fatalf("keeping both: %s", got)
	}
}

func TestMergeUsesTheNodesOptions(t *testing.T) {
	h := newHarness(t)
	f := h.publish(approved(Node{ID: "m", Type: TypeMerge, Config: cfg(
		"strategy", "squash", "message", "feat: {{card.title}}", "delete_worktree", false, "delete_branch", false, "move_to_done", false,
	)}))
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	if err := h.e.Decide(h.waitingApproval(f).Seq, PortApproved, ""); err != nil {
		t.Fatal(err)
	}
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	want := MergeOptions{Strategy: "squash", Message: "feat: Fix it"}
	if len(h.host.merged) != 1 || h.host.merged[0] != want {
		t.Fatalf("options: %+v", h.host.merged)
	}
}

func TestMergeDefaultsWhenTheOptionsAreAbsent(t *testing.T) {
	h := newHarness(t)
	f := h.publish(approved(Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge")}))
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	_ = h.e.Decide(h.waitingApproval(f).Seq, PortApproved, "")
	h.settleCalls()
	want := MergeOptions{Strategy: "merge", DeleteWorktree: true, DeleteBranch: true, MoveToDone: true}
	if len(h.host.merged) != 1 || h.host.merged[0] != want {
		t.Fatalf("options: %+v", h.host.merged)
	}
}

func TestMergeConflictsTakeTheirOutput(t *testing.T) {
	h := newHarness(t)
	h.host.mergePort = PortConflicts
	g := approved(Node{ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge")})
	g.Nodes = append(g.Nodes, Node{ID: "c", Type: TypeComment, Config: cfg("text", "conflict")})
	g.Edges = append(g.Edges, Edge{"m", PortConflicts, "c"})
	f := h.publish(g)
	h.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	_ = h.e.Decide(h.waitingApproval(f).Seq, PortApproved, "")
	h.settleCalls()
	if len(h.host.comments) != 1 || h.host.comments[0] != "conflict" {
		t.Fatalf("comments: %q", h.host.comments)
	}
}

// A merge the hub stopped in the middle of is never repeated blindly.
func TestMergeInterruptedByARestart(t *testing.T) {
	setup := func(t *testing.T, state string) (*harness, dbpkg.Flow) {
		h := newHarness(t)
		f := h.publish(Graph{
			Nodes: []Node{reviewTrigger(), {ID: "m", Type: TypeMerge, Config: cfg("strategy", "merge", "unattended", true)}},
			Edges: []Edge{{"t", PortEvent, "m"}},
		})
		run, err := dbpkg.CreateFlowRun(h.e.db, dbpkg.FlowRun{FlowID: f.ID, Version: 1, ProjectID: "p1", CardID: "c1", Status: dbpkg.RunRunning}, []string{"m"})
		if err != nil {
			t.Fatal(err)
		}
		steps, _ := dbpkg.ListFlowSteps(h.e.db, run.ID)
		if err := dbpkg.WaitFlowStep(h.e.db, steps[0].Seq, gitWaitKey(steps[0].Seq), "", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		h.host.git = GitState{State: state}
		return h, f
	}

	t.Run("it finished", func(t *testing.T) {
		h, f := setup(t, PortNothing)
		h.e.Recover()
		h.settleCalls()
		if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
			t.Fatalf("run: %s %s", got.Status, got.Error)
		}
		if len(h.host.merged) != 0 || h.host.mergeFinished != 1 {
			t.Fatalf("merged %d times, finished %d", len(h.host.merged), h.host.mergeFinished)
		}
	})
	t.Run("it did not", func(t *testing.T) {
		h, f := setup(t, PortClean)
		h.e.Recover()
		h.settleCalls()
		got := h.onlyRun(f)
		if got.Status != dbpkg.RunFailed || !strings.Contains(got.Error, "nothing was retried") {
			t.Fatalf("run: %s %s", got.Status, got.Error)
		}
		if len(h.host.merged) != 0 {
			t.Fatal("it merged again on its own")
		}
	})
}

func TestEveryBlueprintIsValid(t *testing.T) {
	for _, b := range Blueprints() {
		t.Run(b.Key, func(t *testing.T) {
			if p := Validate(b.Graph); len(p) != 0 {
				t.Fatalf("%+v", p)
			}
			for _, n := range b.Graph.Nodes {
				if !strings.HasPrefix(n.ID, "n") {
					t.Errorf("the editor keeps ids like n1: %s", n.ID)
				}
			}
		})
	}
}

func TestSeedingHappensOnceAndADeletedFlowStaysDeleted(t *testing.T) {
	h := newHarness(t)
	if err := SeedBlueprints(h.e.db, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := SeedBlueprints(h.e.db, "p1"); err != nil {
		t.Fatal(err)
	}
	list, _ := dbpkg.ListFlows(h.e.db, "p1")
	if len(list) != 1 || list[0].Name != "Task finished" || !list[0].Enabled || list[0].PublishedVersion != 1 {
		t.Fatalf("flows: %+v", list)
	}
	if !h.e.Handles("p1", TypeTaskDone) {
		t.Fatal("the seeded flow does not handle task_done")
	}
	if h.e.Handles("p2", TypeTaskDone) {
		t.Fatal("another project should not be affected")
	}
	_ = dbpkg.SetFlowsPaused(h.e.db, "p1", true)
	if h.e.Handles("p1", TypeTaskDone) {
		t.Fatal("a paused project handles nothing")
	}
	_ = dbpkg.SetFlowsPaused(h.e.db, "p1", false)
	_ = dbpkg.DeleteFlow(h.e.db, list[0].ID)
	if err := SeedBlueprints(h.e.db, "p1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := dbpkg.ListFlows(h.e.db, "p1"); len(list) != 0 {
		t.Fatalf("the deleted flow came back: %+v", list)
	}
}
