package natsbus

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Persistent claude processes on a runner. The runner says when one starts or
// exits (core NATS, not stored) and answers a query with the ones alive now, so a
// hub that restarted, or never heard an exit, learns the truth instead of assuming.

// RunnerProcess is the state of one session's persistent claude process on a runner.
type RunnerProcess struct {
	SessionID string `json:"session_id"`
	Alive     bool   `json:"alive"`
	PID       int    `json:"pid,omitempty"`
	// At orders the notices of one runner (unix nanoseconds, taken when the event
	// happened), so a late "exited" cannot undo a newer "started".
	At int64 `json:"at,omitempty"`
}

func runnerProcessSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.process", runnerID)
}
func runnerProcessesSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.processes", runnerID)
}

// PublishRunnerProcess tells the hubs a process of this runner started or exited.
func (b *Bus) PublishRunnerProcess(p RunnerProcess) {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()
	if id == "" {
		return
	}
	if p.At == 0 {
		p.At = time.Now().UnixNano()
	}
	data, _ := json.Marshal(p)
	_ = b.nc.Publish(runnerProcessSubject(id), data)
}

// ServeRunnerProcesses answers "which processes do you have alive?" with list().
func (b *Bus) ServeRunnerProcesses(list func() []RunnerProcess) error {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()
	if id == "" {
		return fmt.Errorf("discovery not started — call StartDiscovery first")
	}
	if _, err := b.nc.Subscribe(runnerProcessesSubject(id), func(msg *nats.Msg) {
		procs := list()
		if procs == nil {
			procs = []RunnerProcess{}
		}
		data, _ := json.Marshal(procs)
		_ = msg.Respond(data)
	}); err != nil {
		return fmt.Errorf("subscribe runner processes: %w", err)
	}
	return b.nc.Flush()
}

// WatchRunnerProcesses calls handle for every start/exit notice of any runner.
func (b *Bus) WatchRunnerProcesses(handle func(runnerID string, p RunnerProcess)) error {
	if _, err := b.nc.Subscribe("lucode.runner.*.process", func(msg *nats.Msg) {
		parts := strings.Split(msg.Subject, ".") // lucode.runner.<id>.process
		if len(parts) != 4 {
			return
		}
		var p RunnerProcess
		if json.Unmarshal(msg.Data, &p) != nil || p.SessionID == "" {
			return
		}
		handle(parts[2], p)
	}); err != nil {
		return fmt.Errorf("subscribe runner process notices: %w", err)
	}
	return b.nc.Flush()
}

// RunnerProcesses asks a runner which persistent processes it has alive. A runner
// that is gone, or too old to know the question, returns an error.
func (b *Bus) RunnerProcesses(runnerID string) ([]RunnerProcess, error) {
	msg, err := b.nc.Request(runnerProcessesSubject(runnerID), nil, 2*time.Second)
	if err != nil {
		return nil, err
	}
	var procs []RunnerProcess
	if err := json.Unmarshal(msg.Data, &procs); err != nil {
		return nil, err
	}
	return procs, nil
}
