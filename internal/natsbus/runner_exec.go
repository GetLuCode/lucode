package natsbus

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
)

// Remote turns. A hub sends a turn to the runner a project works on; the runner
// runs claude and streams the events back on the request's inbox, exactly like
// an aiapi instance does.

func runnerTurnSubject(runnerID string) string { return fmt.Sprintf("lucode.runner.%s.turn", runnerID) }
func runnerCloseSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.close", runnerID)
}
func runnerCancelSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.cancel", runnerID)
}

// StartRunnerServer makes this instance accept turns from a hub. run receives the
// payload and the inbox to publish events to; cancel is told which session's turn
// to stop; closeLive, which session's persistent process to close.
func (b *Bus) StartRunnerServer(run func(payload []byte, replySubject string), cancel, closeLive func(sessID string)) error {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()
	if id == "" {
		return fmt.Errorf("discovery not started — call StartDiscovery first")
	}
	if _, err := b.nc.Subscribe(runnerTurnSubject(id), func(msg *nats.Msg) {
		if msg.Reply == "" {
			log.Printf("[runner] turn without reply subject — ignoring")
			return
		}
		go func() {
			payload, err := b.decodeLarge(msg.Data)
			if err != nil {
				log.Printf("[runner] %v", err)
				b.PublishStreamEvent(msg.Reply, []byte(`{"type":"error","message":"runner: could not read the turn: `+jsonEscape(err.Error())+`"}`))
				b.PublishStreamDone(msg.Reply)
				return
			}
			run(payload, msg.Reply)
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

// RunnerExecute sends a turn to a runner and calls cb for every event it streams
// back, until the stream ends, errors, or cb returns false.
func (b *Bus) RunnerExecute(runnerID string, payload []byte, cb func(event []byte) bool) error {
	data, err := b.encodeLarge(payload)
	if err != nil {
		return err
	}
	return b.streamExecute(runnerTurnSubject(runnerID), data, cb)
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

func jsonEscape(s string) string {
	q, _ := json.Marshal(s)
	return string(q[1 : len(q)-1])
}
