# Backend

## Entry Point — `cmd/orchestrator/main.go`

```
./orchestrator [-port 8090] [-data ~/.claude-orchestrator]
```

Startup order:
1. `db.Init(path)` — open SQLite, run migrations
2. `ws.NewHub()` — create WS hub
3. `session.NewManager(db, hub, backendWSURL)` — wires hub callbacks
4. `api.New(db, hub, manager)` — create HTTP handler
5. `http.ListenAndServe(:8090, api.Wrap(handler))` — CORS middleware wraps all routes

## HTTP API — `internal/api/handler.go`

```
POST   /api/projects                    create project
GET    /api/projects                    list projects
DELETE /api/projects/{id}               delete project (cascades to sessions/messages)

POST   /api/projects/{pid}/sessions     create session + optional first turn (⭐ now with image)
GET    /api/projects/{pid}/sessions     list sessions for project
DELETE /api/sessions/{id}               delete session
PATCH  /api/sessions/{id}               ⭐ rename session

POST   /api/sessions/{id}/turn          send next message (⭐ with image, effort, thinking)
POST   /api/sessions/{id}/fork          fork session to new branch

GET    /api/sessions/{id}/messages      paginated messages (50 default, cursor-based)

GET    /api/ideas?scope=                list ideas for scope (project_id or 'global') ⭐
POST   /api/ideas                       create idea ⭐
PATCH  /api/ideas/{id}                  toggle idea done status ⭐
DELETE /api/ideas/{id}                  delete idea ⭐

GET    /ws?role=client&session_id=<id>  browser UI WebSocket
GET    /ws?role=hook&session_id=<id>    hook binary WebSocket

GET    /api/health                      200 OK
```

**createSession body** ⭐:
```json
{
  "message": "initial message text",
  "name": "auto-generated or custom",
  "model": "haiku|sonnet|opus",
  "effort": "auto|low|medium|high|max",
  "thinking": "adaptive|enabled|disabled",
  "image": { "base64": "...", "mediaType": "image/png" }
}
```

**sendTurn body** ⭐:
```json
{
  "message": "message text",
  "model": "haiku|sonnet|opus",
  "effort": "auto|low|medium|high|max",
  "thinking": "adaptive|enabled|disabled",
  "image": { "base64": "...", "mediaType": "image/png" }
}
```

**Ideas endpoints** ⭐:
```
GET  /api/ideas?scope=<project_id|global>
  → 200: [{ "id": "...", "scope": "...", "text": "...", "done": false, "created_at": "..." }]

POST /api/ideas
  body: { "scope": "...", "text": "..." }
  → 201: { "id": "...", "scope": "...", "text": "...", "done": false, "created_at": "..." }

PATCH /api/ideas/{id}
  body: { "done": true|false }
  → 204

DELETE /api/ideas/{id}
  → 204
```

### Resume logic (`sendTurn`)

```go
Resume: sess.MessageCount > 0
```

- First turn (`MessageCount == 0`): uses `--session-id=<id>` (new claude session)
- Subsequent turns (`MessageCount > 0`): uses `--resume=<id>` (continue existing session)

### On client WS connect

Re-broadcasts all pending hooks for the session (reconnect recovery):
```go
hooks, _ := dbpkg.ListPendingHooks(h.db, sessID)
for _, ph := range hooks {
    hub.BroadcastToRole(sessID, RoleClient, hook_pending{...})
}
```

## Session Manager — `internal/session/manager.go`

`NewManager` wires three hub callbacks:
- `hub.OnHookRequest` → `m.onHookRequest(sessID, toolUseID, toolName, toolInput)`
- `hub.OnHookRespond` → `m.onHookRespond(sessID, toolUseID, decision, reason)`
- `hub.OnHookDisconnect` → `m.onHookDisconnect(sessID, toolUseID)`

### `StartTurn(opts TurnOptions) error`

Non-blocking. Returns `ErrSessionBusy` if `procs[sessID]` already active.

1. Updates session status → `running` in DB
2. **Persists user message to DB** ⭐
   - If image attached: serialize as JSON content blocks
   - If no image: plain text
3. Broadcasts `session_status: running`
4. Calls `spawnClaude(ctx, opts, backendWSURL)` — creates process, writes stdin, returns pipes
5. Stores `ProcessState` in `procs` map
6. Launches `go streamOutput(sr)` — returns immediately

