# Hook Binary

## Purpose

The hook binary is a compiled Go program that Claude Code invokes synchronously on every `PreToolUse` event. It acts as a bridge between the claude CLI and the orchestrator backend: it connects over WebSocket, sends the tool info, waits for the user's approval/denial decision, then exits with 0 (allow) or 2 (deny). Claude blocks until the hook exits.

## Installation

```bash
go build -o hook ./cmd/hook
cp hook ~/.claude/hooks/orchestrator-hook
```

Registered in `~/.claude/settings.json`:
```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "/home/homebox/.claude/hooks/orchestrator-hook" }]
      }
    ]
  }
}
```

## Execution Context

- Claude passes tool info as JSON on the hook's **stdin**
- Hook reads stdin, connects WS, waits for decision, exits
- Exit code 0 = allow, exit code 2 = deny
- All other codes default to allow in claude's behavior
- Claude sets `ORCHESTRATOR_SESSION_ID` and `ORCHESTRATOR_WS_URL` in env (injected by the orchestrator when spawning claude — see `process.go`)

## Stdin Format

Claude writes this JSON on stdin:
```json
{
  "session_id": "...",
  "transcript_path": "/path/to/transcript.jsonl",
  "cwd": "/path/to/working/dir",
  "permission_mode": "bypassPermissions",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "ls -la", "description": "List files" },
  "tool_use_id": "toolu_01Q5iS3HFyLyqPhMxA985Squ"
}
```

Key fields extracted: `tool_name`, `tool_use_id`, `tool_input`.

## Approval Flow

```
Claude PreToolUse
  │
  └─ runs hook binary
       │
       ├─ reads stdin (tool info)
       ├─ connects ws://localhost:8090/ws?role=hook&session_id=<id>
       ├─ sends hook_request { type, tool_use_id, tool_name, tool_input }
       │
       │  Backend receives hook_request:
       │    - stores in pending_hooks DB
       │    - updates session status → waiting_hook
       │    - broadcasts hook_pending to RoleClient (browser)
       │
       ├─ hook enters read loop, ignoring non-hook_response messages
       │  (stream_events arrive while waiting — all ignored)
       │
       │  User clicks Approve/Deny in browser:
       │    - browser sends hook_respond { tool_use_id, decision }
       │    - backend calls SendToHookClient(sessID, toolUseID, hook_response)
       │
       ├─ hook receives hook_response { type: "hook_response", decision }
       │
       ├─ decision == "deny" → exit 2
       └─ decision == "approve" → exit 0
```

## Concurrent Tool Calls

Claude may call multiple tools in parallel (e.g. two Bash commands simultaneously). Each tool call spawns a separate hook binary process, each connecting to the WS as `role=hook` for the same session.

**Routing**: Each hook binary sends its `tool_use_id` in `hook_request`. The hub stores this on the client struct (`c.ToolUseID`). When the browser responds with `hook_respond { tool_use_id }`, `SendToHookClient` finds the exact WS connection by matching `tool_use_id`.

The frontend shows a queue of pending hooks (one modal at a time, cycling through). Each hook_resolved event removes one entry from the queue.

## Timeout Handling

Hook binary timeout: **4 minutes** (`conn.SetReadDeadline(time.Now().Add(4 * time.Minute))`). Reset on each ignored non-hook_response message.

If timeout fires → `deny("approval timed out — tool blocked")` → exits 2 (tool automatically denied).

On hook binary exit:
1. WS connection closes
2. `readPump` defer runs → calls `hub.OnHookDisconnect(sessID, toolUseID)`
3. `onHookDisconnect` checks if hook was still pending (using `DeletePendingHook` rows affected):
   - Already resolved (user responded before timeout) → no-op
   - Still pending (timed out before user responded) → broadcasts `hook_resolved` (modal closes) + `info` message

If user approves after timeout (hook already gone):
- `SendToHookClient` returns false
- Backend broadcasts `info` message: "Hook approval timed out — tool was automatically denied."
- Backend still broadcasts `hook_resolved` to close the modal

## Logging

Hook binary writes logs to `~/.claude/hooks/log-<timestamp>.txt`:
```
17:16:38 === hook invoked ===
17:16:38 args: [/home/homebox/.claude/hooks/orchestrator-hook]
17:16:38 ORCHESTRATOR_SESSION_ID="..."
17:16:38 ORCHESTRATOR_WS_URL="ws://localhost:8090"
17:16:38 stdin: { full JSON }
17:16:38 tool_name="Bash" tool_use_id="toolu_..."
17:16:38 connecting to ws://localhost:8090/ws?role=hook&session_id=...
17:16:38 ws connected
17:16:38 hook_request sent, waiting for response (120s timeout)...
17:16:38 waiting for hook_response (4min timeout)...
17:16:38 received: type="stream_event" decision="" reason=""
17:16:38 ignoring non-hook_response message (type="stream_event")
...
17:16:38 received: type="hook_response" decision="approve" reason=""
17:16:38 EXIT allow — user approved
```

## Safety: No Hook When Not Under Orchestrator

```go
if sessionID == "" {
    allow("no ORCHESTRATOR_SESSION_ID — not under orchestrator")
}
```

When Claude Code runs directly (not via orchestrator), `ORCHESTRATOR_SESSION_ID` is not set → hook exits 0 immediately → all tools allowed without prompting.
