package session

import dbpkg "claude-orchestrator/internal/db"

// ProcessInfo says whether a session has a persistent claude process alive between
// turns. A one-shot turn has its process only while it runs, which the session status
// already tells, so it is not reported here.
type ProcessInfo struct {
	Alive  bool   `json:"alive"`
	PID    int    `json:"pid,omitempty"`    // on the machine that runs it (the runner's, when Runner is set)
	Runner string `json:"runner,omitempty"` // the runner holding it, when it is not this instance
	// Workspace is where claude works (cwd, path, branch, worktree, repo), as the
	// machine running it reported at its last start; nil until the first turn.
	Workspace map[string]any `json:"workspace,omitempty"`
}

// WorkspaceOf is the last claude_workspace event of a session (nil if none yet).
func (m *Manager) WorkspaceOf(sessID string) map[string]any {
	m.liveMu.Lock()
	ws := m.workspaces[sessID]
	m.liveMu.Unlock()
	if ws == nil && m.db != nil { // after a restart of the hub: the last one saved
		ws = dbpkg.GetSessionWorkspace(m.db, sessID)
	}
	return ws
}

// ProcessInfoOf reports the persistent claude process of a session. A process on a
// remote runner is assumed alive from its first turn until it is closed: the runner does
// not report its idle exit back.
func (m *Manager) ProcessInfoOf(sessID string) ProcessInfo {
	ws := m.WorkspaceOf(sessID)
	if l := m.getLive(sessID); l != nil {
		return ProcessInfo{Alive: true, PID: l.PID(), Workspace: ws}
	}
	m.liveMu.Lock()
	runner, pid := m.remoteRunnerOf[sessID], m.remotePID[sessID]
	m.liveMu.Unlock()
	if runner != "" {
		return ProcessInfo{Alive: true, PID: pid, Runner: runner, Workspace: ws}
	}
	return ProcessInfo{Workspace: ws}
}

// emitProcess tells the browsers the persistent process of a session started or ended.
func (m *Manager) emitProcess(sessID string) {
	if m.bus == nil {
		return
	}
	info := m.ProcessInfoOf(sessID)
	m.bus.Broadcast(sessID, map[string]any{
		"type": "claude_process", "session_id": sessID,
		"alive": info.Alive, "pid": info.PID, "runner": info.Runner,
	})
}

// TurnActive reports whether the session has a turn in flight.
func (m *Manager) TurnActive(sessID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, active := m.procs[sessID]
	return active
}
