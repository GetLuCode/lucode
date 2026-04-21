package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Init(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return nil, fmt.Errorf("pragma: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS projects (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    work_dirs         TEXT NOT NULL DEFAULT '[]',
    claude_config_dir TEXT NOT NULL DEFAULT '',
    default_model     TEXT NOT NULL DEFAULT 'haiku',
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT 'haiku',
    status        TEXT NOT NULL DEFAULT 'idle',
    parent_id     TEXT,
    message_count INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_active   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS messages (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);
`); err != nil {
		return err
	}

	// Remove messages that were incorrectly stored as raw stream-event JSON blobs.
	// These were written by an old version of manager.go that persisted every
	// streaming delta instead of only finalized text/thinking. They render as
	// raw JSON in the UI; deleting them lets re-created sessions store correctly.
	if _, err := db.Exec(`
DELETE FROM messages
WHERE content LIKE '{"type":"stream_event"%'
   OR content LIKE '{"type":"thinking"%'
`); err != nil {
		return err
	}

	// Create pending_hooks with composite (session_id, tool_use_id) primary key
	// to support concurrent parallel tool calls. Persists across restarts.
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS pending_hooks (
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_use_id TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    tool_input  TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (session_id, tool_use_id)
)
`); err != nil {
		return err
	}

	// Ideas — quick notes scoped to a project (scope=project_id) or global (scope='global')
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS ideas (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL,
    text       TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)
`); err != nil {
		return err
	}

	// Add starred column to messages (idempotent — ignore error if already exists)
	db.Exec(`ALTER TABLE messages ADD COLUMN starred INTEGER NOT NULL DEFAULT 0`)

	// claude_accounts — named CLAUDE_CONFIG_DIR entries for account selection
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS claude_accounts (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    claude_config_dir TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create claude_accounts: %w", err)
	}

	// sessions.account_id — links session to a registered claude account (idempotent)
	db.Exec(`ALTER TABLE sessions ADD COLUMN account_id TEXT NOT NULL DEFAULT ''`)

	// FTS5 virtual table with trigram tokenizer for substring search.
	// Drop and recreate to ensure the tokenizer is correct (safe — just a search index).
	if _, err := db.Exec(`DROP TABLE IF EXISTS messages_fts`); err != nil {
		return fmt.Errorf("drop messages_fts: %w", err)
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS messages_fts_ai`); err != nil {
		return fmt.Errorf("drop messages_fts_ai trigger: %w", err)
	}
	if _, err := db.Exec(`
CREATE VIRTUAL TABLE messages_fts USING fts5(
	message_id UNINDEXED,
	session_id UNINDEXED,
	role       UNINDEXED,
	content,
	tokenize = 'trigram'
)`); err != nil {
		return fmt.Errorf("create messages_fts table: %w", err)
	}

	// Insert trigger: only index messages with searchable roles
	if _, err := db.Exec(`
CREATE TRIGGER messages_fts_ai
AFTER INSERT ON messages
WHEN new.role IN ('user', 'assistant', 'thinking')
BEGIN
	INSERT INTO messages_fts(message_id, session_id, role, content)
	VALUES (new.id, new.session_id, new.role, new.content);
END`); err != nil {
		return fmt.Errorf("create messages_fts_ai trigger: %w", err)
	}

	// Populate FTS table from all existing messages
	if _, err := db.Exec(`
INSERT INTO messages_fts(message_id, session_id, role, content)
SELECT id, session_id, role, content FROM messages
WHERE role IN ('user', 'assistant', 'thinking')`); err != nil {
		return fmt.Errorf("populate messages_fts table: %w", err)
	}

	// user_tools — user-defined tools for voice command detection
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS user_tools (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    description    TEXT NOT NULL DEFAULT '',
    input_schema   TEXT NOT NULL DEFAULT '{}',
    code           TEXT NOT NULL DEFAULT '',
    execution_env  TEXT NOT NULL DEFAULT 'browser',
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create user_tools: %w", err)
	}

	// users — lucode web UI user accounts
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create users: %w", err)
	}
	// Idempotent: add is_admin to existing DBs
	db.Exec(`ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0`)

	// sessions_auth — web session tokens (distinct from claude sessions)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sessions_auth (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at DATETIME NOT NULL
)`); err != nil {
		return fmt.Errorf("create sessions_auth: %w", err)
	}

	// api_keys — per-user API keys for machine-to-machine access (e.g. credfeeder)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    key_hash   TEXT NOT NULL UNIQUE,
    prefix     TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create api_keys: %w", err)
	}

	// projects.aiapi_instance_id — which aiapi instance to use for this project (idempotent)
	db.Exec(`ALTER TABLE projects ADD COLUMN aiapi_instance_id TEXT NOT NULL DEFAULT ''`)

	// claude_accounts: add token columns (idempotent)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN access_token  TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN refresh_token TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN expires_at    INTEGER NOT NULL DEFAULT 0`)

	// sessions.context_usage — last known context window breakdown per session (idempotent)
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_usage TEXT NOT NULL DEFAULT ''`)

	// turn_usage — per-turn token consumption for analytics
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS turn_usage (
    id                           TEXT PRIMARY KEY,
    session_id                   TEXT NOT NULL DEFAULT '',
    project_id                   TEXT NOT NULL DEFAULT '',
    account_id                   TEXT NOT NULL DEFAULT '',
    model                        TEXT NOT NULL DEFAULT '',
    input_tokens                 INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens      INTEGER NOT NULL DEFAULT 0,
    created_at                   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_turn_usage_session   ON turn_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_turn_usage_project   ON turn_usage(project_id);
CREATE INDEX IF NOT EXISTS idx_turn_usage_created   ON turn_usage(created_at);
`); err != nil {
		return fmt.Errorf("create turn_usage: %w", err)
	}

	// Remove ON DELETE CASCADE from turn_usage.session_id so deleting a session
	// does not wipe its historical metrics. Migrate only if the old schema is present.
	if err := migrateTurnUsageDropCascade(db); err != nil {
		return fmt.Errorf("migrate turn_usage cascade: %w", err)
	}

	return nil
}

// migrateTurnUsageDropCascade recreates turn_usage without the ON DELETE CASCADE
// on session_id so that deleting a session preserves its historical metrics.
// It is a no-op when the table already has the correct schema.
func migrateTurnUsageDropCascade(db *sql.DB) error {
	var schema string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='turn_usage'`,
	).Scan(&schema)
	if err != nil {
		return nil // table doesn't exist yet — nothing to do
	}
	if !strings.Contains(schema, "ON DELETE CASCADE") {
		return nil // already migrated
	}

	_, err = db.Exec(`
PRAGMA foreign_keys = OFF;
CREATE TABLE IF NOT EXISTS turn_usage_new (
    id                           TEXT PRIMARY KEY,
    session_id                   TEXT NOT NULL DEFAULT '',
    project_id                   TEXT NOT NULL DEFAULT '',
    account_id                   TEXT NOT NULL DEFAULT '',
    model                        TEXT NOT NULL DEFAULT '',
    input_tokens                 INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens      INTEGER NOT NULL DEFAULT 0,
    created_at                   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO turn_usage_new SELECT * FROM turn_usage;
DROP TABLE turn_usage;
ALTER TABLE turn_usage_new RENAME TO turn_usage;
CREATE INDEX IF NOT EXISTS idx_turn_usage_session ON turn_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_turn_usage_project ON turn_usage(project_id);
CREATE INDEX IF NOT EXISTS idx_turn_usage_created ON turn_usage(created_at);
PRAGMA foreign_keys = ON;
`)
	return err
}
