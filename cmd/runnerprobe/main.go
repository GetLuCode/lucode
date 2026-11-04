// Command runnerprobe checks a remote lucode runner over NATS, as a hub would: it
// discovers the runner, lists a directory and its preconfigured AIs through the RPC
// bridge, and runs two turns of a persistent agent session.
//
//	runnerprobe --nats-url nats://192.168.1.5:4222 --dir /home/me/dev [--host name]
//
// With the runner using a fake claude (LUCODE_CLAUDE_BIN, see internal/fakeclaude)
// the turns replay a recording and spend no tokens.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	execpkg "claude-orchestrator/internal/executor"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := flag.String("nats-url", "nats://127.0.0.1:4222", "NATS server")
	dir := flag.String("dir", "", "directory of the runner to list")
	host := flag.String("host", "", "hostname of the runner (default: the first other lucode peer)")
	noTurns := flag.Bool("no-turns", false, "only check discovery and the RPC calls; run no turns (spends no tokens even with the real claude)")
	flag.Parse()
	*natsURL = natspkg.NormalizeURL(*natsURL)
	log.SetFlags(0)

	nc, err := nats.Connect(*natsURL, nats.Timeout(5*time.Second))
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	defer nc.Drain()
	bus := natspkg.NewBus(nc)
	if err := bus.StartDiscovery(natspkg.InstanceInfo{ID: uuid.New().String(), Type: "probe"}); err != nil {
		log.Fatalf("discovery: %v", err)
	}
	time.Sleep(2 * time.Second) // instances answer a discovery query within ~100 ms

	var runner *natspkg.InstanceInfo
	fmt.Println("peers:")
	for _, p := range bus.Peers() {
		fmt.Printf("  %-8s %-14s %-8s %s\n", p.Type, p.Hostname, p.OS, p.ID)
		if p.Type == "lucode" && (*host == "" || p.Hostname == *host) && runner == nil {
			c := p
			runner = &c
		}
	}
	if runner == nil {
		log.Fatal("no lucode runner found")
	}
	fmt.Printf("\nrunner: %s (%s)\n", runner.Hostname, runner.ID)

	call := func(path string) {
		req, _ := http.NewRequest("GET", "http://x"+path, http.NoBody)
		resp, err := bus.RPCRequest(runner.ID, req)
		if err != nil {
			fmt.Printf("GET %s -> error: %v\n", path, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		s := strings.TrimSpace(string(body))
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		fmt.Printf("GET %s -> %d %s\n", path, resp.StatusCode, s)
	}
	call("/api/ai-profiles")
	call("/api/fs/dirs?path=" + url.QueryEscape(*dir))
	if *noTurns {
		return
	}

	sess := uuid.New().String()
	turn := func(n int, msg string) {
		rt := sessionpkg.RemoteTurn{Persistent: true, LucodeTools: []string{}}
		rt.Req = execpkg.TurnRequest{SessionID: sess, Message: msg, Model: "haiku", Provider: execpkg.ProviderClaude}
		if *dir != "" {
			rt.Req.WorkDirs = []string{*dir}
		}
		payload, _ := json.Marshal(rt)
		var text strings.Builder
		var usage bool
		turnID := uuid.New().String()
		done := make(chan struct{})
		f, err := bus.FollowRunnerTurn(runner.ID, sess, false, nil, func(re natspkg.RunnerEvent) error {
			if re.TurnID != turnID {
				return nil
			}
			if re.Done {
				close(done)
				return nil
			}
			var ev map[string]any
			if json.Unmarshal(re.Event, &ev) != nil {
				return nil
			}
			switch ev["type"] {
			case "message_ready":
				if ev["role"] == "assistant" {
					text.WriteString(fmt.Sprint(ev["content"]))
				}
			case "context_usage":
				usage = true
			case "error":
				fmt.Printf("  error event: %v\n", ev)
			}
			return nil
		})
		if err == nil {
			defer f.Close()
			if err = bus.RunnerStartTurn(runner.ID, sess, turnID, payload); err == nil {
				select {
				case <-done:
				case <-time.After(3 * time.Minute):
					err = fmt.Errorf("timeout waiting for turn_done")
				}
			}
		}
		fmt.Printf("turn %d: reply=%q context_usage=%v err=%v\n", n, strings.TrimSpace(text.String()), usage, err)
	}
	turn(1, "Remember the secret word PINEAPPLE. Reply with just: ok")
	turn(2, "What was the secret word? Answer with just the word.")
	bus.RunnerClose(runner.ID, sess)
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
