// Package aiapi runs the AI executor service (Claude OAuth + MiniMax turns, versioning) inside the hub
// process, with no port of its own. It used to be a separate program; the hub talks to it through the
// inproc://aiapi URL and serves its browser routes under its own /api.
package aiapi

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/nats-io/nats.go"

	"claude-orchestrator/internal/aiapi/api"
	"claude-orchestrator/internal/aiapi/config"
	"claude-orchestrator/internal/aiapi/executor"
	"claude-orchestrator/internal/aiapi/natsbridge"
	"claude-orchestrator/internal/inproc"
)

// Options are the flags the hub passes through. Empty fields fall back to ~/.aicontrol/config.json.
type Options struct {
	DataDir      string // the hub's data dir: where the NATS bridge keeps its instance id
	MiniMaxKey   string
	MiniMaxFile  string
	MiniMaxBase  string
	MiniMaxModel string
}

// URL is the base URL the hub uses to reach the embedded service.
const URL = "inproc://aiapi"

// Start configures the executor and returns the service's HTTP handler; it also answers URL.
func Start(o Options, nc *nats.Conn) (http.Handler, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	key := firstNonEmpty(o.MiniMaxKey, cfg.MiniMaxAPIKey)
	keyFile := firstNonEmpty(o.MiniMaxFile, cfg.MiniMaxAPIKeyFile)
	if key == "" && keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read minimax key file %s: %w", keyFile, err)
		}
		key = strings.TrimSpace(string(b))
	}
	if cfg.ClaudeCredentialsPath != "" {
		executor.SetClaudeCredentialsPath(cfg.ClaudeCredentialsPath)
	}
	if key == "" && !executor.ClaudeCredentialsAvailable() {
		return nil, fmt.Errorf("no MiniMax API key (--minimax-api-key, --minimax-api-key-file or minimaxApiKey in %s) and no Claude credentials", config.Path())
	}
	model := firstNonEmpty(o.MiniMaxModel, cfg.MiniMaxDefaultModel, "MiniMax-M2.7")
	if key == "" {
		log.Printf("[aiapi] no MiniMax key — only Claude models (OAuth, %s) will work", executor.ClaudeCredentialsPath())
	}
	client := executor.NewClient(key, firstNonEmpty(o.MiniMaxBase, cfg.MiniMaxBaseURL))
	executor.SetClient(client)
	log.Printf("[aiapi] minimax ready — base=%s model=%s", client.BaseURL, model)

	server := api.NewServer(model)
	if nc != nil {
		server.SetNATSConn(nc)
		if err := natsbridge.Start(nc, server, o.DataDir); err != nil {
			return nil, fmt.Errorf("nats bridge: %w", err)
		}
	}
	inproc.Register("aiapi", server)
	return server, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
