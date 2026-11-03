package natsbus

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	aiapiExecuteSubject = "lucode.aiapi.%s.execute"
	streamDoneType      = "stream_done"
	streamTimeout       = 30 * time.Minute
	streamChunkTimeout  = 2 * time.Minute
)

func aiapiSubject(instanceID string) string {
	return fmt.Sprintf(aiapiExecuteSubject, instanceID)
}

// StartAIAPIServer subscribes this aiapi instance to handle execute requests.
// fn receives the raw payload bytes and the inbox subject to publish events to.
func (b *Bus) StartAIAPIServer(fn func(payload []byte, replySubject string)) error {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()

	if id == "" {
		return fmt.Errorf("discovery not started — call StartDiscovery first")
	}

	subj := aiapiSubject(id)
	_, err := b.nc.Subscribe(subj, func(msg *nats.Msg) {
		if msg.Reply == "" {
			log.Printf("[aiapi-bridge] received execute without reply subject — ignoring")
			return
		}
		go fn(msg.Data, msg.Reply)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subj, err)
	}
	log.Printf("[aiapi-bridge] serving on %s", subj)
	return nil
}

// PublishStreamEvent publishes a single SSE event JSON to the given reply subject.
// Used by aiapi to stream events back to the requesting lucode.
func (b *Bus) PublishStreamEvent(replySubject string, event []byte) error {
	data, err := b.encodeLarge(event)
	if err != nil {
		return err
	}
	return b.nc.Publish(replySubject, data)
}

// PublishStreamDone signals the end of an aiapi stream.
func (b *Bus) PublishStreamDone(replySubject string) error {
	data, _ := json.Marshal(map[string]string{"type": streamDoneType})
	return b.nc.Publish(replySubject, data)
}

// AIAPIExecute sends a turn execution request to a remote aiapi instance via NATS
// and calls cb for each streamed event. Returns when the stream is done or errors.
// cb returns false to cancel early.
func (b *Bus) AIAPIExecute(instanceID string, payload []byte, cb func(event []byte) bool) error {
	return b.streamExecute(aiapiSubject(instanceID), payload, cb)
}

// streamExecute publishes a request on subj and feeds the events streamed back on
// its inbox to cb, until the done sentinel arrives.
func (b *Bus) streamExecute(subj string, payload []byte, cb func(event []byte) bool) error {
	inbox := nats.NewInbox()

	sub, err := b.nc.SubscribeSync(inbox)
	if err != nil {
		return fmt.Errorf("subscribe inbox: %w", err)
	}
	defer sub.Unsubscribe()

	if err := b.nc.PublishRequest(subj, inbox, payload); err != nil {
		return fmt.Errorf("publish execute: %w", err)
	}

	deadline := time.Now().Add(streamTimeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		wait := streamChunkTimeout
		if remaining < wait {
			wait = remaining
		}
		msg, err := sub.NextMsg(wait)
		if err != nil {
			return fmt.Errorf("stream receive: %w", err)
		}

		data, err := b.decodeLarge(msg.Data)
		if err != nil {
			return err
		}
		msg.Data = data

		// Check for done sentinel before calling cb
		var peek struct {
			Type string `json:"type"`
		}
		json.Unmarshal(msg.Data, &peek)
		if peek.Type == streamDoneType {
			return nil
		}

		if !cb(msg.Data) {
			return nil
		}
	}
	return fmt.Errorf("aiapi stream timeout")
}
