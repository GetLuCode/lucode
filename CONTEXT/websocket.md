# WebSocket Protocol

## Hub — `internal/ws/hub.go`

Session-keyed hub tracking all WS clients across two roles.

### Client struct

```go
type Client struct {
    conn      *websocket.Conn
    send      chan []byte   // buffered 256; drop on full
    SessID    string
    Role      Role          // "client" | "hook"
    ToolUseID string        // set when hook sends hook_request
    hub       *Hub
}
```

### Hub struct

```go
type Hub struct {
    mu      sync.RWMutex
    clients map[string]map[*Client]struct{}  // sessionID → set of clients

    OnHookRequest    func(sessID, toolUseID, toolName string, toolInput json.RawMessage)
    OnHookRespond    func(sessID, toolUseID, decision, reason string)
    OnHookDisconnect func(sessID, toolUseID string)
}
```

### Broadcast methods

| Method | Sends to |
|---|---|
| `Broadcast(sessID, event)` | All clients for session (both roles) |
| `BroadcastToRole(sessID, role, event)` | Only clients with matching role |
| `SendToHookClient(sessID, toolUseID, event) bool` | Specific hook client by tool_use_id; returns false if not found |

### Message routing in `readPump`

```
hook binary sends hook_request →
    c.ToolUseID = msg.ToolUseID   (under hub.mu.Lock)
    hub.OnHookRequest(sessID, toolUseID, toolName, toolInput)

browser sends hook_respond →
    hub.OnHookRespond(sessID, toolUseID, decision, reason)
```

### Disconnect handling (`readPump` defer)

```go
defer func() {
    toolUseID := c.ToolUseID
    hub.unsubscribe(c)
    close(c.send)
    c.conn.Close()
    if c.Role == RoleHook && toolUseID != "" {
        hub.OnHookDisconnect(c.SessID, toolUseID)
    }
}()
```

## WebSocket URLs

| URL | Role | Who connects |
|---|---|---|
| `ws://localhost:8090/ws?role=client&session_id=<id>` | `client` | Browser UI |
| `ws://localhost:8090/ws?role=hook&session_id=<id>` | `hook` | Hook binary (one per tool call) |

## Events: Backend → Frontend (`role=client`)

All events are JSON objects with at minimum a `type` and `session_id` field.

### `session_status`
```json
{ "type": "session_status", "session_id": "...", "status": "running|idle|waiting_hook|error" }
```
Also updates `sessions[].status` in the DB.

### `stream_event`
```json
{ "type": "stream_event", "session_id": "...", "event": <raw claude stdout line> }
```
The `event` field is the verbatim JSON line from claude's stdout. The outer `type` within `event` is one of: `stream_event`, `rate_limit_event`, `thinking`, etc.

Inner `stream_event` contains Anthropic streaming events:
- `message_start` — begin of assistant message, includes input token usage
- `content_block_start` — new content block; `content_block.type` = `text` | `thinking`
- `content_block_delta` — streaming chunk; `delta.type` = `text_delta` | `thinking_delta`
- `content_block_stop` — block finished
- `message_delta` — output token count
- `message_stop` — end of assistant message

### `tool_execution`
```json
{
  "type": "tool_execution",
  "session_id": "...",
  "tool_name": "Bash",
  "tool_use_id": "toolu_...",
  "tool_input": { "command": "..." },
  "timestamp": "2026-04-12T...",
  "message_id": "..."
}
```
Tool is starting execution. Shows in chat with loading indicator (spinning hourglass). Role in DB is `tool_execution`.

### `tool_execution_complete`
```json
{
  "type": "tool_execution_complete",
  "session_id": "...",
  "tool_use_id": "toolu_...",
  "tool_response": { "stdout": "...", "stderr": "...", "exit_code": 0 },
  "message_id": "..."
}
```
Tool execution completed. Updates the existing `tool_execution` message with the response (result, diff for Edit, etc.).

### `hook_pending`
```json
{
  "type": "hook_pending",
  "session_id": "...",
  "tool_use_id": "toolu_...",
  "tool_name": "Bash",
  "tool_input": { "command": "...", "description": "..." }
}
```
Triggers the approval modal for `PermissionRequest` events. Multiple may be in-flight simultaneously (parallel tool calls).

### `hook_resolved`
```json
{ "type": "hook_resolved", "session_id": "...", "tool_use_id": "toolu_...", "decision": "approve|deny|timeout" }
```
Removes the specific hook from the pending queue. `decision: "timeout"` means the hook binary disconnected before user responded.

### `turn_complete`
```json
{ "type": "turn_complete", "session_id": "...", "result_subtype": "success|error_during_execution", "result_text": "..." }
```

### `error`
```json
{ "type": "error", "session_id": "...", "message": "..." }
```
Sets session status to `error`. Adds an error message to the chat.

### `info`
```json
{ "type": "info", "session_id": "...", "message": "..." }
```
Non-fatal notification. Does NOT change session status. Adds a system message to the chat. Used for hook timeout notifications.

## Events: Frontend → Backend (`role=client`)

### `hook_respond`
```json
{
  "type": "hook_respond",
  "session_id": "...",
  "tool_use_id": "toolu_...",
  "decision": "approve|deny",
  "reason": ""
}
```
Must include `tool_use_id` — routes to the specific hook binary WS connection.

## Events: Hook Binary → Backend (`role=hook`)

### `hook_request`
```json
{
  "type": "hook_request",
  "tool_use_id": "toolu_...",
  "tool_name": "Bash",
  "tool_input": { ... }
}
```

## Events: Backend → Hook Binary (`role=hook`)

### `hook_response`
```json
{
  "type": "hook_response",
  "tool_use_id": "toolu_...",
  "decision": "approve|deny",
  "reason": ""
}
```
Sent via `SendToHookClient` — only to the specific connection that sent the matching `hook_request`.
