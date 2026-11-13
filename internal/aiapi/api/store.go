package api

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Turn holds all events for a single AI turn.
type Turn struct {
	ID        string
	SessionID string

	mu      sync.Mutex
	events  []map[string]any
	notify  chan struct{} // closed+replaced on each new event
	done    bool
}

// TurnStore is an in-memory store of active turns.
type TurnStore struct {
	mu    sync.RWMutex
	turns map[string]*Turn
}

func NewTurnStore() *TurnStore {
	return &TurnStore{turns: make(map[string]*Turn)}
}

func (s *TurnStore) New(sessionID string) *Turn {
	t := &Turn{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		notify:    make(chan struct{}),
	}
	s.mu.Lock()
	s.turns[t.ID] = t
	s.mu.Unlock()
	return t
}

func (s *TurnStore) Get(id string) (*Turn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.turns[id]
	return t, ok
}

// Publish appends an event and notifies waiting SSE streams.
func (t *Turn) Publish(ev map[string]any) {
	t.mu.Lock()
	t.events = append(t.events, ev)
	old := t.notify
	t.notify = make(chan struct{})
	if isTerminalEvent(ev) {
		t.done = true
	}
	t.mu.Unlock()
	close(old)
}

// Subscribe returns buffered events from offset onward, a channel notifying
// when new events arrive, and whether the turn is already done.
func (t *Turn) Subscribe(offset int) ([]map[string]any, <-chan struct{}, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot := append([]map[string]any(nil), t.events[offset:]...)
	return snapshot, t.notify, t.done
}

// Stream sends all events to the provided callback, blocking until done or ctx cancelled.
func (t *Turn) Stream(ctx context.Context, send func(map[string]any)) {
	offset := 0
	for {
		events, notify, done := t.Subscribe(offset)
		for _, ev := range events {
			send(ev)
		}
		offset += len(events)
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-notify:
		}
	}
}

func isTerminalEvent(ev map[string]any) bool {
	if ev["type"] == "session_status" {
		status, _ := ev["status"].(string)
		return status == "idle" || status == "error"
	}
	if ev["type"] == "turn_complete" {
		return true
	}
	return false
}
