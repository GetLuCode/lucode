// Package natshub runs the NATS server inside the hub, with no TCP port of its own: the hub talks to it
// in process and runners reach it over a WebSocket on the hub's own HTTP port. nats.go always opens
// the WebSocket at the path "/" (it ignores a path in the URL), so the hub hands any WebSocket upgrade
// of "/" to ServeWS: runners use ws://TOKEN@host:port or wss://TOKEN@host. Only cmd/lucode links this
// package; lucode-runner must not.
package natshub

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Hub is the embedded server and the token every client must present.
type Hub struct {
	Srv   *server.Server
	Token string
}

// Start runs the server with JetStream under dataDir/nats-data. The token is made once and kept in
// dataDir/nats-token (mode 0600): a client without it is refused, so the open endpoint is safe.
func Start(dataDir string) (*Hub, error) {
	token, err := loadOrCreateToken(filepath.Join(dataDir, "nats-token"))
	if err != nil {
		return nil, err
	}
	srv, err := server.NewServer(&server.Options{
		ServerName:    "lucode-hub",
		DontListen:    true,
		JetStream:     true,
		StoreDir:      filepath.Join(dataDir, "nats-data"),
		Authorization: token,
		NoSigs:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("nats server: %w", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("nats server did not become ready")
	}
	return &Hub{Srv: srv, Token: token}, nil
}

// ConnectOptions connect a client in this process.
func (h *Hub) ConnectOptions() []nats.Option {
	return []nats.Option{nats.InProcessServer(h.Srv), nats.Token(h.Token)}
}

// URL is how a client on this machine reaches the hub through its HTTP port.
func (h *Hub) URL(port int) string {
	return fmt.Sprintf("ws://%s@127.0.0.1:%d", h.Token, port)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	CheckOrigin:     func(*http.Request) bool { return true }, // not a browser endpoint; the token decides
}

// IsUpgrade reports whether r is the WebSocket handshake a NATS client sends: an upgrade of "/".
func IsUpgrade(r *http.Request) bool {
	return r.URL.Path == "/" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// ServeWS bridges one WebSocket to one in-process connection: the NATS protocol runs unchanged inside
// the binary frames, as with nats.go's ws:// and wss:// URLs.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn, err := h.Srv.InProcessConn()
	if err != nil {
		ws.Close()
		return
	}
	ws.SetReadLimit(8 << 20)
	go func() {
		defer conn.Close()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()
	defer ws.Close()
	defer conn.Close()
	buf := make([]byte, 64<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Shutdown stops the server.
func (h *Hub) Shutdown() {
	h.Srv.Shutdown()
	h.Srv.WaitForShutdown()
}

func loadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}
