package db

import "database/sql"

// aimode_conversations is what the Go implementation of Google AI Mode left. Nothing writes to it
// any more and nothing removes it: its rows are copied to the plugin's conversations, with their
// ids, so that a conversation moved to another session is not copied again.

const copyAIModeSQL = `INSERT OR IGNORE INTO plugin_conversations
	(id, plugin_id, session_id, project_id, source, url, title, turns, meta, created_at, updated_at)
	SELECT id, ?, session_id, project_id, ?, url, title, turns, '{}', created_at, updated_at FROM aimode_conversations`

// CopyAIModeConversations copies the old rows to a plugin. Running it again adds nothing.
func CopyAIModeConversations(db *sql.DB, pluginID, source string) (int64, error) {
	res, err := db.Exec(copyAIModeSQL, pluginID, source)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CopyAIModeConversationsOnce copies them the first time this hub asks, never again: if the plugin
// is removed with its data, they must not come back.
func CopyAIModeConversationsOnce(db *sql.DB, pluginID, source string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO plugin_seeds (plugin_id) VALUES (?)`, pluginID+"#aimode-conversations")
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return 0, err
	}
	res, err = tx.Exec(copyAIModeSQL, pluginID, source)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
