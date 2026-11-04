package natsbus

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Remote turns.
//
// A hub sends a turn to the runner a project works on (request/reply on
// lucode.runner.<id>.turn, answered at once with an ack). From then on the turn is
// the runner's: it keeps running if the hub goes away, and every event it
// produces is published to the JetStream stream LUCODE_RUNNER_EVENTS on
// lucode.runner.<runnerID>.events.<sessionID>, numbered per turn, ending with an
// explicit turn_done event. The hub reads the stream through a durable consumer
// with explicit acks, so a hub that was down gets the events it missed, in order,
// when it comes back. Delivery is at least once: the hub drops the events it
// already stored (turn_id + seq).
//
// Cancel and close stay plain NATS (lucode.runner.<id>.cancel / .close). The
// runner also says it is alive with a light core message every 30 s per turn
// (lucode.runner.<id>.beat.<sessionID>), which is not stored.

const (
	RunnerStreamName = "LUCODE_RUNNER_EVENTS"
	runnerStreamTTL  = 24 * time.Hour

	// Envelope types in the stream.
	runnerEventType    = "event"
	runnerTurnDoneType = "turn_done"
)

func runnerTurnSubject(runnerID string) string { return fmt.Sprintf("lucode.runner.%s.turn", runnerID) }
func runnerCloseSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.close", runnerID)
}
func runnerCancelSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.cancel", runnerID)
}
func runnerEventsSubject(runnerID, sessID string) string {
	return fmt.Sprintf("lucode.runner.%s.events.%s", runnerID, sessID)
}
func runnerBeatSubject(runnerID, sessID string) string {
	return fmt.Sprintf("lucode.runner.%s.beat.%s", runnerID, sessID)
}

// EnsureRunnerStream creates the events stream if it does not exist. It is safe to
// call from every instance and as often as needed; a success is remembered, a
// failure (no JetStream) is not, since the server may be restarted with -js.
func (b *Bus) EnsureRunnerStream() error {
	b.runnerStreamMu.Lock()
	defer b.runnerStreamMu.Unlock()
	if b.runnerStreamOK {
		return nil
	}
	js, err := b.nc.JetStream()
	if err != nil {
		return fmt.Errorf("runner events need JetStream: start nats-server with -js (%w)", err)
	}
	if _, err := js.StreamInfo(RunnerStreamName); err != nil {
		_, addErr := js.AddStream(&nats.StreamConfig{
			Name:        RunnerStreamName,
			Description: "events of turns run on runners, kept until the hub has them",
			Subjects:    []string{"lucode.runner.*.events.*"},
			Retention:   nats.LimitsPolicy,
			Storage:     nats.FileStorage,
			MaxAge:      runnerStreamTTL,
			MaxBytes:    1 << 30,
			Discard:     nats.DiscardOld,
			Duplicates:  5 * time.Minute,
		})
		if addErr != nil {
			if _, err2 := js.StreamInfo(RunnerStreamName); err2 != nil { // lost a race with another instance: fine
				return fmt.Errorf("runner events need JetStream: start nats-server with -js (%w)", addErr)
			}
		}
	}
	b.runnerStreamOK = true
	return nil
}

// ── runner side ──────────────────────────────────────────────────────────────

// TurnSink is where a runner publishes the events of one turn.
type TurnSink struct {
	b        *Bus
	runnerID string
	sessID   string
	turnID   string

	mu  sync.Mutex
	seq int
}

// NewTurnSink returns the sink of a turn. StartRunnerServer hands one to the run
// callback; it is exported for tests.
func (b *Bus) NewTurnSink(runnerID, sessID, turnID string) *TurnSink {
	return &TurnSink{b: b, runnerID: runnerID, sessID: sessID, turnID: turnID}
}

// Event publishes one event of the turn.
func (s *TurnSink) Event(ev []byte) error { return s.publish(runnerEventType, ev) }

// Done publishes the end of the turn. Nothing may be published after it.
func (s *TurnSink) Done() error { return s.publish(runnerTurnDoneType, nil) }

// Beat tells the hub the turn is still alive (not stored).
func (s *TurnSink) Beat() {
	data, _ := json.Marshal(map[string]string{"turn_id": s.turnID})
	_ = s.b.nc.Publish(runnerBeatSubject(s.runnerID, s.sessID), data)
}

func (s *TurnSink) publish(typ string, ev []byte) error {
	// Numbering and publishing under one lock keeps the order in the stream.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	env := map[string]any{"turn_id": s.turnID, "seq": s.seq, "type": typ}
	if ev != nil {
		env["event"] = json.RawMessage(ev)
	}
	raw, _ := json.Marshal(env)
	data, err := s.b.encodeLarge(raw)
	if err != nil {
		return err
	}
	// The message id lets JetStream drop a retry whose first attempt did arrive.
	msgID := fmt.Sprintf("%s:%d", s.turnID, s.seq)
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if err := s.b.EnsureRunnerStream(); err != nil {
			lastErr = err
		} else if js, err := s.b.nc.JetStream(); err != nil {
			lastErr = err
		} else if _, err := js.Publish(runnerEventsSubject(s.runnerID, s.sessID), data, nats.MsgId(msgID)); err != nil {
			lastErr = err
		} else {
			return nil
		}
		time.Sleep(time.Second)
	}
	log.Printf("[runner] could not publish %s #%d of turn %s: %v", typ, s.seq, s.turnID, lastErr)
	return lastErr
}

