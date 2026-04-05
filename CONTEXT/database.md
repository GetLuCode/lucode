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
    role       TEXT NOT NULL,    -- user|assistant|tool_result|system|thinking|raw
    content    TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

`content` for assistant/tool messages is the raw JSON from the stream event. `seq` is assigned from `NextMessageSeq()` which returns `MAX(seq)+1`.

Only final (non-partial) `stream_event` messages are persisted. Streaming deltas are not stored.

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

InsertMessage(db, m)         // also increments message_count + updates last_active
ListMessages(db, sessionID) []Message
NextMessageSeq(db, sessionID) int

InsertPendingHook(db, h)                         // INSERT OR REPLACE
ListPendingHooks(db, sessionID) []PendingHook    // used on client reconnect
DeletePendingHook(db, sessionID, toolUseID) (int64, error)  // returns rows affected
CountPendingHooks(db, sessionID) int
```

`DeletePendingHook` returning rows affected is critical: `0` means "already resolved" (skip cleanup), `1` means "pending when disconnected" (broadcast hook_resolved).
