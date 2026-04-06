package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
)

var logger *log.Logger

func initLogger() *os.File {
	dir := filepath.Dir(os.Args[0])
	ts := time.Now().Format("2006-01-02 15-04-05")
	path := filepath.Join(dir, fmt.Sprintf("log-%s.txt", ts))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Fallback to /tmp
		f, _ = os.CreateTemp("", "orchestrator-hook-*.log")
	}
	logger = log.New(f, "", log.Ltime|log.Lmicroseconds)
	return f
}

func allow(reason string) {
	logger.Printf("EXIT allow — %s", reason)
	os.Exit(0)
}

func deny(reason string) {
	logger.Printf("EXIT deny — %s", reason)
	fmt.Fprintln(os.Stderr, reason)
	os.Exit(2)
}

func main() {
	logFile := initLogger()
	defer logFile.Close()

	logger.Printf("=== hook invoked ===")
	logger.Printf("args: %v", os.Args)

	sessionID := os.Getenv("ORCHESTRATOR_SESSION_ID")
	wsURL := os.Getenv("ORCHESTRATOR_WS_URL")

	logger.Printf("ORCHESTRATOR_SESSION_ID=%q", sessionID)
	logger.Printf("ORCHESTRATOR_WS_URL=%q", wsURL)

	if sessionID == "" {
		allow("no ORCHESTRATOR_SESSION_ID — not under orchestrator")
	}
	if wsURL == "" {
		wsURL = "ws://localhost:8090"
		logger.Printf("using default wsURL=%s", wsURL)
	}

	// Read tool info JSON from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		allow(fmt.Sprintf("stdin read error: %v", err))
	}
	if len(input) == 0 {
		allow("empty stdin")
	}
	logger.Printf("stdin: %s", string(input))

	var hookEvent map[string]json.RawMessage
	if err := json.Unmarshal(input, &hookEvent); err != nil {
		allow(fmt.Sprintf("json parse error: %v", err))
	}

	toolName := jsonStr(hookEvent["tool_name"])
	toolUseID := jsonStr(hookEvent["tool_use_id"])
	logger.Printf("tool_name=%q tool_use_id=%q", toolName, toolUseID)
	if toolName == "" {
		allow("no tool_name in event")
	}

	connURL := fmt.Sprintf("%s/ws?role=hook&session_id=%s", wsURL, sessionID)
	logger.Printf("connecting to %s", connURL)

	conn, resp, err := websocket.DefaultDialer.Dial(connURL, nil)
	if err != nil {
		if resp != nil {
			logger.Printf("ws dial error (HTTP %d): %v", resp.StatusCode, err)
		} else {
			logger.Printf("ws dial error: %v", err)
		}
		allow("backend not reachable")
	}
	defer conn.Close()
	logger.Printf("ws connected")

	// Send hook_request
	req := map[string]any{
		"type":        "hook_request",
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  hookEvent["tool_input"],
	}
	if err := conn.WriteJSON(req); err != nil {
		logger.Printf("write hook_request error: %v", err)
		allow("write error")
	}
	logger.Printf("hook_request sent, waiting for response (120s timeout)...")

	// Wait for hook_response — loop to skip any other messages (e.g. hook_pending broadcast)
	logger.Printf("waiting for hook_response (4min timeout)...")
	conn.SetReadDeadline(time.Now().Add(4 * time.Minute))
	for {
		var msg struct {
			Type     string `json:"type"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			logger.Printf("read error: %v", err)
			deny("approval timed out — tool blocked")
		}
		logger.Printf("received: type=%q decision=%q reason=%q", msg.Type, msg.Decision, msg.Reason)

		if msg.Type != "hook_response" {
			logger.Printf("ignoring non-hook_response message (type=%q)", msg.Type)
			// Reset deadline on each ignored message
			conn.SetReadDeadline(time.Now().Add(4 * time.Minute))
			continue
		}

		if msg.Decision == "deny" {
			reason := msg.Reason
			if reason == "" {
				reason = "Blocked by user"
			}
			deny(reason)
		}
		break
	}

	allow("user approved")
}

func jsonStr(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
