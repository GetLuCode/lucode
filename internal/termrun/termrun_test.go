package termrun

import (
	"bytes"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu       sync.Mutex
	out      bytes.Buffer
	nextOff  uint64
	gap      bool
	statuses []Status
}

func (r *recorder) Output(_ string, _, off uint64, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if off != r.nextOff {
		r.gap = true
	}
	r.nextOff = off + uint64(len(data))
	r.out.Write(data)
}
func (r *recorder) Status(s Status) {
	r.mu.Lock()
	r.statuses = append(r.statuses, s)
	r.mu.Unlock()
}
func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}
func (r *recorder) last() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.statuses) == 0 {
		return Status{}
	}
	return r.statuses[len(r.statuses)-1]
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunsACommandAndReportsItsExit(t *testing.T) {
	rec := &recorder{}
	m := NewManager(rec)
	if _, err := m.Start(Spec{ID: "a", Command: "echo hello-from-pty && exit 3", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit", func() bool { return rec.last().State == StateExited })
	if got := rec.last(); got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("exit code = %+v, want 3", got.ExitCode)
	}
	if !strings.Contains(rec.text(), "hello-from-pty") {
		t.Fatalf("output %q lacks the echo", rec.text())
	}
	if rec.gap {
		t.Fatal("output offsets were not contiguous")
	}
	data, start, end, st, ok := m.Replay("a")
	if !ok || st.State != StateExited || start != 0 || end != uint64(len(data)) || !strings.Contains(string(data), "hello-from-pty") {
		t.Fatalf("replay = %q %d-%d %+v %v", data, start, end, st, ok)
	}
}

func TestStopEndsAProcessThatWouldRunForever(t *testing.T) {
	rec := &recorder{}
	m := NewManager(rec)
	cmd := "sleep 60"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 60 127.0.0.1"
	}
	if _, err := m.Start(Spec{ID: "b", Command: cmd, Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(Spec{ID: "b", Command: cmd, Cwd: t.TempDir()}); err == nil {
		t.Fatal("a second start under the same id must be refused")
	}
	if err := m.Stop("b"); err != nil {
		t.Fatal(err)
	}
	if st := rec.last(); st.State != StateExited {
		t.Fatalf("state after stop = %q", st.State)
	}
	// once ended, the id can run again
	if _, err := m.Start(Spec{ID: "b", Command: "echo again", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitFor(t, "second run end", func() bool { return rec.last().State == StateExited && strings.Contains(rec.text(), "again") })
}

func TestInputReachesTheProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the interactive shell on unix; ConPTY echo is timing dependent")
	}
	rec := &recorder{}
	m := NewManager(rec)
	if _, err := m.Start(Spec{ID: "c", Command: "read x; echo got-$x", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	m.Write("c", []byte("42\n"))
	waitFor(t, "echo", func() bool { return strings.Contains(rec.text(), "got-42") })
}

func TestReplayKeepsOnlyTheTail(t *testing.T) {
	p := &proc{}
	m := &Manager{procs: map[string]*proc{"x": p}, sink: &recorder{}}
	p.status.ID = "x"
	chunk := bytes.Repeat([]byte("a"), 100*1024)
	for i := 0; i < 5; i++ {
		p.mu.Lock()
		p.buf = append(p.buf, chunk...)
		if over := len(p.buf) - ReplayBytes; over > 0 {
			p.buf = append([]byte(nil), p.buf[over:]...)
			p.base += uint64(over)
		}
		p.mu.Unlock()
	}
	data, start, end, _, _ := m.Replay("x")
	if len(data) != ReplayBytes || end-start != uint64(len(data)) || end != 500*1024 {
		t.Fatalf("len=%d start=%d end=%d", len(data), start, end)
	}
}

func TestMissingDirectoryIsRefused(t *testing.T) {
	m := NewManager(&recorder{})
	if _, err := m.Start(Spec{ID: "d", Command: "echo x", Cwd: t.TempDir() + "/nope"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestShutdownEndsEveryRunningProcess(t *testing.T) {
	rec := &recorder{}
	m := NewManager(rec)
	cmd := "sleep 60"
	if runtime.GOOS == "windows" {
		cmd = "ping -n 60 127.0.0.1"
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := m.Start(Spec{ID: id, Command: cmd, Cwd: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	m.Shutdown()
	for _, st := range m.List() {
		if st.State != StateExited {
			t.Fatalf("%s still %s after Shutdown", st.ID, st.State)
		}
	}
}
