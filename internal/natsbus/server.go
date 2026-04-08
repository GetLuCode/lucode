// Package natsbus starts an embedded NATS server with WebSocket support
// and exposes a connected client for internal pub/sub.
package natsbus

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Server wraps an embedded NATS server and a connected internal client.
type Server struct {
	ns   *server.Server
	Conn *nats.Conn
}

// Options configures the embedded NATS server.
type Options struct {
	// NATSPort is the TCP port for internal Go client connections (default 5222).
	NATSPort int
	// WSPort is the WebSocket port exposed to browsers and lucodehook (default 5224).
	WSPort int
	// StoreDir is the directory for JetStream persistence (default os.TempDir()+"/nats-lucode").
	StoreDir string
}

// Start launches an embedded NATS server and returns a connected Server.
func Start(opts Options) (*Server, error) {
	if opts.NATSPort == 0 {
		opts.NATSPort = 5222
	}
	if opts.WSPort == 0 {
		opts.WSPort = 5224
	}
	if opts.StoreDir == "" {
		opts.StoreDir = filepath.Join(os.TempDir(), "nats-lucode")
	}

	// Create StoreDir if it doesn't exist
	if err := os.MkdirAll(opts.StoreDir, 0700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	so := &server.Options{
		Host:       "127.0.0.1",
		Port:       opts.NATSPort,
		NoLog:      true,
		NoSigs:     true,
		MaxPayload: 8 * 1024 * 1024,
		JetStream:  true,
		StoreDir:   opts.StoreDir,
		Websocket: server.WebsocketOpts{
			Host:        "0.0.0.0",
			Port:        opts.WSPort,
			NoTLS:       true,
			Compression: false, // Disable for dev debugging (see frames as text in devtools)
		},
	}

	ns, err := server.NewServer(so)
	if err != nil {
		return nil, fmt.Errorf("nats server init: %w", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(10 * time.Second) {
		return nil, fmt.Errorf("nats server did not become ready in 10s")
	}
	log.Printf("[nats] ready — tcp://127.0.0.1:%d  ws://0.0.0.0:%d", opts.NATSPort, opts.WSPort)

	nc, err := nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", opts.NATSPort))
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("nats internal connect: %w", err)
	}

	return &Server{ns: ns, Conn: nc}, nil
}

// Shutdown gracefully stops the server and drains the internal connection.
func (s *Server) Shutdown() {
	_ = s.Conn.Drain()
	s.ns.Shutdown()
}
