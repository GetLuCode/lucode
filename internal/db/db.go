package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Init(path string) (*sql.DB, error) {
	// busy_timeout is a per-connection pragma, so it goes in the DSN to apply to every
	// pooled connection: concurrent writers (messages, raw events, usage) wait for the
	// lock instead of failing with SQLITE_BUSY.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
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

	// session_permissions — per-session permission rule overrides.
	// scope is one of "allow" | "deny" | "ask". input_regex is a
	// JSON-encoded map[string]string (field name -> regex pattern).
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS session_permissions (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool        TEXT NOT NULL,
    input_regex TEXT NOT NULL DEFAULT '',
    scope       TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
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

	// ai_profiles: preconfigured AIs of this runner (a claude_config_dir plus the
	// account info the Anthropic profile endpoint reports for it).
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS ai_profiles (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    claude_config_dir TEXT NOT NULL UNIQUE,
    email             TEXT NOT NULL DEFAULT '',
    display_name      TEXT NOT NULL DEFAULT '',
    organization      TEXT NOT NULL DEFAULT '',
    plan              TEXT NOT NULL DEFAULT '',
    error             TEXT NOT NULL DEFAULT '',
    checked_at        TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create ai_profiles: %w", err)
	}
	// projects.runner_id / ai_profile_id — the environment and preconfigured AI a project uses (idempotent)
	db.Exec(`ALTER TABLE projects ADD COLUMN runner_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE projects ADD COLUMN ai_profile_id TEXT NOT NULL DEFAULT ''`)
	// projects.env_dirs — work dirs per runner: {"<runner id>": ["/path", ...]}
	db.Exec(`ALTER TABLE projects ADD COLUMN env_dirs TEXT NOT NULL DEFAULT '{}'`)

	// projects.aiapi_instance_id — which aiapi instance to use for this project (idempotent)
	db.Exec(`ALTER TABLE projects ADD COLUMN aiapi_instance_id TEXT NOT NULL DEFAULT ''`)

	// claude_accounts: add token columns (idempotent)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN access_token  TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN refresh_token TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE claude_accounts ADD COLUMN expires_at    INTEGER NOT NULL DEFAULT 0`)

	// sessions.context_usage — last known context window breakdown per session (idempotent)
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_usage TEXT NOT NULL DEFAULT ''`)

	// Task model: a session belongs to a kanban card (task) and runs on its agent (idempotent)
	db.Exec(`ALTER TABLE sessions ADD COLUMN card_id  TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT ''`)
	// sessions.current_at — when an older orchestrator chat was brought back as the current one
	// (idempotent); empty = never, the creation time decides
	db.Exec(`ALTER TABLE sessions ADD COLUMN current_at TEXT NOT NULL DEFAULT ''`)
	// sessions.worktree_removed — 1 once merging its task deleted the worktree the agent
	// worked in (idempotent): a new turn would start claude in a directory that is gone
	db.Exec(`ALTER TABLE sessions ADD COLUMN worktree_removed INTEGER NOT NULL DEFAULT 0`)

	// aimode_conversations — the Google AI Mode conversations of the local model, by the url
	// that reopens them (idempotent). The url works with the browser profile that made it.
	db.Exec(`CREATE TABLE IF NOT EXISTS aimode_conversations (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    turns      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`)
	db.Exec(`ALTER TABLE aimode_conversations ADD COLUMN turns INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_aimode_conv_project ON aimode_conversations(project_id, updated_at)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_card  ON sessions(card_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id)`)

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

	// raw_events — WS events persisted for debugging/inspection per session per turn
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS raw_events (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_index  INTEGER NOT NULL DEFAULT 0,
    event       TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_raw_events_session ON raw_events(session_id);
`); err != nil {
		return fmt.Errorf("create raw_events: %w", err)
	}

	// kanban_lists — ordered columns on the kanban board per project
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS kanban_lists (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_kanban_lists_project ON kanban_lists(project_id, position);
`); err != nil {
		return fmt.Errorf("create kanban_lists: %w", err)
	}

	// kanban_cards — cards belonging to a list
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS kanban_cards (
    id          TEXT PRIMARY KEY,
    list_id     TEXT NOT NULL REFERENCES kanban_lists(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    card_type   TEXT NOT NULL DEFAULT 'task',
    tags        TEXT NOT NULL DEFAULT '[]',
    agent_id    TEXT NOT NULL DEFAULT '',
    depends_on  TEXT NOT NULL DEFAULT '[]',
    start_date  TEXT NOT NULL DEFAULT '',
    end_date    TEXT NOT NULL DEFAULT '',
    position    INTEGER NOT NULL DEFAULT 0,
    archived    INTEGER NOT NULL DEFAULT 0,
    parent_id   TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_kanban_cards_list    ON kanban_cards(list_id, position);
CREATE INDEX IF NOT EXISTS idx_kanban_cards_project ON kanban_cards(project_id);
`); err != nil {
		return fmt.Errorf("create kanban_cards: %w", err)
	}

	// agents — project-scoped AI agents with personality and tool configuration
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS agents (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    model       TEXT NOT NULL DEFAULT 'sonnet',
    account_id  TEXT NOT NULL DEFAULT '',
    personality TEXT NOT NULL DEFAULT '',
    tools       TEXT NOT NULL DEFAULT '["Bash","Edit","Read","Write"]',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_agents_project ON agents(project_id);
`); err != nil {
		return fmt.Errorf("create agents: %w", err)
	}

	// card_comments — progress notes and completion summaries posted on a task
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS card_comments (
    id         TEXT PRIMARY KEY,
    card_id    TEXT NOT NULL REFERENCES kanban_cards(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL DEFAULT '',
    author     TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT 'comment',
    body       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_card_comments_card ON card_comments(card_id, created_at);
`); err != nil {
		return fmt.Errorf("create card_comments: %w", err)
	}

	// card_images — pictures pasted into a task's description, referenced there as @image1, @image2...
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS card_images (
    card_id    TEXT NOT NULL REFERENCES kanban_cards(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    media_type TEXT NOT NULL,
    data       BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (card_id, name)
);
`); err != nil {
		return fmt.Errorf("create card_images: %w", err)
	}

	// claude_commands — every claude command line this instance started or was told about:
	// runner_id '' = started here, otherwise the runner that reported it
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS claude_commands (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    runner_id  TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL,
    exe        TEXT NOT NULL DEFAULT '',
    args       TEXT NOT NULL DEFAULT '[]',
    cwd        TEXT NOT NULL DEFAULT '',
    pid        INTEGER NOT NULL DEFAULT 0,
    error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_claude_commands_session ON claude_commands(session_id, at);
CREATE INDEX IF NOT EXISTS idx_claude_commands_runner ON claude_commands(runner_id, at);
`); err != nil {
		return fmt.Errorf("create claude_commands: %w", err)
	}

	// session_workspaces — where claude last worked in a session (cwd, branch, worktree...), as JSON
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS session_workspaces (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    data       TEXT NOT NULL
);
`); err != nil {
		return fmt.Errorf("create session_workspaces: %w", err)
	}

	// card_reviews — the git branch/worktree of a task sitting in the Review list
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS card_reviews (
    card_id       TEXT PRIMARY KEY REFERENCES kanban_cards(id) ON DELETE CASCADE,
    branch        TEXT NOT NULL,
    worktree_path TEXT NOT NULL DEFAULT '',
    repo_path     TEXT NOT NULL
);
`); err != nil {
		return fmt.Errorf("create card_reviews: %w", err)
	}

	// Column additions for tables created above (idempotent).
	// card_reviews.runner_id — the runner whose disk holds the repository and worktree;
	// git runs there, never on the hub. "" = this instance (legacy rows)
	db.Exec(`ALTER TABLE card_reviews ADD COLUMN runner_id TEXT NOT NULL DEFAULT ''`)
	// kanban_lists.kind — the workflow role of a list ("backlog", "todo", "inprogress",
	// "review", "done", "blocked"; empty for custom lists). Moving a card into "inprogress" runs it.
	db.Exec(`ALTER TABLE kanban_lists ADD COLUMN kind TEXT NOT NULL DEFAULT ''`)
	// kanban_cards.started_at / finished_at — real timestamps, set when the card enters
	// an "inprogress" / "done" list (start_date / end_date are the planned dates).
	db.Exec(`ALTER TABLE kanban_cards ADD COLUMN started_at TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE kanban_cards ADD COLUMN finished_at TEXT NOT NULL DEFAULT ''`)
	backfillKanbanListKinds(db)
	// agents.executor — "aiapi" (HTTP provider service) or "claude" (claude -p CLI)
	db.Exec(`ALTER TABLE agents ADD COLUMN executor TEXT NOT NULL DEFAULT 'aiapi'`)
	// agents.persistent — keep the claude -p process alive between turns
	db.Exec(`ALTER TABLE agents ADD COLUMN persistent INTEGER NOT NULL DEFAULT 0`)
	// agents.reports_to — the supervising agent that is told when this one finishes, blocks or fails
	db.Exec(`ALTER TABLE agents ADD COLUMN reports_to TEXT NOT NULL DEFAULT ''`)
	// agents.project_id "" = a global agent shared by every project, which a foreign key
	// cannot express, so drop it (rebuilds the table once)
	if err := rebuildAgentsWithoutProjectFK(db); err != nil {
		return err
	}
	// agents.lucode_tools — which lucode tools (task_*, agent_list, send_to_agent, task_info) the agent has
	db.Exec(`ALTER TABLE agents ADD COLUMN lucode_tools TEXT NOT NULL DEFAULT '["task_comment","task_done","task_blocked","agent_list","send_to_agent","task_info","get_task_image","task_list","task_create","task_update","agent_create","agent_update","agent_delete","project_info"]'`)
	// get_task_image came after task_info: agents that already look tasks up get it too.
	db.Exec(`UPDATE agents SET lucode_tools = REPLACE(lucode_tools, '"task_info"', '"task_info","get_task_image"') WHERE lucode_tools LIKE '%"task_info"%' AND lucode_tools NOT LIKE '%get_task_image%'`)

	// After the rebuild above, which only copies the columns it knows.
	// agents.runner_id / ai_profile_id: environment and preconfigured AI that override the project's (empty = inherit)
	db.Exec(`ALTER TABLE agents ADD COLUMN runner_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE agents ADD COLUMN ai_profile_id TEXT NOT NULL DEFAULT ''`)
	// agents.auto_mode — run claude with --permission-mode auto instead of asking through the PreToolUse hook
	db.Exec(`ALTER TABLE agents ADD COLUMN auto_mode INTEGER NOT NULL DEFAULT 0`)
	// agents.worktree / worktree_base_ref — run claude in its own git worktree (--worktree) and the worktree.baseRef setting ("head" | "fresh")
	db.Exec(`ALTER TABLE agents ADD COLUMN worktree INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE agents ADD COLUMN worktree_base_ref TEXT NOT NULL DEFAULT 'head'`)

	// runner_turns — turns this hub has in flight on a runner, to pick them up again after a restart
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS runner_turns (
    session_id TEXT PRIMARY KEY,
    runner_id  TEXT NOT NULL,
    turn_id    TEXT NOT NULL,
    last_seq   INTEGER NOT NULL DEFAULT 0,
    opts       TEXT NOT NULL DEFAULT '{}',
    started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`); err != nil {
		return fmt.Errorf("create runner_turns: %w", err)
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
