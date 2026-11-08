// Package relayclient is the hub's connection to the webhook relay (github.com/.../lucode-webhook-relay):
// a server with a public name that services such as Telegram post to. The hub dials the relay, never
// the other way round, keeps the websocket open, and hands each webhook that comes down it to the
// plugin that asked for that address. Hub only: a runner never links this package.
package relayclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"claude-orchestrator/internal/plugins"

	"github.com/gorilla/websocket"
)

// Config is ~/.lucode/relay.json (mode 0600, like the other keys there): where the relay is and the
// key of this hub's tenant. The key never leaves the hub, and Status never shows it.
type Config struct {
	URL    string `json:"url"`    // wss://relay.example.com/ws
	Tenant string `json:"tenant"` // for the log only: the relay knows the tenant by the key
	Name   string `json:"name"`
	Key    string `json:"key"`
}

// LoadConfig reads the file. A file that is not there means the hub has no relay, and is no error.
func LoadConfig(path string) (Config, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, false, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return c, true, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Key) == "" {
		return errors.New("the relay key is empty")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return errors.New("the relay url is not an address")
	}
	// the key goes in a header of every connection: only over TLS, or to this machine
	switch u.Scheme {
	case "wss":
	case "ws":
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("the relay url must be wss:// (ws:// is only for this machine)")
		}
	default:
		return errors.New("the relay url must start with wss://")
	}
	return nil
}

// Timing. Variables, so a test need not wait for the real ones.
var (
	minBackoff     = time.Second
	maxBackoff     = 30 * time.Second
	refusedBackoff = time.Minute // the relay said 401: the key is wrong, no use asking often
	stableAfter    = 30 * time.Second
	requestTimeout = 15 * time.Second
	readDeadline   = 90 * time.Second // the relay pings every 30 s
)

const (
	eventQueue = 1024
	maxFrame   = 4 << 20 // a 1 MB body, escaped
)

// frame is every message of the relay's protocol: only the fields an op uses are filled.
type frame struct {
	Op          string   `json:"op"`
	Req         string   `json:"req,omitempty"`
	Seq         uint64   `json:"seq,omitempty"`
	Endpoint    string   `json:"endpoint,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	ReceivedAt  int64    `json:"received_at,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Body        string   `json:"body,omitempty"`
	ID          string   `json:"id,omitempty"`
	Label       string   `json:"label,omitempty"`
	URL         string   `json:"url,omitempty"`
	Secret      string   `json:"secret,omitempty"`
	Message     string   `json:"message,omitempty"`
	Tenant      string   `json:"tenant,omitempty"`
	Pending     int      `json:"pending,omitempty"`
	Providers   []string `json:"providers,omitempty"`
}

// Client implements plugins.RelayClient.
type Client struct {
	cfg        Config
	configured bool

	handler atomic.Value // func(context.Context, plugins.RelayEvent)

	connected atomic.Bool

	mu      sync.Mutex
	sess    *session
	pending map[string]chan frame
	nextReq uint64
}

var _ plugins.RelayClient = (*Client)(nil)

// New makes a client. With ok false (no config) it never connects and says so when asked for anything.
func New(cfg Config, ok bool) *Client {
	return &Client{cfg: cfg, configured: ok, pending: map[string]chan frame{}}
}

func (c *Client) SetHandler(fn func(context.Context, plugins.RelayEvent)) { c.handler.Store(fn) }

func (c *Client) Status() plugins.RelayStatus {
	if !c.configured {
		return plugins.RelayStatus{}
	}
	return plugins.RelayStatus{Configured: true, Connected: c.connected.Load(), URL: c.cfg.URL}
}

// session is one websocket. Everything it writes goes through write, one at a time.
type session struct {
	conn *websocket.Conn
	wmu  sync.Mutex
	done chan struct{}
}

func (s *session) write(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(v)
}

// Run keeps the connection up until ctx ends: it dials, serves, and dials again with a growing
// pause when the relay goes away. It returns at once when the hub has no relay.
func (c *Client) Run(ctx context.Context) {
	if !c.configured {
		return
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := c.serve(ctx)
		c.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		wait := backoff
		var refused *refusedError
		switch {
		case errors.As(err, &refused):
			log.Printf("[relay] %v: check the key in relay.json", err)
			wait = refusedBackoff
		case err != nil:
			log.Printf("[relay] %v", err)
		}
		if time.Since(start) >= stableAfter {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(wait)):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func jitter(d time.Duration) time.Duration {
	return d - d/5 + time.Duration(rand.Int63n(int64(d/5)+1))*2 // between 80% and 120%
}

type refusedError struct{ status int }

func (e *refusedError) Error() string {
	return fmt.Sprintf("the relay refused this hub (%d)", e.status)
}

func (c *Client) serve(ctx context.Context) error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.cfg.Key)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, res, err := d.DialContext(ctx, c.cfg.URL, h)
	if err != nil {
		if res != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
			return &refusedError{res.StatusCode}
		}
		return fmt.Errorf("could not reach the relay: %w", scrubErr(err, c.cfg.Key))
	}
	conn.SetReadLimit(maxFrame)
	extend := func() { _ = conn.SetReadDeadline(time.Now().Add(readDeadline)) }
	extend()
	conn.SetPingHandler(func(m string) error {
		extend()
		return conn.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(10*time.Second))
	})

	s := &session{conn: conn, done: make(chan struct{})}
	c.mu.Lock()
	c.sess = s
	c.mu.Unlock()
	events := make(chan plugins.RelayEvent, eventQueue)
	var wg sync.WaitGroup
	defer func() {
		close(s.done)
		_ = conn.Close()
		c.mu.Lock()
		if c.sess == s {
			c.sess = nil
		}
		for req, ch := range c.pending {
			close(ch) // a waiting request learns the connection is gone
			delete(c.pending, req)
		}
		c.mu.Unlock()
		close(events)
		wg.Wait()
	}()
	// ends the read when the hub is shutting down
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-s.done:
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.work(ctx, s, events)
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("the relay connection ended: %w", scrubErr(err, c.cfg.Key))
		}
		extend()
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.Op {
		case "hello":
			c.connected.Store(true)
			log.Printf("[relay] connected as %s (%d waiting)", f.Tenant, f.Pending)
		case "event":
			select {
			case events <- plugins.RelayEvent{Seq: f.Seq, Endpoint: f.Endpoint, Provider: f.Provider, ReceivedAt: f.ReceivedAt, ContentType: f.ContentType, Body: f.Body}:
			case <-ctx.Done():
				return nil
			}
		default:
			c.resolve(f)
		}
	}
}

