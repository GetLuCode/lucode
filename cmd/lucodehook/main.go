package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go"
)

var logger *log.Logger

func initLogger() *os.File {
	dir := filepath.Dir(os.Args[0])
	ts := time.Now().Format("2006-01-02 15-04-05")
	path := filepath.Join(dir, fmt.Sprintf("lucodehook-log-%s.txt", ts))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		f, _ = os.CreateTemp("", "lucodehook-*.log")
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

	logger.Printf("=== lucodehook invoked ===")
	logger.Printf("args: %v", os.Args)

	sessionID := firstEnv("LUCODE_SESSION_ID", "ORCHESTRATOR_SESSION_ID")
	// LUCODE_NATS_URL can be a nats:// (TCP) or ws:// URL
	natsURL := firstEnv("LUCODE_NATS_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}

	logger.Printf("LUCODE_SESSION_ID=%q", sessionID)
	logger.Printf("LUCODE_NATS_URL=%q", natsURL)

	if sessionID == "" {
		allow("no LUCODE_SESSION_ID — not running under lucode")
	}

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

	logger.Printf("connecting to NATS %s", natsURL)
	nc, err := nats.Connect(natsURL,
		nats.Timeout(10*time.Second),
		nats.RetryOnFailedConnect(false),
	)
	if err != nil {
		logger.Printf("nats connect error: %v", err)
		allow("backend not reachable")
	}
	defer nc.Close()
	logger.Printf("nats connected")

	// Subscribe to our specific response subject before publishing the request
	// so we don't miss the reply if the server is very fast.
	responseSubj := fmt.Sprintf("hooks.%s.response.%s", sessionID, toolUseID)
	sub, err := nc.SubscribeSync(responseSubj)
	if err != nil {
		logger.Printf("subscribe error: %v", err)
		allow("subscribe error")
	}
	defer sub.Unsubscribe()

	// Publish hook_request
	req := map[string]any{
		"type":        "hook_request",
		"session_id":  sessionID,
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  hookEvent["tool_input"],
	}
	data, _ := json.Marshal(req)
	requestSubj := fmt.Sprintf("hooks.%s.request", sessionID)
	if err := nc.Publish(requestSubj, data); err != nil {
		logger.Printf("publish hook_request error: %v", err)
		allow("publish error")
	}
	if err := nc.Flush(); err != nil {
		logger.Printf("flush error: %v", err)
		allow("flush error")
	}
	logger.Printf("hook_request published to %s, waiting on %s ...", requestSubj, responseSubj)

	// Wait for hook_response (up to 4 minutes)
	msg, err := sub.NextMsg(4 * time.Minute)
	if err != nil {
		logger.Printf("wait response error: %v", err)
		deny("approval timed out — tool blocked")
	}

	var resp struct {
		Type      string `json:"type"`
		Decision  string `json:"decision"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		logger.Printf("response parse error: %v", err)
		deny("response parse error — tool blocked")
	}
	logger.Printf("received: type=%q decision=%q reason=%q", resp.Type, resp.Decision, resp.Reason)

	if resp.Decision == "deny" {
		reason := resp.Reason
		if reason == "" {
			reason = "Blocked by user"
		}
		deny(reason)
	}

	allow("user approved")
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
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
