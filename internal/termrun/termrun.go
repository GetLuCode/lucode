// Package termrun runs commands attached to a pseudo-terminal, on the machine that calls it: it
// is the runner's half of the Commands tab. It knows nothing about NATS or the hub: output and
// state changes go to a Sink, and the caller decides where they travel.
package termrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// ReplayBytes is how much of a process's output is kept for someone who attaches later.
const ReplayBytes = 256 * 1024

const (
	StateRunning = "running"
	StateExited  = "exited"
	StateFailed  = "failed" // never started
)

// Status is what the hub shows about one process.
type Status struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	Run       uint64 `json:"run"` // identifies this start of the id
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// Spec is one process to start. Command is run by the system shell; empty opens an interactive shell.
type Spec struct {
	ID      string
	Command string
	Cwd     string // absolute, already checked by the caller
	Cols    int
	Rows    int
	Env     []string // added to the environment of the runner
	Shell   Shell    // zero = the system's default shell
}

// Sink receives what happens to the processes. Calls for one process come in order.
type Sink interface {
	// Output carries a chunk of the process's output; run tells one start of the id from the next and
	// off is the number of bytes of that run sent before it.
	Output(id string, run, off uint64, data []byte)
	Status(Status)
}

type proc struct {
	mu     sync.Mutex
	status Status
	buf    []byte
	base   uint64 // output offset of buf[0]
	pty    pty.Pty
	cmd    *pty.Cmd
	done   chan struct{} // closed when the process has ended and its state was published
}

// Manager owns the processes of one runner.
type Manager struct {
	sink  Sink
	mu    sync.Mutex
	procs map[string]*proc
}

func NewManager(sink Sink) *Manager { return &Manager{sink: sink, procs: map[string]*proc{}} }

// stamp is the current time at a fixed width, so that two of them compare as strings.
func stamp() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z") }

func clampSize(cols, rows int) (int, int) {
	if cols < 10 || cols > 1000 {
		cols = 120
	}
	if rows < 3 || rows > 500 {
		rows = 30
	}
	return cols, rows
}

// Start launches a process. A process that is still running under the same id is an error: stop it first.
func (m *Manager) Start(spec Spec) (Status, error) {
	if spec.ID == "" {
		return Status{}, errors.New("no id")
	}
	if fi, err := os.Stat(spec.Cwd); err != nil || !fi.IsDir() {
		return Status{}, fmt.Errorf("the working directory does not exist on this runner: %s", spec.Cwd)
	}
	m.mu.Lock()
	if old := m.procs[spec.ID]; old != nil {
		old.mu.Lock()
		running := old.status.State == StateRunning
		old.mu.Unlock()
		if running {
			m.mu.Unlock()
			return Status{}, errors.New("already running")
		}
	}
	m.mu.Unlock()

	cols, rows := clampSize(spec.Cols, spec.Rows)
	p := &proc{done: make(chan struct{}), status: Status{
		ID: spec.ID, Run: uint64(time.Now().UnixNano()), State: StateRunning, Command: spec.Command, Cwd: spec.Cwd, Cols: cols, Rows: rows,
		StartedAt: stamp(),
	}}

	fail := func(err error) (Status, error) {
		p.status.State, p.status.Error = StateFailed, err.Error()
		p.status.EndedAt = stamp()
		m.mu.Lock()
		m.procs[spec.ID] = p
		m.mu.Unlock()
		close(p.done)
		m.sink.Status(p.status)
		return p.status, err
	}

	pt, err := pty.New()
	if err != nil {
		return fail(fmt.Errorf("cannot open a terminal: %w", err))
	}
	if err := pt.Resize(cols, rows); err != nil {
		_ = pt.Close()
		return fail(fmt.Errorf("cannot size the terminal: %w", err))
	}
	cmd := shellCommand(pt, spec.Shell, spec.Command)
	cmd.Dir = spec.Cwd
	cmd.Env = append(os.Environ(), append(terminalEnv(), spec.Env...)...)
	if err := cmd.Start(); err != nil {
		_ = pt.Close()
		return fail(fmt.Errorf("cannot start: %w", err))
	}
	p.pty, p.cmd = pt, cmd
	p.status.PID = cmd.Process.Pid

	m.mu.Lock()
	m.procs[spec.ID] = p
	m.mu.Unlock()
	m.sink.Status(p.snapshotStatus())

	readerDone := make(chan struct{})
	go m.pump(p, readerDone)
	go m.finish(p, readerDone)
	return p.snapshotStatus(), nil
}

