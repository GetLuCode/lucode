package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---- Project ----

type Project struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	WorkDirs        []string            `json:"work_dirs"`
	ClaudeConfigDir string              `json:"claude_config_dir"`
	DefaultModel    string              `json:"default_model"`
	AIAPIInstanceID string              `json:"aiapi_instance_id,omitempty"`
	RunnerID        string              `json:"runner_id"`     // instance the project runs on ("" = this one)
	AIProfileID     string              `json:"ai_profile_id"` // preconfigured AI of that runner ("" = default account)
	EnvDirs         map[string][]string `json:"env_dirs"`      // work dirs on each runner other than this one
	CreatedAt       time.Time           `json:"created_at"`
	// LastUsedAt is the latest activity of its sessions or tasks (zero when it has none);
	// only ListProjects fills it, for the start page.
	LastUsedAt time.Time `json:"last_used_at"`
}

// parseDBTime reads the timestamp formats the tables use.
func parseDBTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func InsertProject(db *sql.DB, p Project) error {
	dirs, _ := json.Marshal(p.WorkDirs)
	env, _ := json.Marshal(p.EnvDirs)
	if p.EnvDirs == nil {
		env = []byte("{}")
	}
	_, err := db.Exec(
		`INSERT INTO projects (id, name, work_dirs, claude_config_dir, default_model, aiapi_instance_id, runner_id, ai_profile_id, env_dirs) VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, string(dirs), p.ClaudeConfigDir, p.DefaultModel, p.AIAPIInstanceID, p.RunnerID, p.AIProfileID, string(env),
	)
	return err
}

func ListProjects(db *sql.DB) ([]Project, error) {
	rows, err := db.Query(`SELECT id, name, work_dirs, claude_config_dir, default_model, aiapi_instance_id, runner_id, ai_profile_id, env_dirs, created_at,
		COALESCE((SELECT MAX(t) FROM (
			SELECT last_active AS t FROM sessions WHERE project_id = projects.id
			UNION ALL
			SELECT updated_at FROM kanban_cards WHERE project_id = projects.id
		)), '')
		FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var dirsJSON, envJSON, createdAt, lastUsed string
		if err := rows.Scan(&p.ID, &p.Name, &dirsJSON, &p.ClaudeConfigDir, &p.DefaultModel, &p.AIAPIInstanceID, &p.RunnerID, &p.AIProfileID, &envJSON, &createdAt, &lastUsed); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(dirsJSON), &p.WorkDirs)
		json.Unmarshal([]byte(envJSON), &p.EnvDirs)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		p.LastUsedAt = parseDBTime(lastUsed)
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetProject(db *sql.DB, id string) (Project, error) {
	var p Project
	var dirsJSON, envJSON, createdAt string
	err := db.QueryRow(
		`SELECT id, name, work_dirs, claude_config_dir, default_model, aiapi_instance_id, runner_id, ai_profile_id, env_dirs, created_at FROM projects WHERE id=?`, id,
	).Scan(&p.ID, &p.Name, &dirsJSON, &p.ClaudeConfigDir, &p.DefaultModel, &p.AIAPIInstanceID, &p.RunnerID, &p.AIProfileID, &envJSON, &createdAt)
	if err != nil {
		return p, err
	}
	json.Unmarshal([]byte(dirsJSON), &p.WorkDirs)
	json.Unmarshal([]byte(envJSON), &p.EnvDirs)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return p, nil
}

func DeleteProject(db *sql.DB, id string) error {
	// Agents are not tied to projects by a foreign key (global ones have no project),
	// so remove the project's own agents here. Global agents are kept.
	if _, err := db.Exec(`DELETE FROM agents WHERE project_id=? AND project_id<>''`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func UpdateProjectAIAPIInstance(db *sql.DB, id, aiapiInstanceID string) error {
	_, err := db.Exec(`UPDATE projects SET aiapi_instance_id=? WHERE id=?`, aiapiInstanceID, id)
	return err
}

func UpdateProjectName(db *sql.DB, id, name string) error {
	_, err := db.Exec(`UPDATE projects SET name=? WHERE id=?`, name, id)
	return err
}

// UpdateProjectConfigDir sets which Claude config dir (account) a project runs with.
func UpdateProjectConfigDir(db *sql.DB, id, dir string) error {
	_, err := db.Exec(`UPDATE projects SET claude_config_dir=? WHERE id=?`, dir, id)
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
	AccountID    string    `json:"account_id,omitempty"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
	ContextUsage string    `json:"context_usage,omitempty"`
	CardID       string    `json:"card_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	// Kind is "" for an ordinary chat and SessionKindOrchestrator for the project's
	// main chat (the right-hand panel), which is not listed with the other sessions.
	Kind string `json:"kind,omitempty"`
}

const SessionKindOrchestrator = "orchestrator"

// GetOrchestratorSession returns the project's current orchestrator chat, the newest one
// (older ones stay stored but are no longer shown); sql.ErrNoRows if none yet.
func GetOrchestratorSession(db *sql.DB, projectID string) (Session, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM sessions WHERE project_id=? AND kind=? ORDER BY CASE WHEN current_at='' THEN created_at ELSE current_at END DESC, (current_at<>'') DESC, rowid DESC LIMIT 1`, projectID, SessionKindOrchestrator).Scan(&id)
	if err != nil {
		return Session{}, err
	}
	return GetSession(db, id)
}

// MakeOrchestratorCurrent brings an older orchestrator chat back as the one the panel shows.
func MakeOrchestratorCurrent(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE sessions SET current_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND kind=?`, id, SessionKindOrchestrator)
	return err
}

// OrchestratorChat is a stored orchestrator conversation as the history lists it.
type OrchestratorChat struct {
	Session
	ProjectName string `json:"project_name"`
	FirstUser   string `json:"-"` // raw content of the first user message, for the preview
}

// ListOrchestratorChats lists the orchestrator conversations that have messages, the latest
// first. projectID "" lists every project's, so an old chat can be found again by name.
func ListOrchestratorChats(db *sql.DB, projectID string) ([]OrchestratorChat, error) {
	rows, err := db.Query(`SELECT s.id, COALESCE(p.name,''),
		COALESCE((SELECT content FROM messages m WHERE m.session_id=s.id AND m.role='user' ORDER BY m.seq LIMIT 1),'')
		FROM sessions s LEFT JOIN projects p ON p.id=s.project_id
		WHERE s.kind=? AND (?='' OR s.project_id=?) AND s.message_count>0
		ORDER BY s.last_active DESC`, SessionKindOrchestrator, projectID, projectID)
	if err != nil {
		return nil, err
	}
	type row struct{ id, project, first string }
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.project, &r.first); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	var out []OrchestratorChat
	for _, r := range found {
		s, err := GetSession(db, r.id)
		if err != nil {
			continue
		}
		out = append(out, OrchestratorChat{Session: s, ProjectName: r.project, FirstUser: r.first})
	}
	return out, nil
}

// MoveOrchestratorChat assigns an orchestrator conversation to another project (its Google AI
// Mode conversation goes with it). The chat stops being the current one of the project it left.
func MoveOrchestratorChat(db *sql.DB, id, projectID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE sessions SET project_id=?, current_at='' WHERE id=? AND kind=?`, projectID, id, SessionKindOrchestrator)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`UPDATE aimode_conversations SET project_id=? WHERE session_id=?`, projectID, id); err != nil {
		return err
	}
	return tx.Commit()
}

func InsertSession(db *sql.DB, s Session) error {
	_, err := db.Exec(
		`INSERT INTO sessions (id, project_id, name, model, status, parent_id, account_id, card_id, agent_id, kind) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.ProjectID, s.Name, s.Model, s.Status, nullStr(s.ParentID), s.AccountID, s.CardID, s.AgentID, s.Kind,
	)
	return err
}