**User message storage** ⭐:
- Plain text: `content = "message text"`
- With image:
  ```json
  content = JSON.stringify([
    { "type": "text", "text": "message text" },
    { "type": "image", "source": {
        "type": "base64",
        "media_type": "image/png",
        "data": "base64string..."
    }}
  ])
  ```
  
Same format persisted regardless of whether turn is new vs resume.

### `streamOutput` goroutine

Reads stdout line by line with `bufio.Scanner` (1 MB buffer). Uses `turnAccum` to accumulate streaming content:

```
line → ParseLine() → ParsedEvent
  if stream_event:
    run through turnAccum.process() → returns finalized Message if complete
    persist finalized messages to DB (text on message_stop, thinking on content_block_stop)
  broadcast to all WS clients for session
  if isResult → broadcast turn_complete, set idle, delete from procs
```

`turnAccum` accumulates `text_delta` and `thinking_delta` chunks and emits clean text messages, not raw stream-event JSON. This ensures stored messages are renderable without needing post-processing.

On scanner error or process exit without result event → broadcasts `error`, sets status `error`.

### `onHookRequest(sessID, toolUseID, toolName, toolInput)`

1. Insert into `pending_hooks` (composite PK: session_id + tool_use_id)
2. Update session status → `waiting_hook`
3. Broadcast `hook_pending` to `RoleClient` only (NOT to hook binary)

### `onHookRespond(sessID, toolUseID, decision, reason)`

1. Delete from `pending_hooks`
2. If no remaining hooks → update status → `running`
3. `SendToHookClient(sessID, toolUseID, hook_response)` — routes to specific WS connection
4. If not found (hook timed out): broadcast `info` message to client
5. Broadcast `hook_resolved` to `RoleClient`

### `onHookDisconnect(sessID, toolUseID)`

Called when hook binary WS disconnects. Uses `DeletePendingHook` return value (rows affected):
- `deleted == 0`: user already responded → no-op
- `deleted > 0`: hook timed out before user responded → broadcast `hook_resolved` + `info` message

## CLI Process — `internal/session/process.go`

### `spawnClaude(ctx, opts, backendWSURL)`

```go
// New session:
claude -p --verbose --output-format=stream-json --include-hook-events
       --include-partial-messages --input-format=stream-json
       --permission-mode=default
       --model=<model> --session-id=<id> [--name=<name>]

// Resume:
claude -p --verbose --output-format=stream-json --include-hook-events
       --include-partial-messages --input-format=stream-json
       --permission-mode=default
       --model=<model> --resume=<id>

// Fork:
claude -p ... --fork-session --resume=<forkFrom> --session-id=<newId>
```

**Working directory**: `cmd.Dir = opts.WorkDirs[0]` (primary dir). Extra dirs via `--add-dir`.

**Environment variables injected**:
```
ORCHESTRATOR_SESSION_ID=<sessID>
ORCHESTRATOR_WS_URL=ws://localhost:8090
CLAUDE_CONFIG_DIR=<proj.ClaudeConfigDir>  (if set)
```

**Stdin** ⭐: writes user message in stream-json format:
- **Plain text**:
  ```json
  {"type":"user","message":{"role":"user","content":"text"}}\n
  ```
- **With image**:
  ```json
  {"type":"user","message":{"role":"user","content":[
    {"type":"text","text":"message text"},
    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
  ]}}\n
  ```

Then closes stdin.

**Stderr**: piped to `log.Printf` in a background goroutine.

## Stream Parser — `internal/session/parser.go`

`ParseLine(sessID, line) → (ParsedEvent, isResult, error)`

| Outer `type` | Action |
|---|---|
| `system` subtype `init` | Return `session_status: running` |
| `system` (other) | Skip |
| `assistant`, `user` | **Skip** — partial snapshots, redundant with stream events |
| `result` | Return `turn_complete`, `isResult=true` |
| anything else | Pass through as `stream_event` with raw line as `event` field |

The `rate_limit_event`, `thinking`, and `stream_event` outer types all pass through as `stream_event`.

## Process Context & Lifecycle

```go
ctx, cancel = context.WithTimeout(context.Background(), 30*time.Minute)
```

- 30-minute hard limit per turn
- `cancel()` called in `streamOutput` defer (always)
- `cmd.Wait()` called in defer to reap the process
- Multiple sessions run fully concurrently — each has own goroutine + process
