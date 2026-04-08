package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsHub manages all connected browser WebSocket clients.
type wsHub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]struct{}
}

var hub = &wsHub{
	clients: make(map[*websocket.Conn]struct{}),
}

func (h *wsHub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *wsHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *wsHub) broadcast(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.WriteMessage(websocket.TextMessage, data)
	}
}

func (h *wsHub) send(c *websocket.Conn, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c.WriteMessage(websocket.TextMessage, data)
}

// BroadcastToWS sends an event to all connected browser clients.
// Called by the Bus when it receives NATS events.
func BroadcastToWS(data []byte) {
	hub.broadcast(data)
}

// globalWS is the single WebSocket endpoint for all browser connections.
func (h *Handler) globalWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	hub.add(conn)
	defer hub.remove(conn)

	log.Printf("[ws] client connected, total=%d", len(hub.clients))

	// Send all current pending hooks on connect so client can restore state
	allHooks := h.bus.GetAllPendingHooks()
	for _, hook := range allHooks {
		event := map[string]any{
			"type":        "hook_pending",
			"session_id":  hook.SessionID,
			"tool_use_id": hook.ToolUseID,
			"tool_name":   hook.ToolName,
			"tool_input":  hook.ToolInput,
			"created_at":  hook.CreatedAt,
		}
		if data, err := json.Marshal(event); err == nil {
			hub.send(conn, data)
		}
	}

	// Read messages from browser
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[ws] client disconnected: %v", err)
			return
		}
		var cmd struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			ToolUseID string `json:"tool_use_id"`
			Decision  string `json:"decision"`
			Reason    string `json:"reason"`
		}
		if err := json.Unmarshal(data, &cmd); err != nil {
			continue
		}
		switch cmd.Type {
		case "hook_respond":
			if h.bus.OnHookRespond != nil {
				log.Printf("[ws] hook_respond sess=%s tool=%s decision=%s", cmd.SessionID, cmd.ToolUseID, cmd.Decision)
				h.bus.OnHookRespond(cmd.SessionID, cmd.ToolUseID, cmd.Decision, cmd.Reason)
			}
		default:
			log.Printf("[ws] unknown cmd type=%s", cmd.Type)
		}
	}
}
