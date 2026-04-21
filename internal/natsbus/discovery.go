package natsbus

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	discoverySubject  = "lucode.discovery"
	heartbeatInterval = 15 * time.Second
	peerTTL           = 45 * time.Second
)

// InstanceInfo describes a discovered service on the NATS network.
type InstanceInfo struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"` // "lucode" | "aiapi"
	Hostname  string         `json:"hostname"`
	OS        string         `json:"os"`
	StartedAt time.Time      `json:"started_at"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type peerEntry struct {
	info     InstanceInfo
	lastSeen time.Time
}

// LoadOrCreateInstanceID reads ~/.lucode/instance.json (or dataDir/instance.json),
// creating a new stable UUID if the file doesn't exist yet.
func LoadOrCreateInstanceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "instance.json")
	type idFile struct {
		ID string `json:"id"`
	}
	if data, err := os.ReadFile(path); err == nil {
		var f idFile
		if json.Unmarshal(data, &f) == nil && f.ID != "" {
			return f.ID, nil
		}
	}
	id := uuid.New().String()
	data, _ := json.Marshal(idFile{ID: id})
	os.WriteFile(path, data, 0644)
	return id, nil
}

// StartDiscovery begins publishing heartbeats and collecting peer info.
// info.ID must be set; Hostname and OS are filled automatically if empty.
func (b *Bus) StartDiscovery(info InstanceInfo) error {
	if info.Hostname == "" {
		info.Hostname, _ = os.Hostname()
	}
	if info.OS == "" {
		info.OS = runtime.GOOS
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}

	b.peersMu.Lock()
	b.selfInfo = info
	b.peersMu.Unlock()

	// Subscribe to peer heartbeats
	if _, err := b.nc.Subscribe(discoverySubject, b.handleDiscovery); err != nil {
		return err
	}

	// Publish own heartbeat immediately, then every interval
	b.publishHeartbeat()
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for range t.C {
			b.publishHeartbeat()
			b.evictStalePeers()
		}
	}()

	log.Printf("[discovery] started id=%s type=%s hostname=%s", info.ID, info.Type, info.Hostname)
	return nil
}

// Peers returns a snapshot of currently known remote instances (excludes self).
func (b *Bus) Peers() []InstanceInfo {
	b.peersMu.RLock()
	defer b.peersMu.RUnlock()
	out := make([]InstanceInfo, 0, len(b.peers))
	for _, e := range b.peers {
		out = append(out, e.info)
	}
	return out
}

// SelfInfo returns this instance's identity.
func (b *Bus) SelfInfo() InstanceInfo {
	b.peersMu.RLock()
	defer b.peersMu.RUnlock()
	return b.selfInfo
}

func (b *Bus) publishHeartbeat() {
	b.peersMu.RLock()
	info := b.selfInfo
	b.peersMu.RUnlock()
	data, _ := json.Marshal(info)
	if err := b.nc.Publish(discoverySubject, data); err != nil {
		log.Printf("[discovery] publish error: %v", err)
	}
}

func (b *Bus) handleDiscovery(msg *nats.Msg) {
	var info InstanceInfo
	if err := json.Unmarshal(msg.Data, &info); err != nil || info.ID == "" {
		return
	}
	b.peersMu.Lock()
	self := b.selfInfo.ID
	b.peersMu.Unlock()

	if info.ID == self {
		return // ignore own heartbeat
	}

	b.peersMu.Lock()
	_, isNew := b.peers[info.ID]
	b.peers[info.ID] = peerEntry{info: info, lastSeen: time.Now()}
	cb := b.OnPeersChange
	b.peersMu.Unlock()

	if !isNew {
		log.Printf("[discovery] new peer id=%s type=%s host=%s", info.ID, info.Type, info.Hostname)
	}
	if cb != nil {
		cb(b.Peers())
	}
}

func (b *Bus) evictStalePeers() {
	b.peersMu.Lock()
	changed := false
	for id, e := range b.peers {
		if time.Since(e.lastSeen) > peerTTL {
			log.Printf("[discovery] peer gone id=%s type=%s", id, e.info.Type)
			delete(b.peers, id)
			changed = true
		}
	}
	cb := b.OnPeersChange
	b.peersMu.Unlock()

	if changed && cb != nil {
		cb(b.Peers())
	}
}

