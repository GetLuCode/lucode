package natsbus

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// A newcomer learns who is already there right away, not at their next heartbeat
// (15 s), and is told about a peer once, not on every heartbeat.
func TestNewcomerDiscoversPeersImmediately(t *testing.T) {
	suffix := time.Now().Format("150405.000")
	old := testBus(t, "disc-old-"+suffix) // announces itself once, long before the newcomer

	nc, err := nats.Connect("nats://127.0.0.1:4222", nats.Timeout(time.Second))
	if err != nil {
		t.Skipf("no NATS: %v", err)
	}
	t.Cleanup(nc.Close)
	fresh := NewBus(nc)
	var changes atomic.Int32
	fresh.OnPeersChange = func([]InstanceInfo) { changes.Add(1) }

	started := time.Now()
	if err := fresh.StartDiscovery(InstanceInfo{ID: "disc-new-" + suffix, Type: "lucode-test"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hasPeer(fresh, "disc-old-"+suffix) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasPeer(fresh, "disc-old-"+suffix) {
		t.Fatalf("the newcomer did not discover the existing instance within 3 s (heartbeat is %s)", heartbeatInterval)
	}
	t.Logf("discovered after %s", time.Since(started))

	// the existing instance sees the newcomer too (its heartbeat on start)
	if !hasPeer(old, "disc-new-"+suffix) {
		time.Sleep(300 * time.Millisecond)
		if !hasPeer(old, "disc-new-"+suffix) {
			t.Error("the existing instance never saw the newcomer")
		}
	}

	// more heartbeats from a known peer are not a change
	before := changes.Load()
	old.publishHeartbeat()
	old.publishHeartbeat()
	time.Sleep(300 * time.Millisecond)
	if after := changes.Load(); after != before {
		t.Errorf("OnPeersChange fired %d more times for heartbeats of a known peer", after-before)
	}
}

func hasPeer(b *Bus, id string) bool {
	for _, p := range b.Peers() {
		if p.ID == id {
			return true
		}
	}
	return false
}

func TestInstanceRole(t *testing.T) {
	cases := []struct {
		extra  map[string]any
		role   string
		runner bool
	}{
		{nil, RoleBoth, true}, // an older instance announces nothing
		{map[string]any{"role": RoleHub}, RoleHub, false},
		{map[string]any{"role": RoleRunner}, RoleRunner, true},
		{map[string]any{"role": RoleBoth}, RoleBoth, true},
	}
	for _, c := range cases {
		i := InstanceInfo{Extra: c.extra}
		if i.Role() != c.role || i.IsRunner() != c.runner {
			t.Errorf("extra %v: role %q runner %v, want %q %v", c.extra, i.Role(), i.IsRunner(), c.role, c.runner)
		}
	}
}