// runnerTurnReq is what a hub sends to start a turn; Payload is opaque here.
type runnerTurnReq struct {
	TurnID    string          `json:"turn_id"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload"`
}

type runnerAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// StartRunnerServer makes this instance accept turns from a hub. run receives the
// payload and the sink to publish the turn's events to (it must end by calling
// sink.Done); it runs in its own goroutine and does not depend on the hub staying
// connected. cancel is told which session's turn to stop; closeLive, which
// session's persistent process to close.
func (b *Bus) StartRunnerServer(run func(payload []byte, sink *TurnSink), cancel, closeLive func(sessID string)) error {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()
	if id == "" {
		return fmt.Errorf("discovery not started — call StartDiscovery first")
	}
	if err := b.EnsureRunnerStream(); err != nil {
		log.Printf("[runner] %v — turns will be refused until JetStream is available", err)
	}

	var runningMu sync.Mutex
	running := map[string]bool{} // turn ids being run, so a retried request does not start a turn twice

	if _, err := b.nc.Subscribe(runnerTurnSubject(id), func(msg *nats.Msg) {
		if msg.Reply == "" {
			log.Printf("[runner] turn without reply subject — ignoring")
			return
		}
		reply := func(a runnerAck) {
			data, _ := json.Marshal(a)
			_ = msg.Respond(data)
		}
		go func() {
			raw, err := b.decodeLarge(msg.Data)
			var req runnerTurnReq
			if err == nil {
				err = json.Unmarshal(raw, &req)
			}
			if err == nil && (req.TurnID == "" || req.SessionID == "") {
				err = fmt.Errorf("turn without turn_id or session_id")
			}
			if err != nil {
				log.Printf("[runner] %v", err)
				reply(runnerAck{Error: "runner: could not read the turn: " + err.Error()})
				return
			}
			if err := b.EnsureRunnerStream(); err != nil {
				reply(runnerAck{Error: err.Error()})
				return
			}
			runningMu.Lock()
			dup := running[req.TurnID]
			running[req.TurnID] = true
			runningMu.Unlock()
			reply(runnerAck{OK: true})
			if dup {
				return
			}
			defer func() {
				runningMu.Lock()
				delete(running, req.TurnID)
				runningMu.Unlock()
			}()
			run(req.Payload, b.NewTurnSink(id, req.SessionID, req.TurnID))
		}()
	}); err != nil {
		return fmt.Errorf("subscribe runner turn: %w", err)
	}
	bySession := func(subject string, fn func(string)) error {
		_, err := b.nc.Subscribe(subject, func(msg *nats.Msg) {
			var req struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(msg.Data, &req) == nil && req.SessionID != "" {
				fn(req.SessionID)
			}
		})
		return err
	}
	if err := bySession(runnerCancelSubject(id), cancel); err != nil {
		return fmt.Errorf("subscribe runner cancel: %w", err)
	}
	if err := bySession(runnerCloseSubject(id), closeLive); err != nil {
		return fmt.Errorf("subscribe runner close: %w", err)
	}
	if err := b.nc.Flush(); err != nil { // the server must know the subscriptions before anyone sends
		return fmt.Errorf("flush runner subscriptions: %w", err)
	}
	log.Printf("[runner] accepting turns on %s", runnerTurnSubject(id))
	return nil
}

// ── hub side ─────────────────────────────────────────────────────────────────

// RunnerStartTurn asks a runner to start a turn and waits for its ack. After that
// the turn runs on the runner whatever happens to this process; follow it with
// FollowRunnerTurn (subscribe first, so no event is missed).
func (b *Bus) RunnerStartTurn(runnerID, sessID, turnID string, payload []byte) error {
	if err := b.EnsureRunnerStream(); err != nil {
		return err
	}
	raw, _ := json.Marshal(runnerTurnReq{TurnID: turnID, SessionID: sessID, Payload: payload})
	data, err := b.encodeLarge(raw)
	if err != nil {
		return err
	}
	msg, err := b.nc.Request(runnerTurnSubject(runnerID), data, 10*time.Second)
	if err != nil {
		return fmt.Errorf("the runner did not accept the turn: %w", err)
	}
	var ack runnerAck
	if json.Unmarshal(msg.Data, &ack) != nil || !ack.OK {
		return fmt.Errorf("the runner refused the turn: %s", ack.Error)
	}
	return nil
}

