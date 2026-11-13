// Command lucode-runner runs claude for a lucode hub. It is the machine-side half of
// lucode: it has no web UI, no HTTP port and none of the hub's API (projects, kanban,
// sessions). It talks to the hub only over NATS: it accepts turns, streams their events
// through JetStream, keeps the persistent claude processes between turns, and answers the
// few requests a hub makes of a machine (browse folders, manage preconfigured AIs).
//
//	lucode-runner auth login        sign in to a hub (asks for the hub, user and password)
//	lucode-runner configure         profile name and working directory
//	lucode-runner start             connect and run
//
// Or, without the settings file, with the flags: lucode-runner --nats hub-host --working-dir ~/dev
//
// A runner keeps its conversations alive when the hub restarts: see internal/natsbus
// (runner_exec.go) and internal/session (remote.go).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	apipkg "claude-orchestrator/internal/api"
	"claude-orchestrator/internal/claudecreds"
	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	pluginrunner "claude-orchestrator/internal/plugins/runner"
	sessionpkg "claude-orchestrator/internal/session"
	"claude-orchestrator/internal/taskmcp"
	versionpkg "claude-orchestrator/internal/version"
)

// embeddedHook (hook_<os>.go) is the lucodehook binary of this OS, baked in at build time.

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-task" {
		// claude starts this subcommand as the MCP server that gives an agent the lucode
		// tools; see session.Manager.writeTaskMCPConfig.
		taskmcp.RunFromEnv()
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "pair" {
		runPair(os.Args[2:])
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "auth":
			runAuth(os.Args[2:])
			return
		case "configure":
			runConfigure(os.Args[2:])
			return
		case "start":
			runStart(os.Args[2:])
			return
		}
	}

	natsURL := flag.String("nats", "nats://127.0.0.1:4222", "NATS server of the hub: host, host:port or a full URL (needs JetStream: nats-server -js)")
	flag.StringVar(natsURL, "nats-url", "nats://127.0.0.1:4222", "alias of --nats")
	natsCreds := flag.String("nats-creds", "", "path to NATS credentials file (.creds)")
	dataDir := flag.String("data", defaultDataDir(), "directory of the runner's database and identity")
	workingDir := flag.String("working-dir", "", "root folder hubs may browse when choosing a project dir (default: home)")
	pluginPolicy := flag.String("plugin-policy", plugins.PolicyStandard, "what plugins may do here: off (none), standard (fs, git, http if granted), full (exec too)")
	noRefresh := flag.Bool("no-claude-refresh", false, "do not renew the Claude logins of this machine in the background")
	caFile := flag.String("nats-ca", "", "CA file that signed the hub's certificate (for a wss:// address the system does not trust)")
	flag.Parse()
	runRunner(runnerOptions{
		NATSURL:   natspkg.NormalizeURL(*natsURL), // "hub" or "192.168.1.5" are enough
		NATSCreds: *natsCreds, CAFile: *caFile, DataDir: *dataDir, WorkingDir: *workingDir, PluginPolicy: *pluginPolicy, NoClaudeRefresh: *noRefresh,
	})
}

// runnerOptions is everything a running runner needs, from the flags or from a profile.
type runnerOptions struct {
	NATSURL      string
	NATSCreds    string
	CAFile       string
	DataDir      string
	WorkingDir   string
	PluginPolicy string
	// NoClaudeRefresh turns off the keeper that renews the Claude logins (internal/claudecreds).
	NoClaudeRefresh bool
}

func runRunner(o runnerOptions) {
	natsURL, natsCreds, dataDir, workingDir, pluginPolicy := &o.NATSURL, &o.NATSCreds, &o.DataDir, &o.WorkingDir, &o.PluginPolicy
	if !plugins.ValidPolicy(*pluginPolicy) {
		log.Fatalf("--plugin-policy must be off, standard or full, not %q", *pluginPolicy)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	// Same file name as lucode: a machine that ran the full lucode as a runner keeps its
	// preconfigured AIs when it switches to this binary (with --data pointing at the old dir).
	db, err := dbpkg.Init(filepath.Join(*dataDir, "lucode.db"))
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
	if o.CAFile != "" {
		opts = append(opts, nats.RootCAs(o.CAFile))
		// the hooks and MCP servers claude starts connect to the hub too, and need the same CA
		os.Setenv("LUCODE_NATS_CA", o.CAFile)
	}
	nc, err := nats.Connect(*natsURL, opts...)
	if err != nil {
		log.Fatalf("nats connect %s: %v", natspkg.RedactURL(*natsURL), err)
	}
	defer nc.Drain()
	log.Printf("[nats] connected — %s", natspkg.RedactURL(*natsURL))

	// The hub's own subscriptions (bus.Start: tool approvals, task tools, permissions) are
	// not started: those sessions belong to the hub, not to this machine.
	bus := natspkg.NewBus(nc)
	manager := sessionpkg.NewManager(db, bus, *natsURL, sessionpkg.InstallHookSettings(embeddedHook))
	manager.BaseDir = *workingDir
	if *natsCreds != "" {
		manager.SetNATSCreds(*natsCreds)
	}

	v := versionpkg.Current("runner")
	log.Printf("[runner] version %s (commit time %s, built %s)", v.Short, v.CommitTime, v.BuiltAt.Format(time.RFC3339))
	instanceID, _ := natspkg.LoadOrCreateInstanceID(*dataDir)
	if err := bus.StartDiscovery(natspkg.InstanceInfo{
		ID:    instanceID,
		Type:  "lucode",
		Extra: map[string]any{"runner_only": true, "version": versionpkg.Current("runner").Short, plugins.DiscoveryPolicyKey: *pluginPolicy},
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
	agent, err := pluginrunner.New(pluginrunner.Options{NATS: nc, RunnerID: instanceID, DataDir: *dataDir, Policy: *pluginPolicy})
	if err != nil {
		log.Fatalf("plugins: %v", err)
	}
	if err := agent.Start(); err != nil {
		log.Fatalf("plugins: %v", err)
	}
	log.Printf("[runner] ready — id %s, plugin policy %s", instanceID, *pluginPolicy)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !o.NoClaudeRefresh {
		// Keeps the Claude logins of this machine (the default dir and the preconfigured AIs) from
		// expiring while nobody uses them. It replaces the standalone credfeeder.
		keeper := &claudecreds.Keeper{Logf: log.Printf, Dirs: func() []string {
			dirs := []string{claudecreds.DefaultDir()}
			if ps, err := dbpkg.ListAIProfiles(db); err == nil {
				for _, p := range ps {
					dirs = append(dirs, p.ClaudeConfigDir)
				}
			}
			return dirs
		}}
		go keeper.Run(ctx)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("[runner] stopping")
}

// runPair is lucode-runner pair <code>: it writes the pairing and exits. A subcommand and never a
// flag of the running process, so the secret does not sit in the process list.
func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dataDir := fs.String("data", defaultDataDir(), "directory of the runner's database and identity")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal("usage: lucode-runner pair [--data dir] <code>; an admin of the hub creates the code in Settings, Plugins")
	}
	p, err := plugins.ParsePairingCode(fs.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	id, err := natspkg.LoadOrCreateInstanceID(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	if p.Runner != id {
		log.Fatalf("this code is for the runner %s, and this data dir belongs to the runner %s: create one for this runner", p.Runner, id)
	}
	if err := plugins.SavePairing(*dataDir, p); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Paired: runner %s takes plugins from that hub from now on. A running runner needs no restart.\n", id)
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode-runner"
	}
	return filepath.Join(home, ".lucode-runner")
}
