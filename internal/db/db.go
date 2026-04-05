package db

import (
	"database/sql"
	"fmt"

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

	// Recreate pending_hooks with composite (session_id, tool_use_id) primary key
	// to support concurrent parallel tool calls. Drop old single-PK table if present.
	if _, err := db.Exec(`DROP TABLE IF EXISTS pending_hooks`); err != nil {
		return err
	}
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

	return nil
}
