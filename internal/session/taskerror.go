package session

import (
	"log"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

// noteTaskError records why a turn of a task session failed as an error note on the
// task, so the board can say what went wrong instead of only showing "error".
func (m *Manager) noteTaskError(sessID, msg string) {
	sess, err := dbpkg.GetSession(m.db, sessID)
	if err != nil || sess.CardID == "" {
		return
	}
	if err := dbpkg.InsertCardComment(m.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: sess.CardID, SessionID: sessID, Author: "lucode", Kind: "error", Body: msg,
	}); err != nil {
		log.Printf("[session] error note for %s: %v", sessID, err)
	}
}
