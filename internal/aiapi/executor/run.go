package executor

import (
	"context"
	"log"
)

// globalClient holds the package-wide MiniMax client. Set once at startup via
// SetClient from cmd/server/main.go after loading config.
var globalClient *Client

// SetClient configures the executor with a MiniMax HTTP client. Call exactly
// once at server startup; subsequent calls replace the client.
func SetClient(c *Client) { globalClient = c }

// Run executes a turn on the configured MiniMax client. The HTTP handler and
// the NATS bridge both call this; the per-turn event stream goes to publish.
// perm is the orchestrator's permission gate client; nil means no gate
// (always allow) for backwards compat with tests.
func Run(ctx context.Context, req TurnRequest, publish func(map[string]any), perm *PermissionClient) {
	if globalClient == nil {
		publish(map[string]any{
			"type":   "session_status",
			"status": "error",
			"error":  "executor: MiniMax client not configured (call SetClient at startup)",
		})
		return
	}
	if err := globalClient.Run(ctx, req, publish, perm); err != nil {
		// The client's Run method already publishes the error event and an
		// "idle"/"error" status. Logging here is just for ops visibility.
		log.Printf("[executor] turn error: %v", err)
	}
}
