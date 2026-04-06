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

func UpdateProjectName(db *sql.DB, id, name string) error {
	_, err := db.Exec(`UPDATE projects SET name=? WHERE id=?`, name, id)
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
	Starred   bool      `json:"starred"`
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

func GetMessage(db *sql.DB, messageID string) (Message, error) {
	var m Message
	var createdAt string
	var starred int
	err := db.QueryRow(
		`SELECT id, session_id, role, content, seq, created_at, starred FROM messages WHERE id=?`,
		messageID,
	).Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt, &starred)
	if err != nil {
		return m, err
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	m.Starred = starred != 0
	return m, nil
}

func ListMessages(db *sql.DB, sessionID string) ([]Message, error) {
	rows, err := db.Query(
		`SELECT id, session_id, role, content, seq, created_at, starred FROM messages WHERE session_id=? ORDER BY seq ASC`,
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
		var starred int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt, &starred); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		m.Starred = starred != 0
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
			`SELECT id, session_id, role, content, seq, created_at, starred
			 FROM messages WHERE session_id=? AND seq < ?
			 ORDER BY seq DESC LIMIT ?`,
			sessionID, *beforeSeq, limit+1,
		)
	} else {
		rows, err = db.Query(
			`SELECT id, session_id, role, content, seq, created_at, starred
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
		var starred int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt, &starred); err != nil {
			return nil, false, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		m.Starred = starred != 0
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

// ToggleStarMessage toggles the starred flag on a message and returns the new value.
func ToggleStarMessage(db *sql.DB, id string) (bool, error) {
	_, err := db.Exec(
		`UPDATE messages SET starred = 1 - starred WHERE id=?`,
		id,
	)
	if err != nil {
		return false, err
	}

	var starred int
	err = db.QueryRow(`SELECT starred FROM messages WHERE id=?`, id).Scan(&starred)
	if err != nil {
		return false, err
	}
	return starred != 0, nil
}

// ListStarredMessages returns all starred messages for a session, ordered by seq.
func ListStarredMessages(db *sql.DB, sessionID string) ([]Message, error) {
	rows, err := db.Query(
		`SELECT id, session_id, role, content, seq, created_at, starred FROM messages
		 WHERE session_id=? AND starred=1 ORDER BY seq ASC`,
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
		var starred int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Seq, &createdAt, &starred); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		m.Starred = starred != 0
		out = append(out, m)
	}
	return out, rows.Err()
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

// ---- Idea ----

type Idea struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Text      string    `json:"text"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

func InsertIdea(db *sql.DB, idea Idea) error {
	_, err := db.Exec(
		`INSERT INTO ideas (id, scope, text, done) VALUES (?,?,?,?)`,
		idea.ID, idea.Scope, idea.Text, boolToInt(idea.Done),
	)
	return err
}

func ListIdeas(db *sql.DB, scope string) ([]Idea, error) {
	rows, err := db.Query(
		`SELECT id, scope, text, done, created_at FROM ideas WHERE scope=? ORDER BY created_at ASC`,
		scope,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Idea
	for rows.Next() {
		var idea Idea
		var done int
		var createdAt string
		if err := rows.Scan(&idea.ID, &idea.Scope, &idea.Text, &done, &createdAt); err != nil {
			return nil, err
		}
		idea.Done = done != 0
		idea.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, idea)
	}
	return out, rows.Err()
}

func UpdateIdeaDone(db *sql.DB, id string, done bool) error {
	_, err := db.Exec(`UPDATE ideas SET done=? WHERE id=?`, boolToInt(done), id)
	return err
}

func DeleteIdea(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM ideas WHERE id=?`, id)
	return err
}

// ---- helpers ----

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ---- Search ----

type SearchResult struct {
	Type      string `json:"type"`       // "project" | "session" | "message"
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`      // project name, session name, or message snippet
	Snippet   string `json:"snippet"`    // matched text excerpt (messages only)
	Seq       int    `json:"seq"`        // message sequence number (messages only)
}

// Search searches for projects, sessions, and messages by query string.
// Returns results grouped by type.
func Search(db *sql.DB, query string) ([]SearchResult, error) {
	var results []SearchResult

	// Query 1: Search projects by name (case-insensitive LIKE)
	projectRows, err := db.Query(
		`SELECT id, name FROM projects
		 WHERE LOWER(name) LIKE '%' || lower(?) || '%'
		 ORDER BY name ASC LIMIT 10`,
		query,
	)
	if err != nil {
		return nil, err
	}
	defer projectRows.Close()

	for projectRows.Next() {
		var id, name string
		if err := projectRows.Scan(&id, &name); err != nil {
			return nil, err
		}
		results = append(results, SearchResult{
			Type:      "project",
			ID:        id,
			ProjectID: id,
			SessionID: "",
			Title:     name,
			Snippet:   "",
			Seq:       0,
		})
	}
	if err := projectRows.Err(); err != nil {
		return nil, err
	}

	// Query 2: Search sessions by name (case-insensitive LIKE)
	sessionRows, err := db.Query(
		`SELECT id, project_id, name FROM sessions
		 WHERE LOWER(name) LIKE '%' || lower(?) || '%'
		 ORDER BY name ASC LIMIT 10`,
		query,
	)
	if err != nil {
		return nil, err
	}
	defer sessionRows.Close()

	for sessionRows.Next() {
		var id, projectID, name string
		if err := sessionRows.Scan(&id, &projectID, &name); err != nil {
			return nil, err
		}
		results = append(results, SearchResult{
			Type:      "session",
			ID:        id,
			ProjectID: projectID,
			SessionID: id,
			Title:     name,
			Snippet:   "",
			Seq:       0,
		})
	}
	if err := sessionRows.Err(); err != nil {
		return nil, err
	}

	// Query 3: Search messages using FTS5 with trigram tokenizer.
	// Trigram enables substring matching: "priv" finds "privacy", "river", etc.
	// Minimum effective query length is 3 characters (trigram requirement).
	messageRows, err := db.Query(
		`SELECT DISTINCT m.id, m.session_id, s.project_id, s.name, m.content, m.seq, m.role
		 FROM messages_fts fts
		 JOIN messages m ON fts.message_id = m.id
		 JOIN sessions s ON m.session_id = s.id
		 WHERE fts.content MATCH ?
		 ORDER BY rank ASC LIMIT 30`,
		query,
	)
	if err != nil {
		return nil, err
	}
	defer messageRows.Close()

	seenMessageIds := make(map[string]bool)
	for messageRows.Next() {
		var id, sessionID, projectID, sessionName, content string
		var seq int
		var role string
		if err := messageRows.Scan(&id, &sessionID, &projectID, &sessionName, &content, &seq, &role); err != nil {
			return nil, err
		}

		// Skip duplicate message IDs (in case of FTS quirks)
		if seenMessageIds[id] {
			continue
		}
		seenMessageIds[id] = true

		// Truncate content snippet to 120 chars
		snippet := content
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}

		results = append(results, SearchResult{
			Type:      "message",
			ID:        id,
			ProjectID: projectID,
			SessionID: sessionID,
			Title:     sessionName,
			Snippet:   snippet,
			Seq:       seq,
		})
	}
	if err := messageRows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

