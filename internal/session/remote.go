package session

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	execpkg "claude-orchestrator/internal/executor"
)

// Turns on a runner. The hub (this instance, owner of the session) sends the turn
// to the runner's lucode, which spawns claude with its own settings, hooks and
// preconfigured AI, and streams the events back. Tool approvals and task tools
// still reach the hub over NATS because the session id is the same.

// RemoteTurn is what the hub sends to a runner.
type RemoteTurn struct {
	Req         execpkg.TurnRequest `json:"req"`
	LucodeTools []string            `json:"lucode_tools"`
	// AIProfileID names a preconfigured AI of the runner; the runner resolves its
	// claude_config_dir because that path only exists there.
	AIProfileID string `json:"ai_profile_id,omitempty"`
}

const remoteHeartbeat = 30 * time.Second

var (
	remoteMu   sync.Mutex
	remoteRuns = map[string]context.CancelFunc{} // sessions this instance runs for a hub
)

// runWithRemoteCLI runs an agent turn on opts.RunnerID and relays its events.
func (m *Manager) runWithRemoteCLI(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)

	if len(opts.WorkDirs) > 0 {
		m.SetWorkDir(sessID, opts.WorkDirs[0])
	}
	publish := m.cliPublisher(sessID, opts)

	rt := RemoteTurn{
		Req: execpkg.TurnRequest{
			SessionID:          sessID,
			ProjectID:          opts.ProjectID,
			Message:            opts.Message,
			Model:              opts.Model,
			Provider:           execpkg.ProviderClaude,
			WorkDirs:           opts.WorkDirs,
			Resume:             opts.Resume,
			Name:               opts.AgentName,
			Effort:             opts.Effort,
			Thinking:           opts.Thinking,
			AppendSystemPrompt: opts.SystemPrompt,
			Tools:              opts.Tools,
			ImageBase64:        opts.ImageBase64,
			ImageMediaType:     opts.ImageMediaType,
		},
		LucodeTools: opts.LucodeTools,
		AIProfileID: opts.AIProfileID,
	}
	data, _ := json.Marshal(rt)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			m.bus.RunnerCancel(opts.RunnerID, sessID) // user cancelled or timed out
		case <-stop:
		}
	}()

	err := m.bus.RunnerExecute(opts.RunnerID, data, func(raw []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		var ev map[string]any
		if json.Unmarshal(raw, &ev) != nil {
			return true
		}
		if t, _ := ev["type"].(string); t == "heartbeat" {
			return true
		}
		publish(ev)
		return true
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("[runner] turn on %s failed sess=%s: %v", opts.RunnerID, sessID[:8], err)
		publish(map[string]any{
			"type": "error", "code": "runner_unavailable", "session_id": sessID,
			"message": "The runner did not answer: " + err.Error() + ". Is lucode running on that machine?",
		})
		publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
	}
}

// ServeRemoteTurn is the runner side: it runs a turn a hub sent and streams the
// events to replySubject. It is wired to Bus.StartRunnerServer.
func (m *Manager) ServeRemoteTurn(payload []byte, replySubject string) {
	send := func(ev map[string]any) {
		data, _ := json.Marshal(ev)
		_ = m.bus.PublishStreamEvent(replySubject, data)
	}
	defer m.bus.PublishStreamDone(replySubject)

	var rt RemoteTurn
	if err := json.Unmarshal(payload, &rt); err != nil || rt.Req.SessionID == "" {
		send(map[string]any{"type": "error", "message": "runner: malformed turn request"})
		return
	}
	req := rt.Req
	sessID := req.SessionID

	if rt.AIProfileID != "" {
		ai, err := dbpkg.GetAIProfile(m.db, rt.AIProfileID)
		if err != nil {
			send(map[string]any{"type": "error", "code": "ai_profile_missing", "session_id": sessID,
				"message": "The preconfigured AI no longer exists on this runner."})
			send(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
			return
		}
		req.ClaudeConfigDir = ai.ClaudeConfigDir
	}
	// Everything that points at this machine is filled in here, not by the hub.
	req.MCPConfig = m.writeTaskMCPConfig(sessID, rt.LucodeTools)
	req.ClaudeSettings = m.claudeSettings
	req.NATSSettings = m.backendNATSURL
	req.NATSCreds = m.natsCreds

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	remoteMu.Lock()
	remoteRuns[sessID] = cancel
	remoteMu.Unlock()
	defer func() {
		cancel()
		remoteMu.Lock()
		delete(remoteRuns, sessID)
		remoteMu.Unlock()
	}()

	tick := time.NewTicker(remoteHeartbeat)
	defer tick.Stop()
	go func() {
		for {
			select {
			case <-tick.C:
				send(map[string]any{"type": "heartbeat"})
			case <-ctx.Done():
				return
			}
		}
	}()

	execpkg.Run(ctx, req, send)
}

// CancelRemoteTurn stops a turn this instance runs for a hub.
func (m *Manager) CancelRemoteTurn(sessID string) {
	remoteMu.Lock()
	cancel := remoteRuns[sessID]
	remoteMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