func scrubErr(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}

// work hands the events of one connection to the plugins, in order, and acknowledges each when its
// handler has returned. An event whose ack never gets there comes again on the next connection: a
// handler may see one twice, and a plugin that cares tells them apart by what the service put in it.
func (c *Client) work(ctx context.Context, s *session, events <-chan plugins.RelayEvent) {
	for ev := range events {
		fn, _ := c.handler.Load().(func(context.Context, plugins.RelayEvent))
		if fn == nil {
			continue // nobody to give it to yet: not acknowledged, so the relay keeps it
		}
		c.handle(ctx, fn, ev)
		if ctx.Err() != nil {
			return // the hub is going down: let the relay keep it
		}
		if err := s.write(frame{Op: "ack", Seq: ev.Seq}); err != nil {
			return
		}
	}
}

func (c *Client) handle(ctx context.Context, fn func(context.Context, plugins.RelayEvent), ev plugins.RelayEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[relay] the handler of a webhook panicked: %v", r) // acknowledged all the same: it would panic again
		}
	}()
	fn(ctx, ev)
}

// resolve gives an answer to the request waiting for it.
func (c *Client) resolve(f frame) {
	if f.Req == "" {
		if f.Op == "error" {
			log.Printf("[relay] the relay said: %s", f.Message)
		}
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[f.Req]
	delete(c.pending, f.Req)
	c.mu.Unlock()
	if ok {
		ch <- f // buffered: the waiter may be gone
	}
}

// ErrNotConnected is what a request gets while the hub has no live connection to the relay.
var ErrNotConnected = errors.New("the webhook relay is not connected")

func (c *Client) request(ctx context.Context, f frame) (frame, error) {
	if !c.configured {
		return frame{}, errors.New("this hub has no webhook relay: put its address and key in relay.json in the lucode data folder")
	}
	c.mu.Lock()
	s := c.sess
	if s == nil || !c.connected.Load() {
		c.mu.Unlock()
		return frame{}, ErrNotConnected
	}
	c.nextReq++
	f.Req = fmt.Sprintf("r%d", c.nextReq)
	ch := make(chan frame, 1)
	c.pending[f.Req] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, f.Req)
		c.mu.Unlock()
	}
	if err := s.write(f); err != nil {
		forget()
		return frame{}, ErrNotConnected
	}
	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	select {
	case ans, ok := <-ch:
		if !ok {
			return frame{}, ErrNotConnected
		}
		if ans.Op == "error" {
			return frame{}, errors.New("the webhook relay: " + ans.Message)
		}
		return ans, nil
	case <-timer.C:
		forget()
		return frame{}, errors.New("the webhook relay did not answer in time")
	case <-ctx.Done():
		forget()
		return frame{}, ctx.Err()
	}
}

// CreateEndpoint asks the relay for a new address. The secret in the answer is shown once.
func (c *Client) CreateEndpoint(ctx context.Context, provider, label string) (plugins.RelayEndpoint, error) {
	ans, err := c.request(ctx, frame{Op: "create_endpoint", Provider: provider, Label: label})
	if err != nil {
		return plugins.RelayEndpoint{}, err
	}
	if ans.Op != "endpoint" || ans.ID == "" || ans.URL == "" || ans.Secret == "" {
		return plugins.RelayEndpoint{}, errors.New("the webhook relay gave an answer this hub does not understand")
	}
	return plugins.RelayEndpoint{ID: ans.ID, Provider: ans.Provider, Label: ans.Label, URL: ans.URL, Secret: ans.Secret}, nil
}

// DeleteEndpoint gives an address back. One the relay no longer has is as good as deleted.
func (c *Client) DeleteEndpoint(ctx context.Context, id string) error {
	_, err := c.request(ctx, frame{Op: "delete_endpoint", ID: id})
	if err != nil && strings.HasSuffix(err.Error(), "no such endpoint") {
		return nil
	}
	return err
}
