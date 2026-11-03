// Package cdp drives a Chromium browser (Microsoft Edge) over the Chrome DevTools
// Protocol: launch it with a debugging port (windowed or headless), then find and
// act on page elements through the accessibility tree first (roles and accessible
// names), falling back to CSS selectors and raw coordinates.
//
// One browser is shared by the whole process (Default). Every method is
// serialized, so the MCP tools and the user-tool sandbox can use it at once.
package cdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const callTimeout = 15 * time.Second

type rpcMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *rpcErr         `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *rpcErr) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Data)
	}
	return e.Message
}

// conn is one websocket to the browser. Tabs are reached on the same socket
// through flattened sessions (a sessionId on each message).
type conn struct {
	ws      *websocket.Conn
	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcMsg
	dead    chan struct{}
	deadErr error
	onEvent func(sessionID, method string, params json.RawMessage)
}

func dial(wsURL string, onEvent func(sessionID, method string, params json.RawMessage)) (*conn, error) {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second, ReadBufferSize: 1 << 16, WriteBufferSize: 1 << 16}
	ws, _, err := d.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(64 << 20) // screenshots and big accessibility trees
	c := &conn{ws: ws, pending: map[int64]chan rpcMsg{}, dead: make(chan struct{}), onEvent: onEvent}
	go c.read()
	return c, nil
}

func (c *conn) read() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.deadErr = err
			close(c.dead)
			c.mu.Unlock()
			return
		}
		var m rpcMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method != "" && c.onEvent != nil {
			c.onEvent(m.SessionID, m.Method, m.Params)
		}
	}
}

func (c *conn) alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

func (c *conn) close() { _ = c.ws.Close() }

// call sends a command (to the browser when session is "") and decodes its result into out.
func (c *conn) call(session, method string, params any, out any, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = callTimeout
	}
	id := c.nextID.Add(1)
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if session != "" {
		req["sessionId"] = session
	}
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	if !c.alive() {
		c.mu.Unlock()
		return errBrowserGone
	}
	c.pending[id] = ch
	c.mu.Unlock()

	c.wmu.Lock()
	err := c.ws.WriteJSON(req)
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-c.dead:
		return errBrowserGone
	case <-time.After(timeout):
		c.forget(id)
		return fmt.Errorf("%s: no answer from the browser in %s", method, timeout)
	}
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

var errBrowserGone = errors.New("the browser connection closed (it was closed or crashed): call browser_launch again")

// isGoneTarget reports whether err says the tab no longer exists.
func isGoneTarget(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Session with given id not found") || strings.Contains(s, "No target with given id") ||
		strings.Contains(s, "Target closed") || strings.Contains(s, "Inspected target navigated or closed")
}
