package natsbus

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// These tests need a nats-server with JetStream (see startJetStreamServer) and are
// skipped without one.

type collected struct {
	mu   sync.Mutex
	seqs []int
	done chan struct{}
	once sync.Once
}

func newCollected() *collected { return &collected{done: make(chan struct{})} }

func (c *collected) handle(turnID string) func(RunnerEvent) error {
	return func(ev RunnerEvent) error {
		if ev.TurnID != turnID {
			return nil
		}
		c.mu.Lock()
		c.seqs = append(c.seqs, ev.Seq)
		c.mu.Unlock()
		if ev.Done {
			c.once.Do(func() { close(c.done) })
		}
		return nil
	}
}

func (c *collected) wait(t *testing.T) []int {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		c.mu.Lock()
		defer c.mu.Unlock()
		t.Fatalf("no turn_done; got seqs %v", c.seqs)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.seqs...)
}

func inOrder(seqs []int, n int) error {
	if len(seqs) != n {
		return fmt.Errorf("got %d events %v, want %d", len(seqs), seqs, n)
	}
	for i, s := range seqs {
		if s != i+1 {
			return fmt.Errorf("event %d has seq %d: %v", i, s, seqs)
		}
	}
	return nil
}

// The runner publishes a whole turn while nobody listens; a hub that starts to
// listen afterwards (no consumer existed yet) still receives all of it, in order,
// ending with turn_done.
func TestRunnerEventsReachAHubThatComesLater(t *testing.T) {
	url := startJetStreamServer(t)
	runner := testBusAt(t, url, "late-runner")
	sink := runner.NewTurnSink("late-runner", "sess-late", "turn-late")
	for i := 0; i < 5; i++ {
		if err := sink.Event([]byte(fmt.Sprintf(`{"type":"message_ready","n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Done(); err != nil {
		t.Fatal(err)
	}

	hub := testBusAt(t, url, "late-hub")
	c := newCollected()
	f, err := hub.FollowRunnerTurn("late-runner", "sess-late", true, nil, c.handle("turn-late"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := inOrder(c.wait(t), 6); err != nil {
		t.Error(err)
	}
}

// The hub stops reading in the middle of a turn and comes back: it gets the rest
// from where its consumer was, nothing twice (every event was acked) and nothing
// missing.
func TestRunnerEventsResumeWhereTheHubLeftOff(t *testing.T) {
	url := startJetStreamServer(t)
	runner := testBusAt(t, url, "resume-runner")
	hub1 := testBusAt(t, url, "resume-hub")
	sink := runner.NewTurnSink("resume-runner", "sess-resume", "turn-resume")

	first := newCollected()
	f1, err := hub1.FollowRunnerTurn("resume-runner", "sess-resume", false, nil, first.handle("turn-resume"))
	if err != nil {
		t.Fatal(err)
	}
	sink.Event([]byte(`{"type":"a"}`))
	sink.Event([]byte(`{"type":"b"}`))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		first.mu.Lock()
		n := len(first.seqs)
		first.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f1.Close() // the hub goes down

	sink.Event([]byte(`{"type":"c"}`))
	sink.Event([]byte(`{"type":"d"}`))
	sink.Done()

	// A new hub process: same consumer, no history requested.
	hub2 := testBusAt(t, url, "resume-hub")
	second := newCollected()
	f2, err := hub2.FollowRunnerTurn("resume-runner", "sess-resume", false, nil, second.handle("turn-resume"))
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	got := second.wait(t)
	if len(got) != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Errorf("after reconnecting got seqs %v, want [3 4 5]", got)
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	if len(first.seqs) != 2 {
		t.Errorf("first hub saw %v", first.seqs)
	}
}

// An event whose handler fails is delivered again (at least once) and the ones
// after it wait, so the order holds. The same publish retried with the same message
// id is stored once.
func TestRunnerEventsAreRedeliveredUntilHandled(t *testing.T) {
	url := startJetStreamServer(t)
	runner := testBusAt(t, url, "redo-runner")
	hub := testBusAt(t, url, "redo-hub")

	var mu sync.Mutex
	var seen []int
	failed := false
	done := make(chan struct{})
	f, err := hub.FollowRunnerTurn("redo-runner", "sess-redo", false, nil, func(ev RunnerEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if ev.Seq == 2 && !failed {
			failed = true
			seen = append(seen, -ev.Seq) // a failed attempt
			return fmt.Errorf("db busy")
		}
		seen = append(seen, ev.Seq)
		if ev.Done {
			close(done)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sink := runner.NewTurnSink("redo-runner", "sess-redo", "turn-redo")
	sink.Event([]byte(`{"type":"a"}`))
	sink.Event([]byte(`{"type":"b"}`))
	sink.Done()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("no turn_done; seen %v", seen)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []int{1, -2, 2, 3}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("seen %v, want %v", seen, want)
	}

	// A retried publish (same turn id and seq) is dropped by JetStream.
	js, _ := runner.nc.JetStream()
	info, _ := js.StreamInfo(RunnerStreamName)
	before := info.State.Msgs
	dup := runner.NewTurnSink("redo-runner", "sess-redo", "turn-redo") // numbers from 1 again
	dup.Event([]byte(`{"type":"a"}`))
	info, _ = js.StreamInfo(RunnerStreamName)
	if info.State.Msgs != before {
		t.Errorf("a republished seq was stored again (%d -> %d messages)", before, info.State.Msgs)
	}
}
