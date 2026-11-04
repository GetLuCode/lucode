package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	execpkg "claude-orchestrator/internal/executor"
	natspkg "claude-orchestrator/internal/natsbus"

	"github.com/google/uuid"
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

// remoteSilence is how long a turn on a runner may go without any sign of life (an
// event or the runner's beat, one every remoteHeartbeat) before the hub gives up.
const remoteSilence = 5 * remoteHeartbeat

// remoteTurnOpts is what the hub stores with an in-flight turn so the events of a
// turn it picks up after a restart are handled like those of a fresh one.
type remoteTurnOpts struct {
	ProjectID  string `json:"project_id"`
	AccountID  string `json:"account_id"`
	Model      string `json:"model"`
	Persistent bool   `json:"persistent"`
}

// runWithRemoteCLI runs an agent turn on opts.RunnerID and relays its events.
// The turn belongs to the runner from the ack on: if this process dies, the runner
// goes on and RecoverRunnerTurns picks the events up again at the next start.
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
			PermissionMode:     permissionModeOf(opts),
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
		m.emitProcess(sessID)
	}

	turn := dbpkg.RunnerTurn{SessionID: sessID, RunnerID: opts.RunnerID, TurnID: uuid.New().String()}
	ob, _ := json.Marshal(remoteTurnOpts{ProjectID: opts.ProjectID, AccountID: opts.AccountID, Model: opts.Model, Persistent: opts.Persistent})
	turn.Opts = string(ob)
	if err := dbpkg.SaveRunnerTurn(m.db, turn); err != nil {
		log.Printf("[runner] could not record the turn in flight sess=%s: %v", sessID[:8], err)
	}
	m.followRemoteTurn(ctx, turn, publish, false, func() error {
		return m.bus.RunnerStartTurn(turn.RunnerID, sessID, turn.TurnID, data)
	})
}

// RecoverRunnerTurns picks up the turns that were running on runners when this
// hub stopped: the session is marked running again and the events the runner kept
// in JetStream are relayed, ending with the normal finishTurn. Call it once at
// start, with OnTurnEnd and the rest of the callbacks already set.
func (m *Manager) RecoverRunnerTurns() {
	turns, err := dbpkg.ListRunnerTurns(m.db)
	if err != nil {
		log.Printf("[runner] list turns in flight: %v", err)
		return
	}
	for _, t := range turns {
		m.mu.Lock()
		_, busy := m.procs[t.SessionID]
		m.mu.Unlock()
		if busy {
			continue
		}
		var ro remoteTurnOpts
		_ = json.Unmarshal([]byte(t.Opts), &ro)
		opts := TurnOptions{SessionID: t.SessionID, ProjectID: ro.ProjectID, AccountID: ro.AccountID,
			Model: ro.Model, Persistent: ro.Persistent, RunnerID: t.RunnerID}
		log.Printf("[runner] resuming turn %s of session %.8s on %s (last event %d)", t.TurnID, t.SessionID, t.RunnerID, t.LastSeq)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		ps := &ProcessState{SessionID: t.SessionID, Done: make(chan struct{}), Cancel: cancel}
		m.mu.Lock()
		m.procs[t.SessionID] = ps
		m.mu.Unlock()
		dbpkg.UpdateSessionStatus(m.db, t.SessionID, string(StatusRunning))
		m.bus.Broadcast(t.SessionID, map[string]any{"type": "session_status", "session_id": t.SessionID, "status": string(StatusRunning)})
		if ro.Persistent {
			m.liveMu.Lock()
			m.remoteRunnerOf[t.SessionID] = t.RunnerID
			m.liveMu.Unlock()
			m.emitProcess(t.SessionID)
		}
		go func(t dbpkg.RunnerTurn) {
			defer m.finishTurn(t.SessionID, ps)
			m.followRemoteTurn(ctx, t, m.cliPublisher(t.SessionID, opts), true, nil)
		}(t)
	}
}

