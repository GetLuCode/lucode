package natsbus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// Large messages.
//
// NATS rejects any message above the server's max_payload (1 MB by default), and
// JetStream does not lift that limit for messages. What it does offer is the
// Object Store, which splits an object into chunks. A payload above
// largeThreshold (an image attached to a turn, a big tool result...) is put in
// that store and the message carries only {"_obj":"<name>"}; the receiver reads
// the object back and deletes it. Without JetStream (nats-server -js) payloads
// that fit max_payload still go directly and bigger ones fail with a clear error.

const (
	largeBucket    = "lucode-large"
	largeThreshold = 256 * 1024
	largeTTL       = time.Hour // objects nobody read (crashed receiver) expire on their own
	largePrefix    = `{"_obj":`
)

type largeStore struct {
	mu sync.Mutex
	os nats.ObjectStore
}

// objectStore returns the bucket, creating it on first use. A failure (JetStream
// disabled) is not cached: the server may be restarted with -js while we run.
func (b *Bus) objectStore() (nats.ObjectStore, error) {
	b.large.mu.Lock()
	defer b.large.mu.Unlock()
	if b.large.os != nil {
		return b.large.os, nil
	}
	js, err := b.nc.JetStream()
	if err != nil {
		return nil, err
	}
	store, err := js.ObjectStore(largeBucket)
	if err != nil {
		store, err = js.CreateObjectStore(&nats.ObjectStoreConfig{
			Bucket:      largeBucket,
			Description: "payloads too big for a NATS message",
			TTL:         largeTTL,
			Storage:     nats.FileStorage,
		})
		if err != nil {
			return nil, err
		}
	}
	b.large.os = store
	return store, nil
}

// encodeLarge returns what to publish for data: data itself when it is small,
// else a reference to an object holding it.
func (b *Bus) encodeLarge(data []byte) ([]byte, error) {
	if len(data) <= largeThreshold {
		return data, nil
	}
	store, err := b.objectStore()
	if err != nil {
		if int64(len(data)) <= b.nc.MaxPayload() {
			return data, nil // still fits a plain message
		}
		return nil, fmt.Errorf("a %d byte message exceeds the NATS limit (%d) and JetStream is not available: start nats-server with -js (%v)",
			len(data), b.nc.MaxPayload(), err)
	}
	name := uuid.New().String()
	if _, err := store.PutBytes(name, data); err != nil {
		return nil, fmt.Errorf("store large message: %w", err)
	}
	ref, _ := json.Marshal(map[string]string{"_obj": name})
	return ref, nil
}

// decodeLarge undoes encodeLarge on the receiving side.
func (b *Bus) decodeLarge(data []byte) ([]byte, error) { return b.readLarge(data, true) }

// readLarge is decodeLarge with a choice: keep=false leaves the object in the store,
// for messages that may be delivered again (it expires by itself after largeTTL).
func (b *Bus) readLarge(data []byte, del bool) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(largePrefix)) {
		return data, nil
	}
	var ref struct {
		Obj string `json:"_obj"`
	}
	if err := json.Unmarshal(data, &ref); err != nil || ref.Obj == "" {
		return data, nil // not our envelope after all
	}
	store, err := b.objectStore()
	if err != nil {
		return nil, fmt.Errorf("read large message: %w", err)
	}
	full, err := store.GetBytes(ref.Obj)
	if err != nil {
		return nil, fmt.Errorf("read large message %s: %w", ref.Obj, err)
	}
	if del {
		_ = store.Delete(ref.Obj) // single receiver: free the space now
	}
	return full, nil
}
