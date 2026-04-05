package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// ---- Project ----

type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	WorkDirs        []string  `json:"work_dirs"`
	ClaudeConfigDir string    `json:"claude_config_dir"`
	DefaultModel    string    `json:"default_model"`
	CreatedAt       time.Time `json:"created_at"`
}

func InsertProject(db *sql.DB, p Project) error {
	dirs, _ := json.Marshal(p.WorkDirs)
	_, err := db.Exec(
		`INSERT INTO projects (id, name, work_dirs, claude_config_dir, default_model) VALUES (?,?,?,?,?)`,
		p.ID, p.Name, string(dirs), p.ClaudeConfigDir, p.DefaultModel,
	)
	return err
}

func ListProjects(db *sql.DB) ([]Project, error) {
	rows, err := db.Query(`SELECT id, name, work_dirs, claude_config_dir, default_model, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var dirsJSON string
		var createdAt string
		if err := rows.Scan(&p.ID, &p.Name, &dirsJSON, &p.ClaudeConfigDir, &p.DefaultModel, &createdAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(dirsJSON), &p.WorkDirs)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetProject(db *sql.DB, id string) (Project, error) {
	var p Project
	var dirsJSON, createdAt string
	err := db.QueryRow(
		`SELECT id, name, work_dirs, claude_config_dir, default_model, created_at FROM projects WHERE id=?`, id,
	).Scan(&p.ID, &p.Name, &dirsJSON, &p.ClaudeConfigDir, &p.DefaultModel, &createdAt)
	if err != nil {
		return p, err
	}
	json.Unmarshal([]byte(dirsJSON), &p.WorkDirs)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return p, nil
}

func DeleteProject(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

// ---- Session ----

type Session struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	ParentID     string    `json:"parent_id,omitempty"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
}

func InsertSession(db *sql.DB, s Session) error {
	_, err := db.Exec(
		`INSERT INTO sessions (id, project_id, name, model, status, parent_id) VALUES (?,?,?,?,?,?)`,
		s.ID, s.ProjectID, s.Name, s.Model, s.Status, nullStr(s.ParentID),
	)
	return err
}

func GetSession(db *sql.DB, id string) (Session, error) {
	var s Session
	var parentID sql.NullString
	var createdAt, lastActive string
	err := db.QueryRow(
		`SELECT id, project_id, name, model, status, parent_id, message_count, created_at, last_active FROM sessions WHERE id=?`, id,
	).Scan(&s.ID, &s.ProjectID, &s.Name, &s.Model, &s.Status, &parentID, &s.MessageCount, &createdAt, &lastActive)
	if err != nil {
		return s, err
	}
	if parentID.Valid {
		s.ParentID = parentID.String
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	s.LastActive, _ = time.Parse(time.RFC3339Nano, lastActive)
	return s, nil
}

func ListSessions(db *sql.DB, projectID string) ([]Session, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, model, status, parent_id, message_count, created_at, last_active FROM sessions WHERE project_id=? ORDER BY last_active DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var parentID sql.NullString
		var createdAt, lastActive string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Model, &s.Status, &parentID, &s.MessageCount, &createdAt, &lastActive); err != nil {
			return nil, err
		}
		if parentID.Valid {
			s.ParentID = parentID.String
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		s.LastActive, _ = time.Parse(time.RFC3339Nano, lastActive)
		out = append(out, s)
	}
	return out, rows.Err()
}

func UpdateSessionStatus(db *sql.DB, id, status string) error {
	_, err := db.Exec(
		`UPDATE sessions SET status=?, last_active=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		status, id,
	)
	return err
}

func UpdateSessionName(db *sql.DB, id, name string) error {
	_, err := db.Exec(
		`UPDATE sessions SET name=? WHERE id=?`,
		name, id,
	)
	return err
}

func DeleteSession(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return err
}

// ---- Message ----

type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Seq       int       `json:"seq"`
	CreatedAt time.Time `json:"created_at"`
}

func InsertMessage(db *sql.DB, m Message) error {
	_, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, content, seq) VALUES (?,?,?,?,?)`,
		m.ID, m.SessionID, m.Role, m.Content, m.Seq,
	)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`UPDATE sessions SET message_count=message_count+1, last_active=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		m.SessionID,
	)
	return err
}

func ListMessages(db *sql.DB, sessionID string) ([]Message, error) {
	rows, err := db.Query(
		`SELECT id, session_id, role, content, seq, created_at FROM messages WHERE session_id=? ORDER BY seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMessagesPaginated returns up to limit messages in ASC order.
// When beforeSeq is non-nil, only messages with seq < *beforeSeq are returned.
// hasMore is true when there are additional (older) messages beyond the page.
func ListMessagesPaginated(db *sql.DB, sessionID string, limit int, beforeSeq *int) ([]Message, bool, error) {
	var (
		rows *sql.Rows
		err  error
	)
	// Fetch limit+1 rows in DESC order to detect hasMore, then reverse.
	if beforeSeq != nil {
		rows, err = db.Query(
			`SELECT id, session_id, role, content, seq, created_at
			 FROM messages WHERE session_id=? AND seq < ?
			 ORDER BY seq DESC LIMIT ?`,
			sessionID, *beforeSeq, limit+1,
		)
	} else {
		rows, err = db.Query(
			`SELECT id, session_id, role, content, seq, created_at
			 FROM messages WHERE session_id=?
			 ORDER BY seq DESC LIMIT ?`,
			sessionID, limit+1,
		)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var createdAt string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt); err != nil {
			return nil, false, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}

	// Reverse to restore ASC order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, hasMore, nil
}

func NextMessageSeq(db *sql.DB, sessionID string) (int, error) {
	var seq sql.NullInt64
	err := db.QueryRow(`SELECT MAX(seq) FROM messages WHERE session_id=?`, sessionID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if seq.Valid {
		return int(seq.Int64) + 1, nil
	}
	return 0, nil
}

// ---- Pending Hook ----

type PendingHook struct {
	SessionID  string    `json:"session_id"`
	ToolUseID  string    `json:"tool_use_id"`
	ToolName   string    `json:"tool_name"`
	ToolInput  string    `json:"tool_input"`
	CreatedAt  time.Time `json:"created_at"`
}

func InsertPendingHook(db *sql.DB, h PendingHook) error {
	_, err := db.Exec(
		`INSERT INTO pending_hooks (session_id, tool_use_id, tool_name, tool_input) VALUES (?,?,?,?)
         ON CONFLICT(session_id, tool_use_id) DO UPDATE SET tool_name=excluded.tool_name, tool_input=excluded.tool_input, created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		h.SessionID, h.ToolUseID, h.ToolName, h.ToolInput,
	)
	return err
}

func ListPendingHooks(db *sql.DB, sessionID string) ([]PendingHook, error) {
	rows, err := db.Query(`SELECT session_id, tool_use_id, tool_name, tool_input FROM pending_hooks WHERE session_id=? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingHook
	for rows.Next() {
		var h PendingHook
		if err := rows.Scan(&h.SessionID, &h.ToolUseID, &h.ToolName, &h.ToolInput); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetPendingHook retrieves a single pending hook by session and tool_use ID.
func GetPendingHook(db *sql.DB, sessionID, toolUseID string) (PendingHook, error) {
	var h PendingHook
	var createdAt string
	err := db.QueryRow(
		`SELECT session_id, tool_use_id, tool_name, tool_input, created_at FROM pending_hooks WHERE session_id=? AND tool_use_id=?`,
		sessionID, toolUseID,
	).Scan(&h.SessionID, &h.ToolUseID, &h.ToolName, &h.ToolInput, &createdAt)
	if err != nil {
		return h, err
	}
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return h, nil
}

// DeletePendingHook deletes a specific pending hook and returns how many rows were deleted.
func DeletePendingHook(db *sql.DB, sessionID, toolUseID string) (int64, error) {
	res, err := db.Exec(`DELETE FROM pending_hooks WHERE session_id=? AND tool_use_id=?`, sessionID, toolUseID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func CountPendingHooks(db *sql.DB, sessionID string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pending_hooks WHERE session_id=?`, sessionID).Scan(&n)
	return n, err
}

// ---- helpers ----

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

