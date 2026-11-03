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
func runnerCancelSubject(runnerID string) string {
	return fmt.Sprintf("lucode.runner.%s.cancel", runnerID)
}

// StartRunnerServer makes this instance accept turns from a hub. run receives the
// payload and the inbox to publish events to; cancel is told which session to stop.
func (b *Bus) StartRunnerServer(run func(payload []byte, replySubject string), cancel func(sessID string)) error {
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
		go run(msg.Data, msg.Reply)
	}); err != nil {
		return fmt.Errorf("subscribe runner turn: %w", err)
	}
	if _, err := b.nc.Subscribe(runnerCancelSubject(id), func(msg *nats.Msg) {
		var req struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(msg.Data, &req) == nil && req.SessionID != "" {
			cancel(req.SessionID)
		}
	}); err != nil {
		return fmt.Errorf("subscribe runner cancel: %w", err)
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
	return b.streamExecute(runnerTurnSubject(runnerID), payload, cb)
}

// RunnerCancel asks a runner to stop the turn it runs for a session.
func (b *Bus) RunnerCancel(runnerID, sessID string) {
	data, _ := json.Marshal(map[string]string{"session_id": sessID})
	if err := b.nc.Publish(runnerCancelSubject(runnerID), data); err != nil {
		log.Printf("[runner] cancel publish: %v", err)
	}
}
