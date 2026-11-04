// Command lucode-runner runs claude for a lucode hub. It is the machine-side half of
// lucode: it has no web UI, no HTTP port and none of the hub's API (projects, kanban,
// sessions). It talks to the hub only over NATS: it accepts turns, streams their events
// through JetStream, keeps the persistent claude processes between turns, and answers the
// few requests a hub makes of a machine (browse folders, manage preconfigured AIs).
//
//	lucode-runner --nats-url nats://hub-host:4222 --working-dir ~/dev
//
// A runner keeps its conversations alive when the hub restarts: see internal/natsbus
// (runner_exec.go) and internal/session (remote.go).
package main

import (
	_ "embed"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
	"claude-orchestrator/internal/taskmcp"
)

// embeddedHook is the lucodehook binary baked into this executable at build time (see
// build.sh). It is the approval hook claude calls before a tool runs; the hub answers it.
//
//go:embed embedded/lucodehook
var embeddedHook []byte

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-task" {
		// claude starts this subcommand as the MCP server that gives an agent the lucode
		// tools; see session.Manager.writeTaskMCPConfig.
		taskmcp.RunFromEnv()
		return
	}

	natsURL := flag.String("nats-url", "nats://127.0.0.1:4222", "NATS server of the hub (needs JetStream: nats-server -js)")
	natsCreds := flag.String("nats-creds", "", "path to NATS credentials file (.creds)")
	dataDir := flag.String("data", defaultDataDir(), "directory of the runner's database and identity")
	workingDir := flag.String("working-dir", "", "root folder hubs may browse when choosing a project dir (default: home)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	db, err := dbpkg.Init(filepath.Join(*dataDir, "lucode-runner.db"))
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	defer db.Close()

	opts := []nats.Option{
		nats.Timeout(10 * time.Second),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // a runner outlives hub and NATS restarts: never give up
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Printf("[nats] disconnected: %v", err) }),
		nats.ReconnectHandler(func(c *nats.Conn) { log.Printf("[nats] reconnected to %s", c.ConnectedUrl()) }),
	}
	if *natsCreds != "" {
		opts = append(opts, nats.UserCredentials(*natsCreds))
	}
	nc, err := nats.Connect(*natsURL, opts...)
	if err != nil {
		log.Fatalf("nats connect %s: %v", *natsURL, err)
	}
	defer nc.Drain()
	log.Printf("[nats] connected — %s", *natsURL)

	// The hub's own subscriptions (bus.Start: tool approvals, task tools, permissions) are
	// not started: those sessions belong to the hub, not to this machine.
	bus := natspkg.NewBus(nc)
	manager := sessionpkg.NewManager(db, bus, *natsURL, sessionpkg.InstallHookSettings(embeddedHook))
	if *natsCreds != "" {
		manager.SetNATSCreds(*natsCreds)
	}

	instanceID, _ := natspkg.LoadOrCreateInstanceID(*dataDir)
	if err := bus.StartDiscovery(natspkg.InstanceInfo{
		ID:    instanceID,
		Type:  "lucode",
		Extra: map[string]any{"runner_only": true},
	}); err != nil {
		log.Fatalf("discovery: %v", err)
	}
	if err := bus.StartRunnerServer(manager.ServeRemoteTurn, manager.CancelRemoteTurn, manager.CloseRemoteLive); err != nil {
		log.Fatalf("runner server: %v", err)
	}
	if err := bus.ServeRunnerProcesses(manager.RunnerProcesses); err != nil {
		log.Fatalf("runner processes: %v", err)
	}
	if err := bus.StartRPCServer(apipkg.NewRunnerHandler(db, bus, manager, *workingDir)); err != nil {
		log.Fatalf("rpc server: %v", err)
	}
	log.Printf("[runner] ready — id %s", instanceID)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("[runner] stopping")
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode-runner"
	}
	return filepath.Join(home, ".lucode-runner")
}
