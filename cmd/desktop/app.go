package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	natsclient "github.com/nats-io/nats.go"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
)

const defaultLucodePort = 8090

// desktopConfig is read from ~/.lucode/desktop.json on startup.
type desktopConfig struct {
	NATSUrl               string `json:"nats_url"`
	NATSCreds             string `json:"nats_creds"`
	AuthURL               string `json:"auth_url"`
	AIAPIURL              string `json:"aiapi_url"`
	WorkDir               string `json:"working_dir"`
	LucodePort            int    `json:"lucode_port"`
	MinimaxSubscriptionKey string `json:"minimax_subscription_key"`
}

var (
	flagNATSURL   = flag.String("nats-url", "", "NATS server URL")
	flagNATSCreds = flag.String("nats-creds", "", "Path to NATS credentials file (.creds)")
	flagAIAPIURL  = flag.String("aiapi-url", "", "URL of the aiapi service")
	flagAuthURL   = flag.String("auth-url", "", "URL of the centralized auth service")
	flagWorkDir   = flag.String("working-dir", "", "Base directory for workspace picker")
	flagPort      = flag.Int("port", 0, "Lucode HTTP port (overrides desktop.json lucode_port)")
)

type App struct {
	ctx        context.Context
	mu         sync.Mutex
	lucodePort int
	handler    http.Handler
}

func NewApp() *App {
	flag.Parse()
	return &App{}
}

// GetLucodePort is a Wails binding — called from JS to get the WS URL port.
func (a *App) GetLucodePort() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lucodePort == 0 {
		return defaultLucodePort
	}
	return a.lucodePort
}

// ServeHTTP forwards API requests from the Wails asset server to the Go handler.
// This lets the frontend use relative URLs (/api/...) without knowing the port.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	h := a.handler
	a.mu.Unlock()
	if h == nil {
		http.Error(w, "server starting", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.startServer()
}

func (a *App) shutdown(_ context.Context) {}

// Minimise minimises the window.
func (a *App) Minimise() { wailsruntime.WindowMinimise(a.ctx) }

// ToggleMaximise toggles between maximised and normal window state.
func (a *App) ToggleMaximise() { wailsruntime.WindowToggleMaximise(a.ctx) }

// IsMaximised returns whether the window is currently maximised.
func (a *App) IsMaximised() bool { return wailsruntime.WindowIsMaximised(a.ctx) }

// CloseApp quits the application.
func (a *App) CloseApp() { wailsruntime.Quit(a.ctx) }

func (a *App) startServer() {
	defer func() {
		if r := recover(); r != nil {
			log.Fatalf("[desktop] PANIC in startServer: %v", r)
		}
	}()

	cfg := loadConfig()

	natsURL   := firstNonEmpty(*flagNATSURL, cfg.NATSUrl, "nats://127.0.0.1:4222")
	natsCreds := firstNonEmpty(*flagNATSCreds, cfg.NATSCreds)
	authURL   := firstNonEmpty(*flagAuthURL, cfg.AuthURL, "http://localhost:8092")
	aiAPIURL  := firstNonEmpty(*flagAIAPIURL, cfg.AIAPIURL, "http://localhost:8091")
	workDir   := firstNonEmpty(*flagWorkDir, cfg.WorkDir)

	lucodePort := defaultLucodePort
	if *flagPort != 0 {
		lucodePort = *flagPort
	} else if cfg.LucodePort != 0 {
		lucodePort = cfg.LucodePort
	}

	a.mu.Lock()
	a.lucodePort = lucodePort
	a.mu.Unlock()

	log.Printf("[desktop] nats=%s auth=%s aiapi=%s port=%d", natsURL, authURL, aiAPIURL, lucodePort)

	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	db, err := dbpkg.Init(filepath.Join(dataDir, "lucode.db"))
	if err != nil {
		log.Fatalf("db init: %v", err)
	}

	natsOpts := []natsclient.Option{
		natsclient.Timeout(10 * time.Second),
		natsclient.RetryOnFailedConnect(false),
	}
	if natsCreds != "" {
		natsOpts = append(natsOpts, natsclient.UserCredentials(natsCreds))
	}
	nc, err := natsclient.Connect(natsURL, natsOpts...)
	if err != nil {
		log.Fatalf("nats connect %s: %v", natsURL, err)
	}
	defer nc.Drain()

	bus := natspkg.NewBus(nc)
	bus.OnBroadcast = apipkg.BroadcastToWS

	hookBinary := defaultHookBinary()
	_ = hookBinary // legacy: claude-CLI hook path removed; permission gate is now NATS-based

	manager := sessionpkg.NewManager(db, bus, natsURL, "")
	if natsCreds != "" {
		manager.SetNATSCreds(natsCreds)
	}
	manager.SetAIAPIURL(aiAPIURL)

	if err := bus.Start(); err != nil {
		log.Fatalf("nats bus: %v", err)
	}

	instanceID, _ := natspkg.LoadOrCreateInstanceID(dataDir)
	if err := bus.StartDiscovery(natspkg.InstanceInfo{
		ID:   instanceID,
		Type: "lucode",
	}); err != nil {
		log.Printf("[discovery] start error: %v", err)
	}

	apiHandler := apipkg.New(db, bus, manager, workDir)
	apiHandler.SetAuthURL(authURL)
	if cfg.MinimaxSubscriptionKey != "" {
		apiHandler.SetMinimaxSubscriptionKey(cfg.MinimaxSubscriptionKey)
	}
	wrapped := apipkg.Wrap(apiHandler)

	a.mu.Lock()
	a.handler = wrapped
	a.mu.Unlock()

	if err := bus.StartRPCServer(wrapped); err != nil {
		log.Printf("[rpc] start error: %v", err)
	}

	a.startTray(aiAPIURL, authURL, natsURL, lucodePort, nc)

	addr := fmt.Sprintf(":%d", lucodePort)
	log.Printf("[desktop] lucode listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, wrapped); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

func loadConfig() desktopConfig {
	path := filepath.Join(defaultDataDir(), "desktop.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return desktopConfig{}
	}
	var cfg desktopConfig
	json.Unmarshal(data, &cfg)
	return cfg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
