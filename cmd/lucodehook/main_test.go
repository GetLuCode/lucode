package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func tempNATS(t *testing.T) *nats.Conn {
	t.Helper()
	bin := os.Getenv("NATS_SERVER_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("nats-server"); err != nil {
			t.Skip("no nats-server binary (set NATS_SERVER_BIN)")
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin, "-a", "127.0.0.1", "-p", fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 50; i++ {
		if nc, err := nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", port)); err == nil {
			t.Cleanup(nc.Close)
			return nc
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return nil
}

func TestAskHubGivesUpWhenNoHubComesBack(t *testing.T) {
	nc := tempNATS(t)
	if _, err := askHub(nc, "lucode.permissions.s", []byte(`{}`), 0); !errors.Is(err, nats.ErrNoResponders) {
		t.Fatalf("want no responders, got %v", err)
	}
}

// A hub restart loses nothing if the request arrives while it is down.
func TestAskHubWaitsForAHubThatIsRestarting(t *testing.T) {
	nc := tempNATS(t)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if _, err := nc.Subscribe("lucode.permissions.s", func(m *nats.Msg) { m.Respond([]byte(`{"decision":"allow"}`)) }); err != nil {
			t.Error(err)
		}
	}()
	msg, err := askHub(nc, "lucode.permissions.s", []byte(`{}`), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data) != `{"decision":"allow"}` {
		t.Errorf("got %s", msg.Data)
	}
}
