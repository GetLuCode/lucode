# Database

## Location

`~/.claude-orchestrator/orchestrator.db` (SQLite, WAL mode)

Configured via `--data` flag on server startup. Default: `$HOME/.claude-orchestrator/`.

## Init — `internal/db/db.go`

```go
db.Init(path) → sql.DB
```

1. Opens SQLite with `modernc.org/sqlite` driver
2. `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`
3. Runs `migrate(db)`

## Migration Strategy

`migrate()` runs on every startup:

- `projects`, `sessions`, `messages` — `CREATE TABLE IF NOT EXISTS` (safe, no-op if exists)
- `pending_hooks` — **dropped and recreated every startup** (ephemeral; any in-flight hooks at shutdown are orphaned, which is acceptable)

To add a new column to `projects`/`sessions`/`messages`: use a separate `ALTER TABLE ADD COLUMN IF NOT EXISTS` step or a versioned migration table.

## Schema

### `projects`

```sql
CREATE TABLE projects (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    work_dirs         TEXT NOT NULL DEFAULT '[]',    -- JSON array of paths
    claude_config_dir TEXT NOT NULL DEFAULT '',
    default_model     TEXT NOT NULL DEFAULT 'haiku',
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

`work_dirs` serialized as JSON string (`["path1", "path2"]`). First element becomes `cmd.Dir`; the rest become `--add-dir` args.

### `sessions`

```sql
CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT 'haiku',
    status        TEXT NOT NULL DEFAULT 'idle',   -- idle|running|waiting_hook|error
    parent_id     TEXT,                            -- set for forks
    message_count INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_active   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

`message_count` is incremented by `InsertMessage`. Used to determine first-turn vs resume:
- `message_count == 0` → `--session-id=<id>` (new claude session)
- `message_count > 0` → `--resume=<id>` (continue)

### `messages`

```sql
CREATE TABLE messages (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,    -- user|assistant|hook|thinking|system|raw ⭐
    content    TEXT NOT NULL,    -- plain text OR JSON ⭐
    seq        INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_messages_session_seq ON messages(session_id, seq);
```

**Content format** ⭐:
- **User messages**:
  - Plain text: `"message text"`
  - With image: JSON array (see StartTurn section above)
- **Hook messages** (role="hook"):
  ```json
  { "tool_name": "Bash", "tool_input": {...}, "decision": "approve|deny|timeout" }
  ```
- **Other roles** (assistant, thinking, etc.): clean text, not raw JSON

`seq` is assigned from `NextMessageSeq()` which returns `MAX(seq)+1`.

Only final (non-partial) messages are persisted:
- `message_stop` event → text finalized, role=`assistant`, content=accumulated text
- `content_block_stop` for thinking blocks → role=`thinking`, content=accumulated thinking
- Hook decisions → inserted on `onHookRespond` or `onHookDisconnect` with role="hook"
- Streaming deltas (`content_block_delta`) are not stored individually.

The index `(session_id, seq)` enables efficient cursor-based pagination in `ListMessagesPaginated()`.

### `pending_hooks`

```sql
CREATE TABLE pending_hooks (
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_use_id TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    tool_input  TEXT NOT NULL,    -- JSON string
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (session_id, tool_use_id)
);
```

Composite primary key supports multiple concurrent pending hooks (parallel tool calls). Dropped and recreated on every server start — do not store anything here that needs to survive a restart.

### `ideas` ⭐

```sql
CREATE TABLE ideas (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL,    -- project_id or 'global'
    text       TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

Quick notes / todo items per-project or globally. `scope='global'` for workspace-wide ideas, `scope=<project_id>` for project-specific. Boolean `done` stored as 0/1. No foreign key on scope (allows orphaning when project deleted, acceptable).

### `user_tools` ⭐

```sql
CREATE TABLE user_tools (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    description    TEXT NOT NULL DEFAULT '',
    input_schema   TEXT NOT NULL DEFAULT '{}',      -- JSON Schema for tool arguments
    code           TEXT NOT NULL DEFAULT '',          -- JavaScript code
    execution_env  TEXT NOT NULL DEFAULT 'browser',   -- 'browser' | 'orchestrator'
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

User-defined tools for voice command detection. Each tool has:
- **name**: unique identifier used by the LLM to select the tool (e.g. `ui_accept`, `quick_note`)
- **description**: natural language description the LLM uses to match voice commands to tools
- **input_schema**: JSON Schema defining expected arguments (validated before execution)
- **code**: JavaScript code executed when the tool is invoked
- **execution_env**: `browser` (client-side `Function()`) or `orchestrator` (server-side Goja VM)

The `UNIQUE` constraint on `name` prevents duplicate tool definitions.

## Key Queries — `internal/db/queries.go`

```go
InsertProject(db, p)
ListProjects(db) []Project
GetProject(db, id) Project
DeleteProject(db, id)

InsertSession(db, s)
GetSession(db, id) Session
ListSessions(db, projectID) []Session
UpdateSessionStatus(db, id, status)
DeleteSession(db, id)

InsertMessage(db, m)                                    // also increments message_count + updates last_active
ListMessages(db, sessionID) []Message                   // (legacy: returns ALL messages)
ListMessagesPaginated(db, sessionID, limit, beforeSeq) ([]Message, hasMore, error)  // returns page + cursor
NextMessageSeq(db, sessionID) int

InsertPendingHook(db, h)                               // INSERT OR REPLACE
ListPendingHooks(db, sessionID) []PendingHook          // used on client reconnect
DeletePendingHook(db, sessionID, toolUseID) (int64, error)  // returns rows affected
CountPendingHooks(db, sessionID) int

InsertIdea(db, idea)                                   // ⭐ create new idea
ListIdeas(db, scope) []Idea                            // ⭐ get all ideas for scope (project_id or 'global')
UpdateIdeaDone(db, id, done) error                     // ⭐ toggle done status
DeleteIdea(db, id) error                               // ⭐ remove idea

InsertUserTool(db, tool)                               // ⭐ create user tool
ListUserTools(db) []UserTool                           // ⭐ all tools, ordered by created_at DESC
GetUserTool(db, id) UserTool                           // ⭐ by ID
GetUserToolByName(db, name) UserTool                   // ⭐ by unique name
UpdateUserTool(db, tool) error                         // ⭐ update fields + updated_at timestamp
DeleteUserTool(db, id) error                           // ⭐ remove tool
```

**Pagination**: `ListMessagesPaginated(db, sessionID, 50, nil)` returns the 50 newest messages in ASC order. `ListMessagesPaginated(db, sessionID, 50, &oldestSeq)` returns 50 messages with `seq < oldestSeq`, also in ASC order. `hasMore=true` means more older messages exist.

`DeletePendingHook` returning rows affected is critical: `0` means "already resolved" (skip cleanup), `1` means "pending when disconnected" (broadcast hook_resolved).
