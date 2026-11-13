package taskmcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// RunFromEnv is the `mcp-task` subcommand of every binary that starts claude (lucode
// and lucode-runner): an MCP server on stdin/stdout that forwards the lucode tools to
// the hub over NATS. It is configured by the environment the instance wrote into the
// agent's MCP config: LUCODE_SESSION_ID, LUCODE_NATS_URL, LUCODE_NATS_CREDS and
// LUCODE_TOOLS. It exits the process on error.
func RunFromEnv() {
	sessionID := os.Getenv("LUCODE_SESSION_ID")
	natsURL := os.Getenv("LUCODE_NATS_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}

	var nc *nats.Conn
	call := func(tool string, args json.RawMessage) (string, error) {
		if sessionID == "" {
			return "", fmt.Errorf("not running under lucode (no LUCODE_SESSION_ID)")
		}
		if nc == nil {
			opts := []nats.Option{nats.Timeout(10 * time.Second), nats.RetryOnFailedConnect(false)}
			if creds := os.Getenv("LUCODE_NATS_CREDS"); creds != "" {
				opts = append(opts, nats.UserCredentials(creds))
			}
			if ca := os.Getenv("LUCODE_NATS_CA"); ca != "" {
				opts = append(opts, nats.RootCAs(ca))
			}
			c, err := nats.Connect(natsURL, opts...)
			if err != nil {
				return "", fmt.Errorf("cannot reach lucode: %w", err)
			}
			nc = c
		}
		payload, _ := json.Marshal(map[string]any{"tool": tool, "args": args})
		msg, err := nc.Request("lucode.tasktools."+sessionID, payload, 30*time.Second)
		if err != nil {
			return "", fmt.Errorf("lucode did not answer: %w", err)
		}
		var reply struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(msg.Data, &reply); err != nil {
			return "", fmt.Errorf("bad reply from lucode: %w", err)
		}
		if !reply.OK {
			return "", fmt.Errorf("%s", reply.Message)
		}
		return reply.Message, nil
	}

	// LUCODE_TOOLS (comma separated) limits which lucode tools this agent has; unset
	// means all of them and an empty value means none.
	var allowed []string
	if v, ok := os.LookupEnv("LUCODE_TOOLS"); ok {
		allowed = []string{}
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				allowed = append(allowed, name)
			}
		}
	}
	err := RunWithTools(os.Stdin, os.Stdout, call, allowed)
	if nc != nil {
		nc.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp-task:", err)
		os.Exit(1)
	}
}
