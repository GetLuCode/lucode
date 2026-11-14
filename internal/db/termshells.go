package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TermShell is a shell the user registered on a RUNNER for the Commands tab. It lives in the runner's
// own database (the table exists everywhere, but only a runner fills it): the hub never stores or runs
// a shell path, it only asks the runner for its list.
type TermShell struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"` // absolute path of the executable, on the runner
	Args      []string `json:"args"` // put before the command line ("-c"); empty for the default of the shell's name
	CreatedAt string   `json:"created_at"`
}

func migrateTermShells(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS term_shells (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    args       TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);`)
	if err != nil {
		return fmt.Errorf("create term_shells: %w", err)
	}
	return nil
}

func scanTermShell(sc interface{ Scan(...any) error }) (TermShell, error) {
	var s TermShell
	var args string
	if err := sc.Scan(&s.ID, &s.Name, &s.Path, &args, &s.CreatedAt); err != nil {
		return s, err
	}
	s.Args = []string{}
	_ = json.Unmarshal([]byte(args), &s.Args)
	return s, nil
}

func ListTermShells(db *sql.DB) ([]TermShell, error) {
	rows, err := db.Query(`SELECT id, name, path, args, created_at FROM term_shells ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TermShell{}
	for rows.Next() {
		s, err := scanTermShell(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func GetTermShell(db *sql.DB, id string) (TermShell, error) {
	return scanTermShell(db.QueryRow(`SELECT id, name, path, args, created_at FROM term_shells WHERE id = ?`, id))
}

func InsertTermShell(db *sql.DB, s TermShell) (TermShell, error) {
	s.ID = "custom-" + uuid.New().String()[:8]
	s.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if s.Args == nil {
		s.Args = []string{}
	}
	args, _ := json.Marshal(s.Args)
	_, err := db.Exec(`INSERT INTO term_shells (id, name, path, args, created_at) VALUES (?,?,?,?,?)`,
		s.ID, s.Name, s.Path, string(args), s.CreatedAt)
	return s, err
}

func DeleteTermShell(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`DELETE FROM term_shells WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