func GetSession(db *sql.DB, id string) (Session, error) {
	var s Session
	var parentID sql.NullString
	var createdAt, lastActive string
	err := db.QueryRow(
		`SELECT id, project_id, name, model, status, parent_id, message_count, created_at, last_active, account_id, context_usage, card_id, agent_id, kind FROM sessions WHERE id=?`, id,
	).Scan(&s.ID, &s.ProjectID, &s.Name, &s.Model, &s.Status, &parentID, &s.MessageCount, &createdAt, &lastActive, &s.AccountID, &s.ContextUsage, &s.CardID, &s.AgentID, &s.Kind)
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
		`SELECT id, project_id, name, model, status, parent_id, message_count, created_at, last_active, account_id, context_usage, card_id, agent_id FROM sessions WHERE project_id=? AND kind<>? ORDER BY last_active DESC`,
		projectID, SessionKindOrchestrator,
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
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Model, &s.Status, &parentID, &s.MessageCount, &createdAt, &lastActive, &s.AccountID, &s.ContextUsage, &s.CardID, &s.AgentID); err != nil {
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

// UpdateSessionContextUsage persists the last known context window breakdown for a session.
func UpdateSessionContextUsage(db *sql.DB, id, usageJSON string) error {
	_, err := db.Exec(`UPDATE sessions SET context_usage=? WHERE id=?`, usageJSON, id)
	return err
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

// DetachCardSessions unlinks every session of a task and returns their ids. The
// sessions stay as plain chats (history); the task gets a fresh one the next time
// it runs or is opened. Sessions left waiting on an approval go back to idle.
func DetachCardSessions(db *sql.DB, cardID string) ([]string, error) {
	rows, err := db.Query(`SELECT id FROM sessions WHERE card_id=?`, cardID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE sessions SET card_id='', status=CASE WHEN status='waiting_hook' THEN 'idle' ELSE status END WHERE card_id=?`, cardID); err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteCardErrorComments removes the error notes of a task (they describe a
// session that is being replaced).
func DeleteCardErrorComments(db *sql.DB, cardID string) error {
	_, err := db.Exec(`DELETE FROM card_comments WHERE card_id=? AND kind='error'`, cardID)
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

// InsertMessageWithTime inserts a message preserving its original created_at.
// Use this for imports. Does NOT update message_count.
func InsertMessageWithTime(db *sql.DB, m Message) error {
	ts := m.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	_, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, content, seq, created_at) VALUES (?,?,?,?,?,?)`,
		m.ID, m.SessionID, m.Role, m.Content, m.Seq, ts,
	)
	return err
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

func UpdateMessageContent(db *sql.DB, messageID, content string) error {
	_, err := db.Exec(
		`UPDATE messages SET content=? WHERE id=?`,
		content, messageID,
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
	SessionID string    `json:"session_id"`
	ToolUseID string    `json:"tool_use_id"`
	ToolName  string    `json:"tool_name"`
	ToolInput string    `json:"tool_input"`
	CreatedAt time.Time `json:"created_at"`
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
	rows, err := db.Query(`SELECT session_id, tool_use_id, tool_name, tool_input, created_at FROM pending_hooks WHERE session_id=? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingHook
	for rows.Next() {
		var h PendingHook
		var createdAt string
		if err := rows.Scan(&h.SessionID, &h.ToolUseID, &h.ToolName, &h.ToolInput, &createdAt); err != nil {
			return nil, err
		}
		h.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
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

// ClearOrphanedApprovals drops every pending approval and puts the sessions that
// were waiting on one back to idle. Call it at startup only: approvals are held in
// memory, so after a restart none of them can be answered any more. It returns the
// number of sessions that were reset.
func ClearOrphanedApprovals(db *sql.DB) (int64, error) {
	if _, err := db.Exec(`DELETE FROM pending_hooks`); err != nil {
		return 0, err
	}
	res, err := db.Exec(`UPDATE sessions SET status='idle' WHERE status='waiting_hook'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// ---- ClaudeAccount ----

type ClaudeAccount struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ClaudeConfigDir string    `json:"claude_config_dir"`
	AccessToken     string    `json:"access_token,omitempty"`
	RefreshToken    string    `json:"refresh_token,omitempty"`
	ExpiresAt       int64     `json:"expires_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func InsertClaudeAccount(db *sql.DB, a ClaudeAccount) error {
	_, err := db.Exec(
		`INSERT INTO claude_accounts (id, name, claude_config_dir, access_token, refresh_token, expires_at) VALUES (?,?,?,?,?,?)`,
		a.ID, a.Name, a.ClaudeConfigDir, a.AccessToken, a.RefreshToken, a.ExpiresAt,
	)
	return err
}

func ListClaudeAccounts(db *sql.DB) ([]ClaudeAccount, error) {
	rows, err := db.Query(`SELECT id, name, claude_config_dir, access_token, refresh_token, expires_at, created_at FROM claude_accounts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeAccount
	for rows.Next() {
		var a ClaudeAccount
		var createdAt string
		if err := rows.Scan(&a.ID, &a.Name, &a.ClaudeConfigDir, &a.AccessToken, &a.RefreshToken, &a.ExpiresAt, &createdAt); err != nil {
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

func GetClaudeAccount(db *sql.DB, id string) (ClaudeAccount, error) {
	var a ClaudeAccount
	var createdAt string
	err := db.QueryRow(
		`SELECT id, name, claude_config_dir, access_token, refresh_token, expires_at, created_at FROM claude_accounts WHERE id=?`, id,
	).Scan(&a.ID, &a.Name, &a.ClaudeConfigDir, &a.AccessToken, &a.RefreshToken, &a.ExpiresAt, &createdAt)
	if err != nil {
		return a, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return a, nil
}

func DeleteClaudeAccount(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM claude_accounts WHERE id=?`, id)
	return err
}

// UpdateClaudeAccountCredentials updates the stored OAuth tokens for an account.
func UpdateClaudeAccountCredentials(db *sql.DB, id, accessToken, refreshToken string, expiresAt int64) error {
	_, err := db.Exec(
		`UPDATE claude_accounts SET access_token=?, refresh_token=?, expires_at=? WHERE id=?`,
		accessToken, refreshToken, expiresAt, id,
	)
	return err
}

// ReadCredentials reads OAuth credentials from {claudeConfigDir}/.credentials.json.
// Returns subscriptionType, accessToken, refreshToken, expiresAt (Unix ms), and any error.
func ReadCredentials(claudeConfigDir string) (sub, access, refresh string, expiresAt int64, err error) {
	path := claudeConfigDir + "/.credentials.json"
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresAt        int64  `json:"expiresAt"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if err = json.Unmarshal(data, &creds); err != nil {
		return
	}
	sub = creds.ClaudeAiOauth.SubscriptionType
	access = creds.ClaudeAiOauth.AccessToken
	refresh = creds.ClaudeAiOauth.RefreshToken
	expiresAt = creds.ClaudeAiOauth.ExpiresAt
	return
}

// ---- User ----

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
}

func InsertUser(db *sql.DB, u User) error {
	_, err := db.Exec(
		`INSERT INTO users (id, username, password_hash, is_admin) VALUES (?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, boolToInt(u.IsAdmin),
	)
	return err
}

func GetUserByUsername(db *sql.DB, username string) (User, error) {
	var u User
	var createdAt string
	var isAdmin int
	err := db.QueryRow(
		`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username=?`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &createdAt)
	if err != nil {
		return u, err
	}
	u.IsAdmin = isAdmin != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return u, nil
}

func GetUserByID(db *sql.DB, id string) (User, error) {
	var u User
	var createdAt string
	var isAdmin int
	err := db.QueryRow(
		`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &createdAt)
	if err != nil {
		return u, err
	}
	u.IsAdmin = isAdmin != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return u, nil
}

func CountUsers(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---- AuthSession ----

type AuthSession struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func InsertAuthSession(db *sql.DB, s AuthSession) error {
	_, err := db.Exec(
		`INSERT INTO sessions_auth (token, user_id, expires_at) VALUES (?,?,?)`,
		s.Token, s.UserID, s.ExpiresAt.UTC().Format(time.RFC3339),
	)
	return err
}

func GetAuthSession(db *sql.DB, token string) (AuthSession, error) {
	var s AuthSession
	var createdAt, expiresAt string
	err := db.QueryRow(
		`SELECT token, user_id, created_at, expires_at FROM sessions_auth WHERE token=?`, token,
	).Scan(&s.Token, &s.UserID, &createdAt, &expiresAt)
	if err != nil {
		return s, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	s.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	return s, nil
}

func DeleteAuthSession(db *sql.DB, token string) error {
	_, err := db.Exec(`DELETE FROM sessions_auth WHERE token=?`, token)
	return err
}

// ---- APIKey ----

type APIKey struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	KeyHash   string    `json:"-"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
}

func InsertAPIKey(db *sql.DB, k APIKey) error {
	_, err := db.Exec(
		`INSERT INTO api_keys (id, user_id, name, key_hash, prefix) VALUES (?,?,?,?,?)`,
		k.ID, k.UserID, k.Name, k.KeyHash, k.Prefix,
	)
	return err
}

func ListAPIKeys(db *sql.DB, userID string) ([]APIKey, error) {
	rows, err := db.Query(
		`SELECT id, user_id, name, prefix, created_at FROM api_keys WHERE user_id=? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var createdAt string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &createdAt); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, k)
	}
	return out, rows.Err()
}

func GetAPIKeyByHash(db *sql.DB, keyHash string) (APIKey, error) {
	var k APIKey
	var createdAt string
	err := db.QueryRow(
		`SELECT id, user_id, name, key_hash, prefix, created_at FROM api_keys WHERE key_hash=?`, keyHash,
	).Scan(&k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.Prefix, &createdAt)
	if err != nil {
		return k, err
	}
	k.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return k, nil
}

func DeleteAPIKey(db *sql.DB, id, userID string) error {
	_, err := db.Exec(`DELETE FROM api_keys WHERE id=? AND user_id=?`, id, userID)
	return err
}

// ---- TurnUsage ----

type TurnUsage struct {
	ID                       string    `json:"id"`
	SessionID                string    `json:"session_id"`
	ProjectID                string    `json:"project_id"`
	AccountID                string    `json:"account_id"`
	Model                    string    `json:"model"`
	InputTokens              int       `json:"input_tokens"`
	OutputTokens             int       `json:"output_tokens"`
	CacheCreationInputTokens int       `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int       `json:"cache_read_input_tokens"`
	CreatedAt                time.Time `json:"created_at"`
}

func InsertTurnUsage(db *sql.DB, u TurnUsage) error {
	_, err := db.Exec(
		`INSERT INTO turn_usage (id, session_id, project_id, account_id, model,
		 input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		u.ID, u.SessionID, u.ProjectID, u.AccountID, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens,
	)
	return err
}

// UpdateToolExecutionTokens embeds turn-level token usage into tool_execution messages
// that belong to the current turn (seq >= minSeq). This lets the chat history show
// token counts even after page refresh.
func UpdateToolExecutionTokens(db *sql.DB, sessID string, minSeq int, u TurnUsage) error {
	rows, err := db.Query(
		`SELECT id, content FROM messages WHERE session_id=? AND role='tool_execution' AND seq>=?`,
		sessID, minSeq,
	)
	if err != nil {
		return err
	}
	type row struct {
		id      string
		content string
	}
	var msgs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content); err != nil {
			rows.Close()
			return err
		}
		msgs = append(msgs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	usageJSON, _ := json.Marshal(map[string]int{
		"input_tokens":                u.InputTokens,
		"output_tokens":               u.OutputTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens,
		"cache_read_input_tokens":     u.CacheReadInputTokens,
	})

	for _, m := range msgs {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(m.content), &obj); err != nil {
			continue
		}
		obj["usage"] = json.RawMessage(usageJSON)
		updated, _ := json.Marshal(obj)
		db.Exec(`UPDATE messages SET content=? WHERE id=?`, string(updated), m.id)
	}
	return nil
}

// DailyUsageRow aggregates token usage grouped by date and model.
type DailyUsageRow struct {
	Date                     string `json:"date"`
	Model                    string `json:"model"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	TurnCount                int    `json:"turn_count"`
}

func AggregateDailyUsage(db *sql.DB, since time.Time) ([]DailyUsageRow, error) {
	rows, err := db.Query(`
SELECT date(created_at) as day, model,
       SUM(input_tokens), SUM(output_tokens),
       SUM(cache_creation_input_tokens), SUM(cache_read_input_tokens),
       COUNT(*) as turn_count
FROM turn_usage
WHERE created_at >= ?
GROUP BY day, model
ORDER BY day`, since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyUsageRow
	for rows.Next() {
		var r DailyUsageRow
		if err := rows.Scan(&r.Date, &r.Model, &r.InputTokens, &r.OutputTokens,
			&r.CacheCreationInputTokens, &r.CacheReadInputTokens, &r.TurnCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModelUsageRow aggregates token usage grouped by model.
type ModelUsageRow struct {
	Model                    string `json:"model"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	TurnCount                int    `json:"turn_count"`
}

func AggregateModelUsage(db *sql.DB, since time.Time) ([]ModelUsageRow, error) {
	rows, err := db.Query(`
SELECT model,
       SUM(input_tokens), SUM(output_tokens),
       SUM(cache_creation_input_tokens), SUM(cache_read_input_tokens),
       COUNT(*) as turn_count
FROM turn_usage
WHERE created_at >= ?
GROUP BY model
ORDER BY SUM(input_tokens + output_tokens) DESC`, since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsageRow
	for rows.Next() {
		var r ModelUsageRow
		if err := rows.Scan(&r.Model, &r.InputTokens, &r.OutputTokens,
			&r.CacheCreationInputTokens, &r.CacheReadInputTokens, &r.TurnCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionUsageRow aggregates usage per session.
type SessionUsageRow struct {
	SessionID                string  `json:"session_id"`
	SessionName              string  `json:"session_name"`
	ProjectID                string  `json:"project_id"`
	Model                    string  `json:"model"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	TurnCount                int     `json:"turn_count"`
	DurationMinutes          float64 `json:"duration_minutes"`
	CreatedAt                string  `json:"created_at"`
}

func AggregateSessionUsage(db *sql.DB, since time.Time, projectID string) ([]SessionUsageRow, error) {
	query := `
SELECT tu.session_id, s.name, tu.project_id, tu.model,
       SUM(tu.input_tokens), SUM(tu.output_tokens),
       SUM(tu.cache_creation_input_tokens), SUM(tu.cache_read_input_tokens),
       COUNT(*) as turn_count,
       ROUND((julianday(s.last_active) - julianday(s.created_at)) * 1440, 1) as duration_minutes,
       s.created_at
FROM turn_usage tu
JOIN sessions s ON s.id = tu.session_id
WHERE tu.created_at >= ?`
	args := []any{since.Format(time.RFC3339)}
	if projectID != "" {
		query += ` AND tu.project_id = ?`
		args = append(args, projectID)
	}
	query += ` GROUP BY tu.session_id ORDER BY s.created_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionUsageRow
	for rows.Next() {
		var r SessionUsageRow
		if err := rows.Scan(&r.SessionID, &r.SessionName, &r.ProjectID, &r.Model,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.TurnCount, &r.DurationMinutes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	Type      string `json:"type"` // "project" | "session" | "message"
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`   // project name, session name, or message snippet
	Snippet   string `json:"snippet"` // matched text excerpt (messages only)
	Seq       int    `json:"seq"`     // message sequence number (messages only)
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

// ---- UserTool ----

type UserTool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  string `json:"input_schema"`
	Code         string `json:"code"`
	ExecutionEnv string `json:"execution_env"` // "browser" or "orchestrator"
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func InsertUserTool(db *sql.DB, t UserTool) error {
	_, err := db.Exec(
		`INSERT INTO user_tools (id, name, description, input_schema, code, execution_env) VALUES (?,?,?,?,?,?)`,
		t.ID, t.Name, t.Description, t.InputSchema, t.Code, t.ExecutionEnv,
	)
	return err
}

func ListUserTools(db *sql.DB) ([]UserTool, error) {
	rows, err := db.Query(`SELECT id, name, description, input_schema, code, execution_env, created_at, updated_at FROM user_tools ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserTool
	for rows.Next() {
		var t UserTool
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.InputSchema, &t.Code, &t.ExecutionEnv, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetUserTool(db *sql.DB, id string) (UserTool, error) {
	var t UserTool
	err := db.QueryRow(
		`SELECT id, name, description, input_schema, code, execution_env, created_at, updated_at FROM user_tools WHERE id=?`, id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.InputSchema, &t.Code, &t.ExecutionEnv, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func GetUserToolByName(db *sql.DB, name string) (UserTool, error) {
	var t UserTool
	err := db.QueryRow(
		`SELECT id, name, description, input_schema, code, execution_env, created_at, updated_at FROM user_tools WHERE name=?`, name,
	).Scan(&t.ID, &t.Name, &t.Description, &t.InputSchema, &t.Code, &t.ExecutionEnv, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func UpdateUserTool(db *sql.DB, t UserTool) error {
	_, err := db.Exec(
		`UPDATE user_tools SET name=?, description=?, input_schema=?, code=?, execution_env=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		t.Name, t.Description, t.InputSchema, t.Code, t.ExecutionEnv, t.ID,
	)
	return err
}

func DeleteUserTool(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM user_tools WHERE id=?`, id)
	return err
}

// ---- RawEvent ----

type RawEvent struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	TurnIndex int             `json:"turn_index"`
	Event     json.RawMessage `json:"event"`
	CreatedAt time.Time       `json:"created_at"`
}

func InsertRawEvent(db *sql.DB, e RawEvent) error {
	_, err := db.Exec(
		`INSERT INTO raw_events (id, session_id, turn_index, event) VALUES (?,?,?,?)`,
		e.ID, e.SessionID, e.TurnIndex, string(e.Event),
	)
	return err
}

func ListRawEvents(db *sql.DB, sessionID string) ([]RawEvent, error) {
	rows, err := db.Query(
		`SELECT id, session_id, turn_index, event, created_at FROM raw_events WHERE session_id=? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RawEvent
	for rows.Next() {
		var e RawEvent
		var evStr string
		var ts string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TurnIndex, &evStr, &ts); err != nil {
			return nil, err
		}
		e.Event = json.RawMessage(evStr)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Agent ----

type Agent struct {
	ID string `json:"id"`
	// ProjectID is the project the agent belongs to; "" means a global agent,
	// available in every project (see Scope).
	ProjectID   string `json:"project_id"`
	Scope       string `json:"scope"` // "project" | "global" (derived from ProjectID)
	Name        string `json:"name"`
	Model       string `json:"model"`
	AccountID   string `json:"account_id"`
	Personality string `json:"personality"`
	// Tools are the built-in Claude Code tools the agent may use (empty = all).
	Tools []string `json:"tools"`
	// LucodeTools are the lucode tools (task_*, agent_list, send_to_agent,
	// task_info) the agent has. nil in memory means all of them; an empty list
	// means none.
	LucodeTools []string  `json:"lucode_tools"`
	Executor    string    `json:"executor"`   // "aiapi" | "claude"
	Persistent  bool      `json:"persistent"` // keep the claude -p process alive between turns
	ReportsTo   string    `json:"reports_to"` // id of the supervising agent ("" = none)
	// RunnerID and AIProfileID override the project's environment for this agent
	// ("" = use the project's). The AI belongs to the runner it is set with.
	RunnerID    string    `json:"runner_id"`
	AIProfileID string    `json:"ai_profile_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DefaultLucodeTools is every lucode tool; new agents start with all of them.
var DefaultLucodeTools = []string{"task_comment", "task_done", "task_blocked", "agent_list", "send_to_agent", "task_info", "get_task_image", "task_list", "task_create", "task_update", "agent_create", "agent_update", "agent_delete", "project_info"}

const agentCols = `id, project_id, name, model, account_id, personality, tools, lucode_tools, executor, persistent, reports_to, runner_id, ai_profile_id, created_at, updated_at`

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var toolsJSON, lucodeJSON, createdAt, updatedAt string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Model, &a.AccountID, &a.Personality, &toolsJSON, &lucodeJSON,
		&a.Executor, &a.Persistent, &a.ReportsTo, &a.RunnerID, &a.AIProfileID, &createdAt, &updatedAt); err != nil {
		return a, err
	}
	json.Unmarshal([]byte(toolsJSON), &a.Tools)
	if err := json.Unmarshal([]byte(lucodeJSON), &a.LucodeTools); err != nil || a.LucodeTools == nil {
		a.LucodeTools = append([]string{}, DefaultLucodeTools...)
	}
	a.Scope = "project"
	if a.ProjectID == "" {
		a.Scope = "global"
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return a, nil
}

func lucodeToolsJSON(tools []string) string {
	if tools == nil {
		tools = DefaultLucodeTools
	}
	b, _ := json.Marshal(tools)
	return string(b)
}

func InsertAgent(db *sql.DB, a Agent) error {
	tools, _ := json.Marshal(a.Tools)
	_, err := db.Exec(
		`INSERT INTO agents (id, project_id, name, model, account_id, personality, tools, lucode_tools, executor, persistent, reports_to, runner_id, ai_profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ProjectID, a.Name, a.Model, a.AccountID, a.Personality, string(tools), lucodeToolsJSON(a.LucodeTools), a.Executor, a.Persistent, a.ReportsTo, a.RunnerID, a.AIProfileID,
	)
	return err
}

// ListAgents returns the agents available in a project: its own plus the global ones.
func ListAgents(db *sql.DB, projectID string) ([]Agent, error) {
	rows, err := db.Query(
		`SELECT `+agentCols+` FROM agents WHERE project_id=? OR project_id='' ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func GetAgent(db *sql.DB, id string) (Agent, error) {
	return scanAgent(db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, id))
}

func UpdateAgent(db *sql.DB, a Agent) error {
	tools, _ := json.Marshal(a.Tools)
	_, err := db.Exec(
		`UPDATE agents SET project_id=?, name=?, model=?, account_id=?, personality=?, tools=?, lucode_tools=?, executor=?, persistent=?, reports_to=?, runner_id=?, ai_profile_id=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		a.ProjectID, a.Name, a.Model, a.AccountID, a.Personality, string(tools), lucodeToolsJSON(a.LucodeTools), a.Executor, a.Persistent, a.ReportsTo, a.RunnerID, a.AIProfileID, a.ID,
	)
	return err
}

func DeleteAgent(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM agents WHERE id=?`, id)
	return err
}

// ---- KanbanList ----

type KanbanList struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	Kind      string    `json:"kind"` // workflow role, see the Kanban* constants
	CreatedAt time.Time `json:"created_at"`
}

// Workflow roles of the default kanban lists.
const (
	KanbanBacklog    = "backlog"
	KanbanTodo       = "todo"
	KanbanInProgress = "inprogress"
	KanbanDone       = "done"
	KanbanBlocked    = "blocked"
)

func InsertKanbanList(db *sql.DB, l KanbanList) error {
	_, err := db.Exec(
		`INSERT INTO kanban_lists (id, project_id, name, position, kind) VALUES (?,?,?,?,?)`,
		l.ID, l.ProjectID, l.Name, l.Position, l.Kind,
	)
	return err
}

// EnsureDefaultKanbanLists creates Backlog / Todo / In Progress / Done / Blocked
// when a project has no lists yet.
func EnsureDefaultKanbanLists(db *sql.DB, projectID string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kanban_lists WHERE project_id=?`, projectID).Scan(&n); err != nil || n > 0 {
		return err
	}
	defaults := []struct{ name, kind string }{
		{"Backlog", KanbanBacklog},
		{"Todo", KanbanTodo},
		{"In Progress", KanbanInProgress},
		{"Done", KanbanDone},
		{"Blocked", KanbanBlocked},
	}
	for i, d := range defaults {
		if err := InsertKanbanList(db, KanbanList{ID: uuid.New().String(), ProjectID: projectID, Name: d.name, Position: i, Kind: d.kind}); err != nil {
			return err
		}
	}
	return nil
}

// FindKanbanListByKind returns the project's list with the given workflow role.
func FindKanbanListByKind(db *sql.DB, projectID, kind string) (KanbanList, error) {
	var id string
	if err := db.QueryRow(`SELECT id FROM kanban_lists WHERE project_id=? AND kind=? ORDER BY position ASC LIMIT 1`, projectID, kind).Scan(&id); err != nil {
		return KanbanList{}, err
	}
	return GetKanbanList(db, id)
}

func ListKanbanLists(db *sql.DB, projectID string) ([]KanbanList, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, position, kind, created_at FROM kanban_lists WHERE project_id=? ORDER BY position ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KanbanList
	for rows.Next() {
		var l KanbanList
		var ts string
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Position, &l.Kind, &ts); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, l)
	}
	return out, rows.Err()
}

func GetKanbanList(db *sql.DB, id string) (KanbanList, error) {
	var l KanbanList
	var ts string
	err := db.QueryRow(
		`SELECT id, project_id, name, position, kind, created_at FROM kanban_lists WHERE id=?`, id,
	).Scan(&l.ID, &l.ProjectID, &l.Name, &l.Position, &l.Kind, &ts)
	if err != nil {
		return l, err
	}
	l.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
	return l, nil
}

func UpdateKanbanList(db *sql.DB, id, name string, position int) error {
	_, err := db.Exec(`UPDATE kanban_lists SET name=?, position=? WHERE id=?`, name, position, id)
	return err
}

func DeleteKanbanList(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM kanban_lists WHERE id=?`, id)
	return err
}

// ---- KanbanCard ----

type KanbanCard struct {
	ID          string    `json:"id"`
	ListID      string    `json:"list_id"`
	ProjectID   string    `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CardType    string    `json:"card_type"`
	Tags        []string  `json:"tags"`
	AgentID     string    `json:"agent_id"`
	DependsOn   []string  `json:"depends_on"`
	StartDate   string    `json:"start_date"`
	EndDate     string    `json:"end_date"`
	StartedAt   string    `json:"started_at"`  // real start: first time the card entered In Progress
	FinishedAt  string    `json:"finished_at"` // real end: when the card entered Done
	Position    int       `json:"position"`
	Archived    bool      `json:"archived"`
	ParentID    string    `json:"parent_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func scanKanbanCard(row interface{ Scan(...any) error }) (KanbanCard, error) {
	var c KanbanCard
	var tagsJSON, dependsJSON, createdAt, updatedAt string
	var archived int
	err := row.Scan(
		&c.ID, &c.ListID, &c.ProjectID, &c.Title, &c.Description,
		&c.CardType, &tagsJSON, &c.AgentID, &dependsJSON,
		&c.StartDate, &c.EndDate, &c.StartedAt, &c.FinishedAt, &c.Position, &archived,
		&c.ParentID, &createdAt, &updatedAt,
	)
	if err != nil {
		return c, err
	}
	json.Unmarshal([]byte(tagsJSON), &c.Tags)
	json.Unmarshal([]byte(dependsJSON), &c.DependsOn)
	c.Archived = archived != 0
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return c, nil
}

const kanbanCardCols = `id, list_id, project_id, title, description, card_type, tags, agent_id, depends_on, start_date, end_date, started_at, finished_at, position, archived, parent_id, created_at, updated_at`

func InsertKanbanCard(db *sql.DB, c KanbanCard) error {
	tags, _ := json.Marshal(c.Tags)
	depends, _ := json.Marshal(c.DependsOn)
	_, err := db.Exec(
		`INSERT INTO kanban_cards (id, list_id, project_id, title, description, card_type, tags, agent_id, depends_on, start_date, end_date, position, archived, parent_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ListID, c.ProjectID, c.Title, c.Description, c.CardType,
		string(tags), c.AgentID, string(depends),
		c.StartDate, c.EndDate, c.Position, boolToInt(c.Archived), c.ParentID,
	)
	return err
}

func ListKanbanCards(db *sql.DB, projectID string, includeArchived bool) ([]KanbanCard, error) {
	q := `SELECT ` + kanbanCardCols + ` FROM kanban_cards WHERE project_id=?`
	if !includeArchived {
		q += ` AND archived=0`
	}
	q += ` ORDER BY list_id, position ASC`
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KanbanCard
	for rows.Next() {
		c, err := scanKanbanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetKanbanCard(db *sql.DB, id string) (KanbanCard, error) {
	row := db.QueryRow(`SELECT `+kanbanCardCols+` FROM kanban_cards WHERE id=?`, id)
	return scanKanbanCard(row)
}

func UpdateKanbanCard(db *sql.DB, c KanbanCard) error {
	tags, _ := json.Marshal(c.Tags)
	depends, _ := json.Marshal(c.DependsOn)
	stampKanbanCardDates(db, &c)
	_, err := db.Exec(`UPDATE kanban_cards SET
		list_id=?, title=?, description=?, card_type=?, tags=?, agent_id=?,
		depends_on=?, start_date=?, end_date=?, started_at=?, finished_at=?, position=?, archived=?, parent_id=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=?`,
		c.ListID, c.Title, c.Description, c.CardType, string(tags), c.AgentID,
		string(depends), c.StartDate, c.EndDate, c.StartedAt, c.FinishedAt, c.Position, boolToInt(c.Archived),
		c.ParentID, c.ID,
	)
	return err
}

// stampKanbanCardDates keeps the real dates: entering an In Progress list starts the
// card (first time only, and reopens it), entering Done finishes it, and leaving Done
// clears the finish.
func stampKanbanCardDates(db *sql.DB, c *KanbanCard) {
	var oldList string
	db.QueryRow(`SELECT list_id FROM kanban_cards WHERE id=?`, c.ID).Scan(&oldList)
	if oldList == c.ListID {
		return
	}
	var kind string
	db.QueryRow(`SELECT kind FROM kanban_lists WHERE id=?`, c.ListID).Scan(&kind)
	now := time.Now().UTC().Format(time.RFC3339)
	switch kind {
	case KanbanInProgress:
		if c.StartedAt == "" {
			c.StartedAt = now
		}
		c.FinishedAt = ""
	case KanbanDone:
		if c.StartedAt == "" {
			c.StartedAt = now
		}
		c.FinishedAt = now
	default:
		c.FinishedAt = ""
	}
}

func DeleteKanbanCard(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM kanban_cards WHERE id=?`, id)
	return err
}

// ---- Session Permission Rule ----

type SessionRuleRow struct {
	ID         string
	SessionID  string
	Tool       string
	InputRegex string
	Scope      string
	CreatedAt  time.Time
}

func InsertSessionRule(db *sql.DB, r SessionRuleRow) error {
	_, err := db.Exec(
		`INSERT INTO session_permissions (id, session_id, tool, input_regex, scope, created_at) VALUES (?,?,?,?,?,?)`,
		r.ID, r.SessionID, r.Tool, r.InputRegex, r.Scope, r.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func ListSessionRules(db *sql.DB, sessID string) ([]SessionRuleRow, error) {
	rows, err := db.Query(
		`SELECT id, session_id, tool, input_regex, scope, created_at FROM session_permissions WHERE session_id=? ORDER BY created_at ASC`,
		sessID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRuleRow
	for rows.Next() {
		var r SessionRuleRow
		var createdAt string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Tool, &r.InputRegex, &r.Scope, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func DeleteSessionRule(db *sql.DB, sessID, ruleID string) error {
	_, err := db.Exec(`DELETE FROM session_permissions WHERE id=? AND session_id=?`, ruleID, sessID)
	return err
}

// ---- Task sessions ----

// GetSessionByCard returns the session that belongs to a kanban card (task).
func GetSessionByCard(db *sql.DB, cardID string) (Session, error) {
	var id string
	if err := db.QueryRow(`SELECT id FROM sessions WHERE card_id=? ORDER BY created_at ASC LIMIT 1`, cardID).Scan(&id); err != nil {
		return Session{}, err
	}
	return GetSession(db, id)
}

// ListSessionsByAgent returns the task sessions an agent has run, newest first.
func ListSessionsByAgent(db *sql.DB, agentID string) ([]Session, error) {
	rows, err := db.Query(`SELECT id FROM sessions WHERE agent_id=? ORDER BY last_active DESC`, agentID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Session{}
	for _, id := range ids {
		if s, err := GetSession(db, id); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// UpdateSessionAgent reassigns a task session to another agent (and its model).
func UpdateSessionAgent(db *sql.DB, id, agentID, model string) error {
	_, err := db.Exec(`UPDATE sessions SET agent_id=?, model=? WHERE id=?`, agentID, model, id)
	return err
}

// backfillKanbanListKinds assigns a workflow role to existing lists whose name
// matches one of the defaults (case/space/dash-insensitive). Idempotent.
func backfillKanbanListKinds(db *sql.DB) {
	rows, err := db.Query(`SELECT id, name FROM kanban_lists WHERE kind=''`)
	if err != nil {
		return
	}
	type row struct{ id, name string }
	var todo []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.name) == nil {
			todo = append(todo, r)
		}
	}
	rows.Close()
	for _, r := range todo {
		key := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(r.name))
		switch key {
		case "backlog", "todo", "inprogress", "done", "blocked":
			db.Exec(`UPDATE kanban_lists SET kind=? WHERE id=?`, key, r.id)
		}
	}
}

// ---- Card images ----

// CardImage is a picture attached to a task. Name is how the description refers
// to it ("image1" for @image1).
type CardImage struct {
	CardID    string `json:"card_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"-"`
}

// InsertCardImage stores a picture and returns it with the next free name
// (image<N>, N one more than the highest in use).
func InsertCardImage(db *sql.DB, cardID, mediaType string, data []byte) (CardImage, error) {
	names, err := ListCardImageNames(db, cardID)
	if err != nil {
		return CardImage{}, err
	}
	next := 1
	for _, n := range names {
		var i int
		if _, err := fmt.Sscanf(n, "image%d", &i); err == nil && i >= next {
			next = i + 1
		}
	}
	img := CardImage{CardID: cardID, Name: fmt.Sprintf("image%d", next), MediaType: mediaType, Data: data}
	_, err = db.Exec(`INSERT INTO card_images (card_id, name, media_type, data) VALUES (?,?,?,?)`,
		img.CardID, img.Name, img.MediaType, img.Data)
	return img, err
}

func ListCardImageNames(db *sql.DB, cardID string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM card_images WHERE card_id=? ORDER BY created_at ASC, rowid ASC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func ListCardImages(db *sql.DB, cardID string) ([]CardImage, error) {
	rows, err := db.Query(`SELECT card_id, name, media_type, data FROM card_images WHERE card_id=? ORDER BY created_at ASC, rowid ASC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardImage{}
	for rows.Next() {
		var im CardImage
		if err := rows.Scan(&im.CardID, &im.Name, &im.MediaType, &im.Data); err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

func GetCardImage(db *sql.DB, cardID, name string) (CardImage, error) {
	var im CardImage
	err := db.QueryRow(`SELECT card_id, name, media_type, data FROM card_images WHERE card_id=? AND name=?`, cardID, name).
		Scan(&im.CardID, &im.Name, &im.MediaType, &im.Data)
	return im, err
}

func DeleteCardImage(db *sql.DB, cardID, name string) error {
	_, err := db.Exec(`DELETE FROM card_images WHERE card_id=? AND name=?`, cardID, name)
	return err
}

// ---- Card comments ----

// CardComment is a note on a task. Kind is "comment", "done" or "blocked".
type CardComment struct {
	ID        string    `json:"id"`
	CardID    string    `json:"card_id"`
	SessionID string    `json:"session_id,omitempty"`
	Author    string    `json:"author"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func InsertCardComment(db *sql.DB, c CardComment) error {
	_, err := db.Exec(
		`INSERT INTO card_comments (id, card_id, session_id, author, kind, body) VALUES (?,?,?,?,?,?)`,
		c.ID, c.CardID, c.SessionID, c.Author, c.Kind, c.Body,
	)
	return err
}

func ListCardComments(db *sql.DB, cardID string) ([]CardComment, error) {
	rows, err := db.Query(
		`SELECT id, card_id, session_id, author, kind, body, created_at FROM card_comments WHERE card_id=? ORDER BY created_at ASC, rowid ASC`,
		cardID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardComment{}
	for rows.Next() {
		var c CardComment
		var ts string
		if err := rows.Scan(&c.ID, &c.CardID, &c.SessionID, &c.Author, &c.Kind, &c.Body, &ts); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateProjectEnvironment sets the runner and preconfigured AI a project uses.
func UpdateProjectEnvironment(db *sql.DB, id, runnerID, aiProfileID string) error {
	_, err := db.Exec(`UPDATE projects SET runner_id=?, ai_profile_id=? WHERE id=?`, runnerID, aiProfileID, id)
	return err
}

// UpdateProjectEnvDirs sets the work dirs a project uses on each runner.
func UpdateProjectEnvDirs(db *sql.DB, id string, envDirs map[string][]string) error {
	data, _ := json.Marshal(envDirs)
	if envDirs == nil {
		data = []byte("{}")
	}
	_, err := db.Exec(`UPDATE projects SET env_dirs=? WHERE id=?`, string(data), id)
	return err
}

// UnassignAgentCards clears the agent of every task assigned to it (before the agent is deleted).
func UnassignAgentCards(db *sql.DB, agentID string) error {
	_, err := db.Exec(`UPDATE kanban_cards SET agent_id='' WHERE agent_id=?`, agentID)
	return err
}
