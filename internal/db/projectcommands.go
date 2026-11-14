package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ProjectCommand is a command a project defines to run in a terminal on a runner (the Commands tab).
// Only the definition lives here; whether it is running is the runner's to say.
type ProjectCommand struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Name      string   `json:"name"`
	Command   string   `json:"command"`   // empty opens an interactive shell
	Cwd       string   `json:"cwd"`       // relative to the project's work dir on the runner; "" = that dir
	RunnerID  string   `json:"runner_id"` // "" = the project's runner
	Shell     string   `json:"shell"`     // id of a shell the RUNNER offers (termrun shells); "" = the runner's default
	Env       []EnvVar `json:"env"`       // added to the runner's environment when it starts, in this order
	CreatedAt string   `json:"created_at"`
}

// EnvVar is one environment variable of a command. The values are the user's own and may be
// secrets: they are stored here and sent to the runner when the command starts, never logged.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func encodeEnv(env []EnvVar) string {
	if len(env) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func decodeEnv(s string) []EnvVar {
	out := []EnvVar{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

func migrateProjectCommands(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS project_commands (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    command    TEXT NOT NULL DEFAULT '',
    cwd        TEXT NOT NULL DEFAULT '',
    runner_id  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_project_commands_project ON project_commands(project_id, created_at);
`)
	if err != nil {
		return fmt.Errorf("create project_commands: %w", err)
	}
	db.Exec(`ALTER TABLE project_commands ADD COLUMN env TEXT NOT NULL DEFAULT '[]'`) // these fail harmlessly once they exist
	db.Exec(`ALTER TABLE project_commands ADD COLUMN shell TEXT NOT NULL DEFAULT ''`)
	return nil
}

func ListProjectCommands(db *sql.DB, projectID string) ([]ProjectCommand, error) {
	rows, err := db.Query(`SELECT id, project_id, name, command, cwd, runner_id, shell, env, created_at
		FROM project_commands WHERE project_id = ? ORDER BY created_at, rowid`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectCommand{}
	for rows.Next() {
		var c ProjectCommand
		var env string
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Command, &c.Cwd, &c.RunnerID, &c.Shell, &env, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Env = decodeEnv(env)
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetProjectCommand(db *sql.DB, id string) (ProjectCommand, error) {
	var c ProjectCommand
	var env string
	err := db.QueryRow(`SELECT id, project_id, name, command, cwd, runner_id, shell, env, created_at
		FROM project_commands WHERE id = ?`, id).
		Scan(&c.ID, &c.ProjectID, &c.Name, &c.Command, &c.Cwd, &c.RunnerID, &c.Shell, &env, &c.CreatedAt)
	c.Env = decodeEnv(env)
	return c, err
}

func InsertProjectCommand(db *sql.DB, c ProjectCommand) (ProjectCommand, error) {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO project_commands (id, project_id, name, command, cwd, runner_id, shell, env, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, c.ProjectID, c.Name, c.Command, c.Cwd, c.RunnerID, c.Shell, encodeEnv(c.Env), c.CreatedAt)
	return c, err
}

func UpdateProjectCommand(db *sql.DB, c ProjectCommand) error {
	_, err := db.Exec(`UPDATE project_commands SET name = ?, command = ?, cwd = ?, runner_id = ?, shell = ?, env = ? WHERE id = ?`,
		c.Name, c.Command, c.Cwd, c.RunnerID, c.Shell, encodeEnv(c.Env), c.ID)
	return err
}

func DeleteProjectCommand(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM project_commands WHERE id = ?`, id)
	return err
}
