package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type Role string

const (
	RoleClient Role = "client"
	RoleHook   Role = "hook"
)

type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	SessID string
	Role   Role
	hub    *Hub
}

type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*Client]struct{} // sessionID → clients

	// OnHookRequest is called when a hook binary sends hook_request.
	OnHookRequest func(sessID string, toolName string, toolInput json.RawMessage)
	// OnHookRespond is called when the browser sends hook_respond.
	OnHookRespond func(sessID string, decision string, reason string)
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[string]map[*Client]struct{}),
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func (h *Hub) Upgrade(w http.ResponseWriter, r *http.Request, sessID string, role Role) (*Client, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:   conn,
		send:   make(chan []byte, 256),
		SessID: sessID,
		Role:   role,
		hub:    h,
	}
	h.subscribe(c)
	go c.writePump()
	go c.readPump()
	return c, nil
}

func (h *Hub) subscribe(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.SessID] == nil {
		h.clients[c.SessID] = make(map[*Client]struct{})
	}
	h.clients[c.SessID][c] = struct{}{}
}

func (h *Hub) unsubscribe(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.clients[c.SessID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.SessID)
		}
	}
}

// Broadcast sends an event to all clients (both roles) for a session.
func (h *Hub) Broadcast(sessID string, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[sessID] {
		select {
		case c.send <- data:
		default:
			log.Printf("ws: drop message for session %s (buffer full)", sessID)
		}
	}
}

// BroadcastToRole sends to only clients with the given role.
func (h *Hub) BroadcastToRole(sessID string, role Role, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[sessID] {
		if c.Role != role {
			continue
		}
		select {
		case c.send <- data:
		default:
			log.Printf("ws: drop message for %s/%s (buffer full)", sessID, role)
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unsubscribe(c)
		close(c.send)
		c.conn.Close()
	}()

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		var msg struct {
			Type      string          `json:"type"`
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
			Decision  string          `json:"decision"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "hook_request":
			// hook binary → backend → broadcast hook_pending to client role
			if c.Role == RoleHook && c.hub.OnHookRequest != nil {
				c.hub.OnHookRequest(c.SessID, msg.ToolName, msg.ToolInput)
			}
		case "hook_respond":
			// browser → backend → forward hook_response to hook role
			if c.Role == RoleClient && c.hub.OnHookRespond != nil {
				c.hub.OnHookRespond(c.SessID, msg.Decision, msg.Reason)
			}
		}
	}
}
