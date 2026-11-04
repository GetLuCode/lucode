package session

import (
	"log"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
)

// Persistent claude processes on runners, seen from the hub. The runner tells when
// one starts or exits (WatchRunnerProcesses) and answers "what do you have alive?"
// (SyncRunnerProcesses), so the hub does not have to assume.

// WatchRunnerProcesses makes the hub follow the start/exit notices of every runner.
func (m *Manager) WatchRunnerProcesses() error {
	return m.bus.WatchRunnerProcesses(m.HandleRunnerProcess)
}

// HandleRunnerProcess applies one notice of a runner. Notices about sessions this hub
// does not own (a runner can serve several hubs) are ignored, and so are stale ones:
// an older notice than the last applied, or an exit of a process that is not the one
// the hub knows.
func (m *Manager) HandleRunnerProcess(runnerID string, p natspkg.RunnerProcess) {
	if _, err := dbpkg.GetSession(m.db, p.SessionID); err != nil {
		return
	}
	sess := p.SessionID
	m.liveMu.Lock()
	if p.At != 0 && p.At < m.remoteAt[sess] {
		m.liveMu.Unlock()
		return
	}
	if p.At != 0 {
		if m.remoteAt == nil {
			m.remoteAt = map[string]int64{}
		}
		m.remoteAt[sess] = p.At
	}
	changed := false
	if p.Alive {
		if m.remoteRunnerOf == nil {
			m.remoteRunnerOf = map[string]string{}
		}
		if m.remotePID == nil {
			m.remotePID = map[string]int{}
		}
		changed = m.remoteRunnerOf[sess] != runnerID || m.remotePID[sess] != p.PID
		m.remoteRunnerOf[sess] = runnerID
		m.remotePID[sess] = p.PID
	} else if m.remoteRunnerOf[sess] == runnerID {
		known := m.remotePID[sess]
		if known == 0 || p.PID == 0 || known == p.PID {
			delete(m.remoteRunnerOf, sess)
			delete(m.remotePID, sess)
			changed = true
		}
	}
	m.liveMu.Unlock()
	if changed {
		m.emitProcess(sess)
	}
}

// SyncRunnerProcesses asks a runner which processes it has alive and brings the hub's
// view of that runner in line: it adds the ones it did not know and forgets the ones
// the runner no longer has. A runner that does not answer (down, or an older version)
// is left as it is.
func (m *Manager) SyncRunnerProcesses(runnerID string) {
	procs, err := m.bus.RunnerProcesses(runnerID)
	if err != nil {
		log.Printf("[runner] processes of %s unknown: %v", runnerID, err)
		return
	}
	alive := make(map[string]bool, len(procs))
	for _, p := range procs {
		p.Alive = true
		alive[p.SessionID] = true
		m.HandleRunnerProcess(runnerID, p)
	}
	m.liveMu.Lock()
	var gone []string
	for sess, r := range m.remoteRunnerOf {
		if r == runnerID && !alive[sess] {
			gone = append(gone, sess)
		}
	}
	m.liveMu.Unlock()
	for _, sess := range gone {
		if m.TurnActive(sess) {
			continue // its process may be starting right now
		}
		m.liveMu.Lock()
		if m.remoteRunnerOf[sess] == runnerID {
			delete(m.remoteRunnerOf, sess)
			delete(m.remotePID, sess)
		}
		m.liveMu.Unlock()
		m.emitProcess(sess)
	}
}

// RunnerProcesses lists the persistent processes this instance keeps for hubs (it is
// the answer to a hub's SyncRunnerProcesses).
func (m *Manager) RunnerProcesses() []natspkg.RunnerProcess {
	remoteMu.Lock()
	defer remoteMu.Unlock()
	out := make([]natspkg.RunnerProcess, 0, len(remoteLives))
	for sess, rl := range remoteLives {
		if rl.live != nil && rl.live.Alive() {
			out = append(out, natspkg.RunnerProcess{SessionID: sess, Alive: true, PID: rl.live.PID()})
		}
	}
	return out
}
