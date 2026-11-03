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
	// Persistent keeps the claude process alive on the runner between turns.
	Persistent bool `json:"persistent,omitempty"`
}

const remoteHeartbeat = 30 * time.Second

var (
	remoteMu    sync.Mutex
	remoteRuns  = map[string]context.CancelFunc{} // turns this instance runs for a hub
	remoteLives = map[string]*remoteLive{}        // persistent processes it keeps for a hub
)

// remoteLive is a persistent claude process of a session a hub owns. Its events
// go to the inbox of the turn in flight, which changes with every turn.
type remoteLive struct {
	live *execpkg.Live

	mu      sync.Mutex
	sink    func(map[string]any) // publishes to the current turn's inbox
	ctxSeen chan struct{}        // closed when the turn's context_usage arrives
}

func (r *remoteLive) setTurn(sink func(map[string]any)) {
	r.mu.Lock()
	r.sink = sink
	r.ctxSeen = make(chan struct{})
	r.mu.Unlock()
}

func (r *remoteLive) publish(ev map[string]any) {
	r.mu.Lock()
	sink, seen := r.sink, r.ctxSeen
	r.mu.Unlock()
	if sink != nil {
		sink(ev)
	}
	if t, _ := ev["type"].(string); t == "context_usage" && seen != nil {
		select {
		case <-seen:
		default:
			close(seen)
		}
	}
}

// runWithRemoteCLI runs an agent turn on opts.RunnerID and relays its events.
func (m *Manager) runWithRemoteCLI(ctx context.Context, ps *ProcessState, opts TurnOptions) {
	sessID := ps.SessionID
	defer m.finishTurn(sessID, ps)

	// No SetWorkDir: opts.WorkDirs are folders of the runner, and the hub uses that
	// cache to find settings files on its own disk.
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
		Persistent:  opts.Persistent,
	}
	data, _ := json.Marshal(rt)
	if opts.Persistent {
		m.liveMu.Lock()
		m.remoteRunnerOf[sessID] = opts.RunnerID // so CloseLive can reach the process
		m.liveMu.Unlock()
	}

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

	if rt.Persistent {
		m.serveRemoteLive(ctx, req, send)
		return
	}
	execpkg.Run(ctx, req, send)
}

// serveRemoteLive runs a turn on the session's persistent process, starting one if
// there is none (or it is unusable), and returns when the turn ends.
func (m *Manager) serveRemoteLive(ctx context.Context, req execpkg.TurnRequest, send func(map[string]any)) {
	sessID := req.SessionID

	remoteMu.Lock()
	rl := remoteLives[sessID]
	remoteMu.Unlock()

	var done <-chan struct{}
	if rl != nil {
		rl.setTurn(send)
		d, err := rl.live.Send(req.Message, req.ImageBase64, req.ImageMediaType)
		if err == nil {
			done = d
		} else {
			log.Printf("[runner] live process for %s unusable (%v); starting a new one", sessID[:8], err)
			dropRemoteLive(sessID, rl)
			rl.live.Kill()
			rl = nil
		}
	}
	if rl == nil {
		rl = &remoteLive{}
		rl.setTurn(send)
		cur := rl
		live, d, err := execpkg.StartLive(req, rl.publish, func() { dropRemoteLive(sessID, cur) })
		if err != nil {
			send(map[string]any{"type": "error", "session_id": sessID, "message": err.Error()})
			send(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
			return
		}
		rl.live, done = live, d
		remoteMu.Lock()
		remoteLives[sessID] = rl
		remoteMu.Unlock()
	}

	select {
	case <-done:
		// claude reports the context usage right after the result; let it through
		// before the stream is closed, as it would arrive to nobody afterwards.
		rl.mu.Lock()
		seen := rl.ctxSeen
		rl.mu.Unlock()
		select {
		case <-seen:
		case <-time.After(3 * time.Second):
		}
	case <-ctx.Done(): // cancelled by the user (or the turn timed out): stop the process
		rl.live.Kill()
	}
}

func dropRemoteLive(sessID string, rl *remoteLive) {
	remoteMu.Lock()
	if remoteLives[sessID] == rl {
		delete(remoteLives, sessID)
	}
	remoteMu.Unlock()
}

// CloseRemoteLive closes the persistent process kept for a hub's session; the
// next turn starts a fresh one with --resume.
func (m *Manager) CloseRemoteLive(sessID string) {
	remoteMu.Lock()
	rl := remoteLives[sessID]
	delete(remoteLives, sessID)
	remoteMu.Unlock()
	if rl != nil {
		rl.live.Close()
	}
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
