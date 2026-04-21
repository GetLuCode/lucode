package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	natsclient "github.com/nats-io/nats.go"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	hookcfg "claude-orchestrator/internal/hookconfig"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
)

const serverPort = 8090

var (
	flagNATSURL   = flag.String("nats-url", "nats://127.0.0.1:4222", "NATS server URL")
	flagNATSCreds = flag.String("nats-creds", "", "Path to NATS credentials file (.creds)")
	flagAIAPIURL  = flag.String("aiapi-url", "http://localhost:8091", "URL of the aiapi service")
	flagAuthURL   = flag.String("auth-url", "http://localhost:8092", "URL of the centralized auth service")
	flagWorkDir   = flag.String("working-dir", "", "Base directory for workspace picker")
)

type App struct {
	ctx context.Context
}

func NewApp() *App {
	flag.Parse()
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.startServer()
}

func (a *App) shutdown(_ context.Context) {}

func (a *App) startServer() {
	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	dbPath := filepath.Join(dataDir, "lucode.db")
	db, err := dbpkg.Init(dbPath)
	if err != nil {
		log.Fatalf("db init: %v", err)
	}

	log.Printf("[desktop] os.Args: %v", os.Args)
	log.Printf("[desktop] nats-url=%s nats-creds=%q", *flagNATSURL, *flagNATSCreds)

	natsOpts := []natsclient.Option{
		natsclient.Timeout(10 * time.Second),
		natsclient.RetryOnFailedConnect(false),
	}
	if *flagNATSCreds != "" {
		natsOpts = append(natsOpts, natsclient.UserCredentials(*flagNATSCreds))
	}
	nc, err := natsclient.Connect(*flagNATSURL, natsOpts...)
	if err != nil {
		log.Fatalf("nats connect %s: %v", *flagNATSURL, err)
	}
	defer nc.Drain()

	bus := natspkg.NewBus(nc)
	bus.OnBroadcast = apipkg.BroadcastToWS

	hookBinary := defaultHookBinary()
	backendBaseURL := fmt.Sprintf("http://localhost:%d", serverPort)
	claudeSettingsJSON, err := hookcfg.BuildJSON(hookBinary, backendBaseURL)
	if err != nil {
		log.Fatalf("build claude settings: %v", err)
	}

	manager := sessionpkg.NewManager(db, bus, *flagNATSURL, claudeSettingsJSON)
	if *flagNATSCreds != "" {
		manager.SetNATSCreds(*flagNATSCreds)
	}
	manager.SetAIAPIURL(*flagAIAPIURL)

	if err := bus.Start(); err != nil {
		log.Fatalf("nats bus: %v", err)
	}

	handler := apipkg.New(db, bus, manager, *flagWorkDir)
	handler.SetAuthURL(*flagAuthURL)

	addr := fmt.Sprintf(":%d", serverPort)
	log.Printf("[desktop] lucode listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, apipkg.Wrap(handler)); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode"
	}
	return filepath.Join(home, ".lucode")
}

func defaultHookBinary() string {
	if _, err := os.Stat("./tmp/lucodehook"); err == nil {
		abs, _ := filepath.Abs("./tmp/lucodehook")
		return abs
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", "lucodehook")
}
