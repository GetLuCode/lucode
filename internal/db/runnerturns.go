package db

import "database/sql"

// RunnerTurn is a turn the hub sent to a runner and has not seen finish yet.
// LastSeq is the last event of the turn the hub already relayed and stored; the
// runner numbers its events from 1, so redelivered ones are the ones <= LastSeq.
type RunnerTurn struct {
	SessionID string
	RunnerID  string
	TurnID    string
	LastSeq   int
	Opts      string // JSON with what the event publisher needs (project, account, model...)
	StartedAt string
}

// SaveRunnerTurn records the turn in flight of a session, replacing any older one.
func SaveRunnerTurn(db *sql.DB, t RunnerTurn) error {
	_, err := db.Exec(`INSERT INTO runner_turns (session_id, runner_id, turn_id, last_seq, opts)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET runner_id=excluded.runner_id, turn_id=excluded.turn_id,
  last_seq=excluded.last_seq, opts=excluded.opts,
  started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		t.SessionID, t.RunnerID, t.TurnID, t.LastSeq, t.Opts)
	return err
}

// GetRunnerTurn returns the turn in flight of a session, or sql.ErrNoRows.
func GetRunnerTurn(db *sql.DB, sessID string) (RunnerTurn, error) {
	var t RunnerTurn
	err := db.QueryRow(`SELECT session_id, runner_id, turn_id, last_seq, opts, started_at FROM runner_turns WHERE session_id=?`, sessID).
		Scan(&t.SessionID, &t.RunnerID, &t.TurnID, &t.LastSeq, &t.Opts, &t.StartedAt)
	return t, err
}

// SetRunnerTurnSeq stores the last confirmed event. It never goes backwards.
func SetRunnerTurnSeq(db *sql.DB, sessID, turnID string, seq int) error {
	_, err := db.Exec(`UPDATE runner_turns SET last_seq=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE session_id=? AND turn_id=? AND last_seq<?`, seq, sessID, turnID, seq)
	return err
}

// DeleteRunnerTurn forgets the turn once it is over.
func DeleteRunnerTurn(db *sql.DB, sessID, turnID string) error {
	_, err := db.Exec(`DELETE FROM runner_turns WHERE session_id=? AND turn_id=?`, sessID, turnID)
	return err
}

// ListRunnerTurns returns every turn in flight.
func ListRunnerTurns(db *sql.DB) ([]RunnerTurn, error) {
	rows, err := db.Query(`SELECT session_id, runner_id, turn_id, last_seq, opts, started_at FROM runner_turns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunnerTurn
	for rows.Next() {
		var t RunnerTurn
		if err := rows.Scan(&t.SessionID, &t.RunnerID, &t.TurnID, &t.LastSeq, &t.Opts, &t.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
