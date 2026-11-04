package db

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ClaudeCommand is one claude process an instance launched (or failed to launch).
type ClaudeCommand struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session_id"`
	RunnerID  string   `json:"runner_id,omitempty"` // "" = started on this instance
	At        string   `json:"at"`                  // RFC3339
	Exe       string   `json:"exe"`
	Args      []string `json:"args"`
	Cwd       string   `json:"cwd"`
	PID       int      `json:"pid"`
	Error     string   `json:"error,omitempty"`
}

// maxClaudeCommands bounds the history kept per instance.
const maxClaudeCommands = 1000

// ClaudeCommandFromEvent reads a claude_command event (see executor.CommandEvent).
func ClaudeCommandFromEvent(ev map[string]any, runnerID string) ClaudeCommand {
	str := func(k string) string { s, _ := ev[k].(string); return s }
	c := ClaudeCommand{
		ID: uuid.New().String(), SessionID: str("session_id"), RunnerID: runnerID,
		At: str("at"), Exe: str("exe"), Cwd: str("cwd"), Error: str("error"), Args: []string{},
	}
	if c.At == "" {
		c.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	switch p := ev["pid"].(type) {
	case int:
		c.PID = p
	case float64:
		c.PID = int(p)
	}
	switch a := ev["args"].(type) {
	case []string:
		c.Args = a
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				c.Args = append(c.Args, s)
			}
		}
	}
	return c
}

func InsertClaudeCommand(db *sql.DB, c ClaudeCommand) error {
	args, _ := json.Marshal(c.Args)
	if _, err := db.Exec(`INSERT INTO claude_commands (id, session_id, runner_id, at, exe, args, cwd, pid, error) VALUES (?,?,?,?,?,?,?,?,?)`,
		c.ID, c.SessionID, c.RunnerID, c.At, c.Exe, string(args), c.Cwd, c.PID, c.Error); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM claude_commands WHERE id NOT IN (SELECT id FROM claude_commands ORDER BY at DESC LIMIT ?)`, maxClaudeCommands)
	return err
}

func scanClaudeCommand(row interface{ Scan(...any) error }) (ClaudeCommand, error) {
	var c ClaudeCommand
	var args string
	if err := row.Scan(&c.ID, &c.SessionID, &c.RunnerID, &c.At, &c.Exe, &args, &c.Cwd, &c.PID, &c.Error); err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(args), &c.Args)
	if c.Args == nil {
		c.Args = []string{}
	}
	return c, nil
}

const claudeCommandCols = `id, session_id, runner_id, at, exe, args, cwd, pid, error`

// LatestClaudeCommand is the newest command a session was started with.
func LatestClaudeCommand(db *sql.DB, sessionID string) (ClaudeCommand, error) {
	return scanClaudeCommand(db.QueryRow(`SELECT `+claudeCommandCols+` FROM claude_commands WHERE session_id=? ORDER BY at DESC LIMIT 1`, sessionID))
}

// ListClaudeCommands returns the commands started on this instance (runner_id ''),
// newest first.
func ListClaudeCommands(db *sql.DB, limit int) ([]ClaudeCommand, error) {
	if limit <= 0 || limit > maxClaudeCommands {
		limit = 100
	}
	rows, err := db.Query(`SELECT `+claudeCommandCols+` FROM claude_commands WHERE runner_id='' ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClaudeCommand{}
	for rows.Next() {
		c, err := scanClaudeCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