// followRemoteTurn relays the events of a turn on a runner until its turn_done.
// start, when not nil, sends the turn once the consumer exists (so no event is
// missed). fromStart makes a consumer that has to be created read the retained
// history. Events already relayed (seq <= the stored last_seq, or of another
// turn) are skipped, which makes redelivery harmless.
func (m *Manager) followRemoteTurn(ctx context.Context, t dbpkg.RunnerTurn, publish func(map[string]any), fromStart bool, start func() error) {
	sessID := t.SessionID
	var mu sync.Mutex
	last := t.LastSeq
	lastSignal := time.Now()
	done := make(chan struct{})
	var doneOnce sync.Once
	touch := func() { mu.Lock(); lastSignal = time.Now(); mu.Unlock() }

	forget := func() { _ = dbpkg.DeleteRunnerTurn(m.db, sessID, t.TurnID) }
	fail := func(msg string) {
		publish(map[string]any{"type": "error", "code": "runner_unavailable", "session_id": sessID, "message": msg})
		publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
		forget()
	}

	handle := func(ev natspkg.RunnerEvent) error {
		if ev.TurnID != t.TurnID {
			return nil // an earlier turn of the session
		}
		mu.Lock()
		gap := ev.Seq > last+1
		seen := ev.Seq <= last
		mu.Unlock()
		if gap { // an earlier event has not been handled (yet): ask for this one again later
			return fmt.Errorf("event %d arrived before %d", ev.Seq, last+1)
		}
		touch()
		if ev.Done {
			doneOnce.Do(func() { close(done) })
			return nil
		}
		if seen {
			return nil
		}
		var e map[string]any
		if ctx.Err() == nil && json.Unmarshal(ev.Event, &e) == nil {
			if typ, _ := e["type"].(string); typ != "heartbeat" {
				publish(e)
			}
		}
		mu.Lock()
		last = ev.Seq
		mu.Unlock()
		return dbpkg.SetRunnerTurnSeq(m.db, sessID, t.TurnID, ev.Seq)
	}

	f, err := m.bus.FollowRunnerTurn(t.RunnerID, sessID, fromStart, touch, handle)
	if err != nil {
		if ctx.Err() == nil {
			fail("The runner's events cannot be read: " + err.Error())
		} else {
			forget()
		}
		return
	}
	defer f.Close()
	if start != nil {
		if err := start(); err != nil {
			log.Printf("[runner] turn on %s failed sess=%.8s: %v", t.RunnerID, sessID, err)
			if ctx.Err() == nil {
				fail("The runner did not answer: " + err.Error() + ". Is lucode running on that machine?")
			} else {
				forget()
			}
			return
		}
	}

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	ctxDone := ctx.Done()
	var cancelDeadline <-chan time.Time
	for {
		select {
		case <-done:
			forget()
			return
		case <-ctxDone: // user cancelled or the turn timed out: the runner stops it and says turn_done
			m.bus.RunnerCancel(t.RunnerID, sessID)
			ctxDone = nil
			cancelDeadline = time.After(20 * time.Second)
		case <-cancelDeadline:
			forget()
			return
		case <-tick.C:
			mu.Lock()
			silent := time.Since(lastSignal)
			mu.Unlock()
			if silent > remoteSilence {
				if ctx.Err() == nil {
					fail("The runner went silent for " + silent.Round(time.Second).String() + ". Is lucode still running on that machine?")
				} else {
					forget()
				}
				return
			}
		}
	}
}

// ServeRemoteTurn is the runner side: it runs a turn a hub sent and publishes the
// events to the sink, ending with turn_done. It is wired to Bus.StartRunnerServer
// and does not care whether the hub is listening.
func (m *Manager) ServeRemoteTurn(payload []byte, sink *natspkg.TurnSink) {
	send := func(ev map[string]any) {
		data, _ := json.Marshal(ev)
		_ = sink.Event(data)
	}
	defer sink.Done()

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
				sink.Beat()
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
