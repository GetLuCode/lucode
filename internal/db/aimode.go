package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// AIModeConversation is a conversation of the local Google AI Mode model: the lucode session
// it belongs to and the url that reopens it in the browser profile that made it.
type AIModeConversation struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	ProjectID string    `json:"project_id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Turns     int       `json:"turns"` // turns the page had when it was saved: how many to wait for on reopening
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const aimodeCols = `id, session_id, project_id, url, title, turns, created_at, updated_at`

func scanAIModeConversation(row interface{ Scan(...any) error }) (AIModeConversation, error) {
	var c AIModeConversation
	var created, updated string
	if err := row.Scan(&c.ID, &c.SessionID, &c.ProjectID, &c.URL, &c.Title, &c.Turns, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return c, nil
}

// SaveAIModeConversation records (or moves to a new url) the conversation of a session. The
// title is kept once it is set: it is the first thing the user asked.
func SaveAIModeConversation(db *sql.DB, sessionID, projectID, url, title string, turns int) error {
	_, err := db.Exec(`
INSERT INTO aimode_conversations (id, session_id, project_id, url, title, turns) VALUES (?,?,?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET
  url = excluded.url,
  turns = excluded.turns,
  title = CASE WHEN aimode_conversations.title = '' THEN excluded.title ELSE aimode_conversations.title END,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		uuid.New().String(), sessionID, projectID, url, title, turns)
	return err
}

func GetAIModeConversationBySession(db *sql.DB, sessionID string) (AIModeConversation, error) {
	return scanAIModeConversation(db.QueryRow(`SELECT `+aimodeCols+` FROM aimode_conversations WHERE session_id=?`, sessionID))
}

func GetAIModeConversation(db *sql.DB, id string) (AIModeConversation, error) {
	return scanAIModeConversation(db.QueryRow(`SELECT `+aimodeCols+` FROM aimode_conversations WHERE id=?`, id))
}

// ListAIModeConversations is the history of a project, the latest first.
func ListAIModeConversations(db *sql.DB, projectID string) ([]AIModeConversation, error) {
	rows, err := db.Query(`SELECT `+aimodeCols+` FROM aimode_conversations WHERE project_id=? ORDER BY updated_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIModeConversation
	for rows.Next() {
		c, err := scanAIModeConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BindAIModeConversation hands a conversation to another lucode session (when its own was deleted).
func BindAIModeConversation(db *sql.DB, id, sessionID string) error {
	_, err := db.Exec(`UPDATE aimode_conversations SET session_id=? WHERE id=?`, sessionID, id)
	return err
}
