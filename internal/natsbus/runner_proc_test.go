package natsbus

import (
	"sync"
	"testing"
	"time"
)

// Needs a nats-server (see startJetStreamServer); skipped without one.

// A hub hears the notices a runner publishes, tagged with the runner they come from.
func TestHubHearsRunnerProcessNotices(t *testing.T) {
	url := startJetStreamServer(t)
	runner := testBusAt(t, url, "proc-runner")
	hub := testBusAt(t, url, "proc-hub")

	var mu sync.Mutex
	var got []RunnerProcess
	var from []string
	if err := hub.WatchRunnerProcesses(func(runnerID string, p RunnerProcess) {
		mu.Lock()
		got = append(got, p)
		from = append(from, runnerID)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}

	runner.PublishRunnerProcess(RunnerProcess{SessionID: "s1", Alive: true, PID: 4242})
	runner.PublishRunnerProcess(RunnerProcess{SessionID: "s1", Alive: false, PID: 4242})

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d notices, want 2: %v", len(got), got)
	}
	if from[0] != "proc-runner" || got[0].SessionID != "s1" || !got[0].Alive || got[0].PID != 4242 {
		t.Errorf("first notice: runner %q %+v", from[0], got[0])
	}
	if got[1].Alive {
		t.Errorf("second notice should say the process exited: %+v", got[1])
	}
	if got[0].At == 0 || got[1].At < got[0].At {
		t.Errorf("notices are not ordered by At: %d then %d", got[0].At, got[1].At)
	}
}

// A hub asks a runner which processes it has alive and gets the list; a runner that
// does not answer is an error, not an empty list.
func TestHubAsksTheRunnerForItsProcesses(t *testing.T) {
	url := startJetStreamServer(t)
	runner := testBusAt(t, url, "ask-runner")
	hub := testBusAt(t, url, "ask-hub")

	if err := runner.ServeRunnerProcesses(func() []RunnerProcess {
		return []RunnerProcess{{SessionID: "a", Alive: true, PID: 11}, {SessionID: "b", Alive: true, PID: 22}}
	}); err != nil {
		t.Fatal(err)
	}
	procs, err := hub.RunnerProcesses("ask-runner")
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 2 || procs[0].SessionID != "a" || procs[1].PID != 22 {
		t.Errorf("unexpected list: %+v", procs)
	}

	if _, err := hub.RunnerProcesses("nobody-home"); err == nil {
		t.Error("a runner that does not exist must be an error")
	}
}
