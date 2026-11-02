// Command lucodehook is the tool-approval hook for `claude -p`. Headless mode
// has no permission dialog, so PermissionRequest never fires; PreToolUse does,
// before every tool call. The hook forwards the request to lucode over NATS
// (request/reply on lucode.permissions.<session>) — the same gate aiapi uses —
// and translates the decision into claude's hook protocol.
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

// permissionTimeout matches the aiapi permission client.
const permissionTimeout = 4 * time.Minute

var logger = log.New(io.Discard, "", 0)

func initLogger() *os.File {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".lucode")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "lucodehook.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	logger = log.New(f, "", log.Ltime|log.Lmicroseconds)
	return f
}

// eventName is the hook event being handled, set once stdin is parsed.
var eventName = "PreToolUse"

// allow exits 0, optionally printing the approval JSON for the current event.
func allow(reason string, writeJSON bool) {
	logger.Printf("EXIT allow — %s", reason)
	if writeJSON {
		out := map[string]any{}
		if eventName == "PermissionRequest" {
			out["hookSpecificOutput"] = map[string]any{
				"hookEventName": "PermissionRequest",
				"decision":      map[string]any{"behavior": "allow"},
			}
		} else {
			out["hookSpecificOutput"] = map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "allow",
				"permissionDecisionReason": reason,
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
	os.Exit(0)
}

// deny blocks the tool. PreToolUse takes a JSON decision; PermissionRequest
// uses exit 2 with the reason on stderr.
func deny(reason string) {
	logger.Printf("EXIT deny — %s", reason)
	if eventName == "PreToolUse" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		})
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, reason)
	os.Exit(2)
}

func main() {
	if f := initLogger(); f != nil {
		defer f.Close()
	}

	sessionID := firstEnv("LUCODE_SESSION_ID", "ORCHESTRATOR_SESSION_ID")
	natsURL := firstEnv("LUCODE_NATS_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}
	if sessionID == "" {
		allow("no LUCODE_SESSION_ID — not running under lucode", false)
	}

	input, err := io.ReadAll(os.Stdin)
	if err != nil || len(input) == 0 {
		allow("empty or unreadable stdin", false)
	}
	var ev map[string]json.RawMessage
	if err := json.Unmarshal(input, &ev); err != nil {
		allow("json parse error", false)
	}
	switch name := jsonStr(ev["hook_event_name"]); name {
	case "PreToolUse", "PermissionRequest":
		eventName = name
	default:
		allow("not a tool-approval event — passthrough", false)
	}
	toolName := jsonStr(ev["tool_name"])
	if toolName == "" {
		allow("no tool_name", true)
	}

	opts := []nats.Option{nats.Timeout(10 * time.Second), nats.RetryOnFailedConnect(false)}
	if creds := firstEnv("LUCODE_NATS_CREDS"); creds != "" {
		opts = append(opts, nats.UserCredentials(creds))
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		deny("lucode backend not reachable — tool blocked")
	}
	defer nc.Close()

	payload, _ := json.Marshal(map[string]any{
		"tool_use_id": jsonStr(ev["tool_use_id"]),
		"tool_name":   toolName,
		"tool_input":  ev["tool_input"],
		"work_dir":    jsonStr(ev["cwd"]),
	})
	msg, err := nc.Request("lucode.permissions."+sessionID, payload, permissionTimeout)
	if err != nil {
		deny("approval timed out — tool blocked")
	}
	var reply struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		deny("invalid permission reply — tool blocked")
	}
	switch reply.Decision {
	case "allow", "approve":
		allow("approved: "+reply.Reason, true)
	default:
		reason := reply.Reason
		if reason == "" {
			reason = "Blocked by user"
		}
		deny(reason)
	}
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
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
