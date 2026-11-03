package session

import (
	"testing"

	execpkg "claude-orchestrator/internal/executor"
)

func TestProcessInfoOfAndTurnActive(t *testing.T) {
	m := &Manager{
		lives:          map[string]*execpkg.Live{},
		remoteRunnerOf: map[string]string{"remote": "kali"},
		procs:          map[string]*ProcessState{"busy": {}},
	}
	if got := m.ProcessInfoOf("none"); got.Alive {
		t.Errorf("a session without a persistent process is not alive: %+v", got)
	}
	if got := m.ProcessInfoOf("remote"); !got.Alive || got.Runner != "kali" || got.PID != 0 {
		t.Errorf("a remote process is reported with its runner: %+v", got)
	}
	if !m.TurnActive("busy") || m.TurnActive("none") {
		t.Error("TurnActive should follow the running turns")
	}
}
