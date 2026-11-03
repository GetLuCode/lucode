package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// rebuildAgentsWithoutProjectFK drops the foreign key from agents.project_id.
//
// An agent used to belong to exactly one project (ON DELETE CASCADE). Agents can
// now be global: project_id "" means "available in every project", which a
// foreign key cannot express. Deleting a project therefore deletes its own agents
// explicitly (DeleteProject) and leaves global ones alone.
//
// SQLite cannot drop a constraint in place, so the table is rebuilt. It is a
// no-op once the constraint is gone, so it is safe to run on every start.
func rebuildAgentsWithoutProjectFK(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='agents'`).Scan(&ddl); err != nil {
		return fmt.Errorf("read agents schema: %w", err)
	}
	if !strings.Contains(ddl, "REFERENCES projects") {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE agents_new (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL,
    model       TEXT NOT NULL DEFAULT 'sonnet',
    account_id  TEXT NOT NULL DEFAULT '',
    personality TEXT NOT NULL DEFAULT '',
    tools       TEXT NOT NULL DEFAULT '["Bash","Edit","Read","Write"]',
    executor    TEXT NOT NULL DEFAULT 'aiapi',
    persistent  INTEGER NOT NULL DEFAULT 0,
    reports_to  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`,
		`INSERT INTO agents_new (id, project_id, name, model, account_id, personality, tools, executor, persistent, reports_to, created_at, updated_at)
 SELECT id, project_id, name, model, account_id, personality, tools, executor, persistent, reports_to, created_at, updated_at FROM agents`,
		`DROP TABLE agents`,
		`ALTER TABLE agents_new RENAME TO agents`,
		`CREATE INDEX IF NOT EXISTS idx_agents_project ON agents(project_id)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild agents: %w", err)
		}
	}
	return tx.Commit()
}