func (p *proc) snapshotStatus() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// pump moves the terminal's output into the replay buffer and to the sink.
func (m *Manager) pump(p *proc, done chan struct{}) {
	defer close(done)
	chunk := make([]byte, 32*1024)
	for {
		n, err := p.pty.Read(chunk)
		if n > 0 {
			data := make([]byte, n)
			copy(data, chunk[:n])
			p.mu.Lock()
			off := p.base + uint64(len(p.buf))
			p.buf = append(p.buf, data...)
			if over := len(p.buf) - ReplayBytes; over > 0 {
				p.buf = append([]byte(nil), p.buf[over:]...)
				p.base += uint64(over)
			}
			p.mu.Unlock()
			m.sink.Output(p.status.ID, p.status.Run, off, data)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = err // the terminal closing under us ends the stream the same way
			}
			return
		}
	}
}

// finish waits for the process, lets its last output drain and publishes the end.
func (m *Manager) finish(p *proc, readerDone chan struct{}) {
	werr := p.cmd.Wait()
	code := 0
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	} else if werr != nil {
		code = -1
	}
	time.Sleep(200 * time.Millisecond) // ConPTY flushes its last bytes after the process is gone
	go func() { _ = p.pty.Close() }()  // Close can block on Windows until the output is read
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
	}
	p.mu.Lock()
	p.status.State = StateExited
	p.status.ExitCode = &code
	p.status.EndedAt = stamp()
	st := p.status
	p.mu.Unlock()
	close(p.done)
	m.sink.Status(st)
}

func (m *Manager) get(id string) *proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[id]
}

// Write sends keystrokes to a running process.
func (m *Manager) Write(id string, data []byte) {
	p := m.get(id)
	if p == nil || p.pty == nil {
		return
	}
	if p.snapshotStatus().State != StateRunning {
		return
	}
	_, _ = p.pty.Write(data)
}

// Resize changes the size of the terminal of a running process.
func (m *Manager) Resize(id string, cols, rows int) {
	p := m.get(id)
	if p == nil || p.pty == nil {
		return
	}
	cols, rows = clampSize(cols, rows)
	p.mu.Lock()
	running := p.status.State == StateRunning
	if running {
		p.status.Cols, p.status.Rows = cols, rows
	}
	p.mu.Unlock()
	if running {
		_ = p.pty.Resize(cols, rows)
	}
}

// Stop ends the process and everything it started. It returns once the process is gone, or after
// ten seconds if it refuses to die.
func (m *Manager) Stop(id string) error {
	p := m.get(id)
	if p == nil {
		return errors.New("not running")
	}
	if p.snapshotStatus().State != StateRunning {
		return nil
	}
	pid := p.snapshotStatus().PID
	killTree(pid)
	select {
	case <-p.done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the process did not stop")
	}
}

// Replay is the output kept for the process: the bytes in [start, end) and its state.
func (m *Manager) Replay(id string) (data []byte, start, end uint64, st Status, ok bool) {
	p := m.get(id)
	if p == nil {
		return nil, 0, 0, Status{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.buf...), p.base, p.base + uint64(len(p.buf)), p.status, true
}

// Forget drops what is kept of a process that already ended.
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.procs[id]; p != nil && p.snapshotStatus().State != StateRunning {
		delete(m.procs, id)
	}
}

// List is every process the runner knows, running ones first.
func (m *Manager) List() []Status {
	m.mu.Lock()
	all := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		all = append(all, p)
	}
	m.mu.Unlock()
	out := make([]Status, 0, len(all))
	for _, p := range all {
		out = append(out, p.snapshotStatus())
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == StateRunning) != (out[j].State == StateRunning) {
			return out[i].State == StateRunning
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Shutdown ends every running process and waits for them (the runner is stopping).
func (m *Manager) Shutdown() {
	var wg sync.WaitGroup
	for _, st := range m.List() {
		if st.State == StateRunning {
			wg.Add(1)
			go func(id string) { defer wg.Done(); _ = m.Stop(id) }(st.ID)
		}
	}
	wg.Wait()
}
