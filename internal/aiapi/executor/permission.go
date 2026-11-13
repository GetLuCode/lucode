package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// PermissionTimeout caps how long aiapi will block waiting for the
// orchestrator's permission decision. Matches the legacy
// lucodehook/main.go timeout.
const PermissionTimeout = 4 * time.Minute

// PermissionClient asks the orchestrator for permission before each
// tool execution. The orchestrator resolves the decision via its
// PermissionStore (settings.json hierarchy + session rules) or by
// prompting the user. The client blocks on a NATS request/reply.
//
// A nil *PermissionClient is a valid "no permission gate" — every
// call returns ("allow", "", nil) so unit tests can run without NATS.
type PermissionClient struct {
	NC        *nats.Conn
	SessionID string
}

// Request blocks until the orchestrator replies with a decision. The
// returned (decision, reason) pair is one of:
//
//	("allow", "<rule text>")
//	("deny",  "<rule text or error>")
//
// On NATS / timeout error the client fails closed: the caller is
// expected to treat the empty decision as a deny.
func (p *PermissionClient) Request(ctx context.Context, toolUseID, toolName string, input json.RawMessage, workDir string) (decision, reason string, err error) {
	if p == nil || p.NC == nil {
		// No permission gate configured — fail open (legacy behaviour).
		return "allow", "", nil
	}
	subject := fmt.Sprintf("lucode.permissions.%s", p.SessionID)
	payload, _ := json.Marshal(map[string]any{
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  input,
		"work_dir":    workDir,
	})

	reqCtx, cancel := context.WithTimeout(ctx, PermissionTimeout)
	defer cancel()

	msg, err := p.NC.RequestWithContext(reqCtx, subject, payload)
	if err != nil {
		log.Printf("[permission] NATS request failed: %v", err)
		return "", "", err
	}

	var reply struct {
		ToolUseID string `json:"tool_use_id"`
		Decision  string `json:"decision"`
		Reason    string `json:"reason"`
	}
	if jerr := json.Unmarshal(msg.Data, &reply); jerr != nil {
		log.Printf("[permission] reply parse: %v", jerr)
		return "", "", jerr
	}
	if reply.Decision != "allow" && reply.Decision != "deny" {
		log.Printf("[permission] orchestrator returned decision=%q — fail closed", reply.Decision)
		return "deny", "orchestrator returned invalid decision", nil
	}
	return reply.Decision, reply.Reason, nil
}