// RunnerEvent is one message of a turn read from the stream.
type RunnerEvent struct {
	TurnID string
	Seq    int  // 1, 2, ... per turn
	Done   bool // the turn_done event; Event is nil
	Event  []byte
}

// RunnerFollower reads the events of a session on a runner.
type RunnerFollower struct {
	sub  *nats.Subscription
	beat *nats.Subscription
}

// Close stops reading. The durable consumer stays, with its position, for the
// next FollowRunnerTurn of the session.
func (f *RunnerFollower) Close() {
	if f == nil {
		return
	}
	_ = f.sub.Unsubscribe()
	_ = f.beat.Unsubscribe()
}

var consumerNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// FollowRunnerTurn delivers every stream event of a session on a runner to handle,
// in order, through a durable consumer with explicit acks: a message is acked
// only when handle returns nil, and redelivered after a delay when it errs. The
// same event can arrive more than once, so handle must be idempotent.
//
// There is one consumer per (runner, session), created on first use and kept
// across turns and hub restarts. A new one starts after the last event
// (fromStart=false: the caller subscribes before starting the turn) or at the
// beginning of the retained history (fromStart=true, to recover a turn whose
// consumer is gone); events of other turns are for handle to skip. onBeat is
// called for every liveness message of the runner.
func (b *Bus) FollowRunnerTurn(runnerID, sessID string, fromStart bool, onBeat func(), handle func(RunnerEvent) error) (*RunnerFollower, error) {
	if err := b.EnsureRunnerStream(); err != nil {
		return nil, err
	}
	js, err := b.nc.JetStream()
	if err != nil {
		return nil, err
	}
	name := "hub-" + consumerNameRe.ReplaceAllString(runnerID+"-"+sessID, "_")

	cb := func(m *nats.Msg) {
		raw, err := b.readLarge(m.Data, false) // may be redelivered: do not delete the object
		if err != nil {
			log.Printf("[runner] %v", err)
			_ = m.NakWithDelay(2 * time.Second)
			return
		}
		var env struct {
			TurnID string          `json:"turn_id"`
			Seq    int             `json:"seq"`
			Type   string          `json:"type"`
			Event  json.RawMessage `json:"event"`
		}
		if json.Unmarshal(raw, &env) != nil {
			_ = m.Ack() // poison message: nobody could ever read it
			return
		}
		ev := RunnerEvent{TurnID: env.TurnID, Seq: env.Seq, Done: env.Type == runnerTurnDoneType, Event: env.Event}
		if err := handle(ev); err != nil {
			log.Printf("[runner] handling event %d of turn %s: %v", ev.Seq, ev.TurnID, err)
			_ = m.NakWithDelay(time.Second)
			return
		}
		_ = m.Ack()
	}

	subject := runnerEventsSubject(runnerID, sessID)
	// The consumer is created here and only bound by Subscribe: nats.go deletes the
	// consumers it creates itself when the subscription ends, even durable ones.
	if _, err := js.ConsumerInfo(RunnerStreamName, name); err != nil {
		cfg := &nats.ConsumerConfig{
			Durable:           name,
			DeliverSubject:    nats.NewInbox(),
			FilterSubject:     subject,
			AckPolicy:         nats.AckExplicitPolicy,
			AckWait:           30 * time.Second,
			InactiveThreshold: 48 * time.Hour,
			// One message at a time: a failed one is redelivered before the next is
			// sent, so the handler sees the turn strictly in order.
			MaxAckPending: 1,
			DeliverPolicy: nats.DeliverNewPolicy,
		}
		if fromStart {
			cfg.DeliverPolicy = nats.DeliverAllPolicy
		}
		if _, err := js.AddConsumer(RunnerStreamName, cfg); err != nil {
			return nil, fmt.Errorf("create runner events consumer: %w", err)
		}
	}
	sub, err := js.Subscribe(subject, cb, nats.Bind(RunnerStreamName, name), nats.ManualAck())
	if err != nil {
		return nil, fmt.Errorf("follow runner events: %w", err)
	}
	beat, err := b.nc.Subscribe(runnerBeatSubject(runnerID, sessID), func(*nats.Msg) {
		if onBeat != nil {
			onBeat()
		}
	})
	if err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	return &RunnerFollower{sub: sub, beat: beat}, nil
}

// RunnerCancel asks a runner to stop the turn it runs for a session.
func (b *Bus) RunnerCancel(runnerID, sessID string) {
	data, _ := json.Marshal(map[string]string{"session_id": sessID})
	if err := b.nc.Publish(runnerCancelSubject(runnerID), data); err != nil {
		log.Printf("[runner] cancel publish: %v", err)
	}
}

// RunnerClose asks a runner to close the persistent claude process of a session.
func (b *Bus) RunnerClose(runnerID, sessID string) {
	data, _ := json.Marshal(map[string]string{"session_id": sessID})
	if err := b.nc.Publish(runnerCloseSubject(runnerID), data); err != nil {
		log.Printf("[runner] close publish: %v", err)
	}
}
