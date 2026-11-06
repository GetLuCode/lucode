package session

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/fakeclaude/fakeclaudetest"
	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/google/uuid"
)

func hubWithSession(t *testing.T, url, sessID string) (*Manager, *sql.DB) {
	t.Helper()
	m, db := testManager(t, testBus(t, url), url)
	if err := dbpkg.InsertProject(db, dbpkg.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.InsertSession(db, dbpkg.Session{ID: sessID, ProjectID: "p1", Name: "s", Model: "haiku", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	return m, db
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// A runner starts a persistent process for a session of the hub: the hub learns it,
// with the pid, without anybody asking; when the process exits on the runner (here: it
// is closed there, as the idle timeout or a crash would) the hub learns that too.
func TestHubFollowsTheProcessOfARunner(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	url := startJetStreamServer(t)

	runnerBus := testBus(t, url)
	if err := runnerBus.StartDiscovery(natspkg.InstanceInfo{ID: "proc-runner", Type: "lucode-test"}); err != nil {
		t.Fatal(err)
	}
	runnerM, _ := testManager(t, runnerBus, url)
	if err := runnerBus.StartRunnerServer(runnerM.ServeRemoteTurn, runnerM.CancelRemoteTurn, runnerM.CloseRemoteLive); err != nil {
		t.Fatal(err)
	}

	sess := uuid.New().String()
	hubM, _ := hubWithSession(t, url, sess)
	if err := hubM.WatchRunnerProcesses(); err != nil {
		t.Fatal(err)
	}
	if hubM.ProcessInfoOf(sess).Alive {
		t.Fatal("the hub thinks there is a process before any turn")
	}

	rt := RemoteTurn{Persistent: true, LucodeTools: []string{}}
	rt.Req.SessionID = sess
	rt.Req.Message = "Remember the secret word PINEAPPLE. Reply with just: ok"
	rt.Req.Model = "haiku"
	rt.Req.Provider = "claude"
	rt.Req.WorkDirs = []string{t.TempDir()}
	rt.Req.Tools = []string{"Read"}
	payload, _ := json.Marshal(rt)
	if err := hubM.bus.RunnerStartTurn("proc-runner", sess, uuid.New().String(), payload); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the hub to learn the process", func() bool { return hubM.ProcessInfoOf(sess).Alive })
	info := hubM.ProcessInfoOf(sess)
	if info.Runner != "proc-runner" || info.PID == 0 {
		t.Errorf("the hub knows %+v, want the runner and a pid", info)
	}
	if procs := runnerM.RunnerProcesses(); len(procs) != 1 || procs[0].PID != info.PID {
		t.Errorf("the runner lists %+v, the hub saw pid %d", procs, info.PID)
	}

	runnerM.CloseRemoteLive(sess) // nobody tells the hub: the runner's notice must
	waitFor(t, "the hub to learn the process exited", func() bool { return !hubM.ProcessInfoOf(sess).Alive })
}

// A hub that starts after the process was born (it restarted, it never heard the
// notice) learns it by asking the runner; and it forgets a process it believed alive
// that the runner no longer has.
func TestRestartedHubLearnsTheProcessesByAsking(t *testing.T) {
	fakeclaudetest.Use(t, "two_turns.jsonl")
	url := startJetStreamServer(t)

	runnerBus := testBus(t, url)
	if err := runnerBus.StartDiscovery(natspkg.InstanceInfo{ID: "ask-runner", Type: "lucode-test"}); err != nil {
		t.Fatal(err)
	}
	runnerM, _ := testManager(t, runnerBus, url)
	if err := runnerBus.StartRunnerServer(runnerM.ServeRemoteTurn, runnerM.CancelRemoteTurn, runnerM.CloseRemoteLive); err != nil {
		t.Fatal(err)
	}
	if err := runnerBus.ServeRunnerProcesses(runnerM.RunnerProcesses); err != nil {
		t.Fatal(err)
	}

	sess := uuid.New().String()
	// the first hub starts the process and dies
	hub1, _ := hubWithSession(t, url, sess)
	rt := RemoteTurn{Persistent: true, LucodeTools: []string{}}
	rt.Req.SessionID = sess
	rt.Req.Message = "Remember the secret word PINEAPPLE. Reply with just: ok"
	rt.Req.Model = "haiku"
	rt.Req.Provider = "claude"
	rt.Req.WorkDirs = []string{t.TempDir()}
	rt.Req.Tools = []string{"Read"}
	payload, _ := json.Marshal(rt)
	if err := hub1.bus.RunnerStartTurn("ask-runner", sess, uuid.New().String(), payload); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runner's process", func() bool { return len(runnerM.RunnerProcesses()) == 1 })
	want := runnerM.RunnerProcesses()[0].PID

	// the second hub knows nothing, and wrongly believes another session is alive there
	ghost := uuid.New().String()
	hub2, db2 := hubWithSession(t, url, sess)
	if err := dbpkg.InsertSession(db2, dbpkg.Session{ID: ghost, ProjectID: "p1", Name: "ghost", Model: "haiku", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	hub2.remoteRunnerOf[ghost] = "ask-runner"
	if hub2.ProcessInfoOf(sess).Alive {
		t.Fatal("a fresh hub should not know the process yet")
	}

	hub2.SyncRunnerProcesses("ask-runner")
	info := hub2.ProcessInfoOf(sess)
	if !info.Alive || info.Runner != "ask-runner" || info.PID != want {
		t.Errorf("after asking, the hub knows %+v, want alive on ask-runner with pid %d", info, want)
	}
	if hub2.ProcessInfoOf(ghost).Alive {
		t.Error("the hub still believes in a process the runner does not have")
	}
	runnerM.CloseRemoteLive(sess)
}

// A late notice cannot undo a newer one, and the exit of another process than the one
// the hub knows is not the exit of this one.
func TestStaleRunnerProcessNoticesAreIgnored(t *testing.T) {
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := dbpkg.InsertProject(db, dbpkg.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	sess := uuid.New().String()
	if err := dbpkg.InsertSession(db, dbpkg.Session{ID: sess, ProjectID: "p1", Name: "s", Model: "haiku", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	m := &Manager{db: db, remoteRunnerOf: map[string]string{}, remotePID: map[string]int{}, remoteAt: map[string]int64{}}

	m.HandleRunnerProcess("r", natspkg.RunnerProcess{SessionID: sess, Alive: true, PID: 7, At: 200})
	if got := m.ProcessInfoOf(sess); !got.Alive || got.PID != 7 || got.Runner != "r" {
		t.Fatalf("after the start notice: %+v", got)
	}
	m.HandleRunnerProcess("r", natspkg.RunnerProcess{SessionID: sess, Alive: false, PID: 7, At: 100})
	if !m.ProcessInfoOf(sess).Alive {
		t.Error("an exit older than the start undid it")
	}
	m.HandleRunnerProcess("r", natspkg.RunnerProcess{SessionID: sess, Alive: false, PID: 999, At: 300})
	if !m.ProcessInfoOf(sess).Alive {
		t.Error("the exit of another process (pid 999) ended this one (pid 7)")
	}
	m.HandleRunnerProcess("other", natspkg.RunnerProcess{SessionID: sess, Alive: false, PID: 7, At: 350})
	if !m.ProcessInfoOf(sess).Alive {
		t.Error("an exit notice from another runner ended it")
	}
	m.HandleRunnerProcess("r", natspkg.RunnerProcess{SessionID: sess, Alive: false, PID: 7, At: 400})
	if m.ProcessInfoOf(sess).Alive {
		t.Error("the real exit was ignored")
	}
	// a session the hub does not own is not remembered
	m.HandleRunnerProcess("r", natspkg.RunnerProcess{SessionID: "someone-elses", Alive: true, PID: 1, At: 500})
	if m.ProcessInfoOf("someone-elses").Alive {
		t.Error("the hub took over a process of a session it does not own")
	}
}
