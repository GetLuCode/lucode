package db

import (
	"database/sql"
	"encoding/json"
	"log"
)

// BackfillToolUsage fills api_calls and tool_calls from the raw_events of sessions run before the
// runners reported per-call usage, so the cost of tools has history from the first start. It
// runs once (a marker row in seeds) and never touches a session that already has live rows.
//
// raw_events keep the stream of every call: message_start (usage, 1h/5m cache split),
// the tool_use blocks and message_delta (output tokens), each tagged with the api_message_id of
// its call, so the calls are rebuilt without depending on the order the events were inserted in.
// What they lack is the size of each tool result, so parallel results of old sessions are split
// equally. A turn ends in the first call that asks for no tool.
func BackfillToolUsage(db *sql.DB) (int, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS seeds (name TEXT PRIMARY KEY)`); err != nil {
		return 0, err
	}
	var done int
	if err := db.QueryRow(`SELECT COUNT(*) FROM seeds WHERE name='api_calls_backfill'`).Scan(&done); err != nil || done > 0 {
		return 0, err
	}

	rows, err := db.Query(`
SELECT r.session_id, COALESCE(s.project_id, ''), r.created_at, r.event
FROM raw_events r
LEFT JOIN sessions s ON s.id = r.session_id
WHERE (r.event LIKE '%"message_start"%' OR r.event LIKE '%"message_delta"%' OR r.event LIKE '%"tool_use"%')
  AND NOT EXISTS (SELECT 1 FROM api_calls a WHERE a.session_id = r.session_id)
ORDER BY r.session_id, r.created_at, r.rowid`)
	if err != nil {
		return 0, err
	}

	inserted := 0
	var session string
	var evs []rawCallEvent // the session's events, in stored order
	flush := func() error {
		n, err := insertBackfilled(db, rebuildCalls(evs))
		inserted += n
		evs = nil
		return err
	}

	for rows.Next() {
		var sid, pid, createdAt, raw string
		if err := rows.Scan(&sid, &pid, &createdAt, &raw); err != nil {
			rows.Close()
			return inserted, err
		}
		if sid != session {
			if err := flush(); err != nil {
				rows.Close()
				return inserted, err
			}
			session = sid
		}
		var ev struct {
			Event struct {
				ParentToolUseID *string `json:"parent_tool_use_id"`
				APIMessageID    string  `json:"api_message_id"`
				Event           struct {
					Type    string `json:"type"`
					Message struct {
						ID    string `json:"id"`
						Model string `json:"model"`
						Usage struct {
							Input         int `json:"input_tokens"`
							CacheCreation int `json:"cache_creation_input_tokens"`
							CacheRead     int `json:"cache_read_input_tokens"`
							Split         struct {
								OneHour  int `json:"ephemeral_1h_input_tokens"`
								FiveMins int `json:"ephemeral_5m_input_tokens"`
							} `json:"cache_creation"`
						} `json:"usage"`
					} `json:"message"`
					ContentBlock struct {
						Type string `json:"type"`
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"content_block"`
					Usage struct {
						Output int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"event"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		e := ev.Event.Event
		msgID := ev.Event.APIMessageID
		if e.Type == "message_start" && e.Message.ID != "" {
			msgID = e.Message.ID
		}
		if msgID == "" {
			continue
		}
		re := rawCallEvent{kind: e.Type, msgID: msgID, session: sid, project: pid, createdAt: createdAt}
		switch e.Type {
		case "message_start":
			if ev.Event.ParentToolUseID != nil {
				re.parent = *ev.Event.ParentToolUseID
			}
			u := e.Message.Usage
			re.model, re.input, re.cc, re.cr = e.Message.Model, u.Input, u.CacheCreation, u.CacheRead
			re.cc1h, re.cc5m = u.Split.OneHour, u.Split.FiveMins
		case "content_block_start":
			if e.ContentBlock.Type != "tool_use" || e.ContentBlock.ID == "" {
				continue
			}
			re.toolID, re.toolName = e.ContentBlock.ID, e.ContentBlock.Name
		case "message_delta":
			re.output = e.Usage.Output
		default:
			continue
		}
		evs = append(evs, re)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return inserted, err
	}
	if err := flush(); err != nil {
		return inserted, err
	}
	if _, err := db.Exec(`INSERT INTO seeds (name) VALUES ('api_calls_backfill')`); err != nil {
		return inserted, err
	}
	if inserted > 0 {
		log.Printf("[db] backfilled %d API calls from raw_events", inserted)
	}
	return inserted, nil
}

// rawCallEvent is the part of a stored stream event that matters to rebuild a call.
type rawCallEvent struct {
	kind, msgID, session, project, createdAt, parent, model string
	input, cc, cr, cc1h, cc5m, output                       int
	toolID, toolName                                        string
}

// rebuildCalls groups a session's events by call. Events were stored asynchronously, so a
// tool or the output may come before the start of its call: the starts go first.
func rebuildCalls(evs []rawCallEvent) []*APICall {
	var calls []*APICall
	byMsg := map[string]*APICall{}
	for _, e := range evs {
		if e.kind != "message_start" || byMsg[e.msgID] != nil {
			continue
		}
		c := &APICall{
			SessionID: e.session, ProjectID: e.project, Parent: e.parent, Model: e.model, CreatedAt: e.createdAt,
			Input: e.input, CacheCreation: e.cc, CacheRead: e.cr, CacheCreation1h: e.cc1h, CacheCreation5m: e.cc5m,
		}
		byMsg[e.msgID] = c
		calls = append(calls, c)
	}
	for _, e := range evs {
		c := byMsg[e.msgID]
		if c == nil {
			continue
		}
		switch e.kind {
		case "content_block_start":
			c.Tools = append(c.Tools, ToolUse{ID: e.toolID, Name: e.toolName})
		case "message_delta":
			if e.output > 0 {
				c.Output = e.output
			}
		}
	}
	return calls
}

// insertBackfilled stores a session's rebuilt calls, marking which ones open a turn.
func insertBackfilled(db *sql.DB, calls []*APICall) (int, error) {
	hadTools := map[string]bool{} // per agent (parent): did its previous call ask for a tool?
	seen := map[string]bool{}
	n := 0
	for _, c := range calls {
		c.FirstInTurn = c.Parent == "" && (!seen[c.Parent] || !hadTools[c.Parent])
		seen[c.Parent] = true
		hadTools[c.Parent] = len(c.Tools) > 0
		if err := InsertAPICall(db, *c); err != nil {
			// leave nothing half done: a session with rows is skipped by the next try
			db.Exec(`DELETE FROM tool_calls WHERE call_id IN (SELECT id FROM api_calls WHERE session_id=?)`, c.SessionID)
			db.Exec(`DELETE FROM api_calls WHERE session_id=?`, c.SessionID)
			return 0, err
		}
		n++
	}
	return n, nil
}
