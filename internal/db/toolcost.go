package db

import (
	"database/sql"
	"sort"
	"time"

	"github.com/google/uuid"
)

// What each tool costs. A tool call costs, in tokens:
//   - what the model wrote to ask for it (the output of the call that issued it);
//   - its result, which enters the context in the next call (written to the cache once) and is
//     then re-read by every later call of the session (cache reads).
//
// The runner reports every API call's usage and every result's size (never its content);
// the result's tokens are the growth of the context between a call and the next one.

// APICall is one call to the model, as the runner reported it.
type APICall struct {
	// CreatedAt is set only when rebuilding old calls (RFC3339); live calls take the time of arrival.
	CreatedAt                           string
	SessionID, ProjectID, Parent, Model string
	FirstInTurn                         bool
	Input, Output, CacheCreation        int
	CacheRead, CacheCreation1h          int
	CacheCreation5m                     int
	Tools                               []ToolUse
}

// ToolUse is a tool a call asked for.
type ToolUse struct{ ID, Name string }

// InsertAPICall stores a call and the tools it asked for. A tool's result may have been
// reported first; its size is kept.
func InsertAPICall(db *sql.DB, c APICall) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := uuid.New().String()
	// created_at falls back to its default (now) when the call carries no time
	if _, err := tx.Exec(
		`INSERT INTO api_calls (id, session_id, project_id, parent, first_in_turn, model, input_tokens, output_tokens,
		 cache_creation_input_tokens, cache_read_input_tokens, cache_creation_1h_tokens, cache_creation_5m_tokens, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?, COALESCE(NULLIF(?, ''), strftime('%Y-%m-%dT%H:%M:%fZ','now')))`,
		id, c.SessionID, c.ProjectID, c.Parent, boolToInt(c.FirstInTurn), c.Model, c.Input, c.Output,
		c.CacheCreation, c.CacheRead, c.CacheCreation1h, c.CacheCreation5m, c.CreatedAt,
	); err != nil {
		return err
	}
	for _, t := range c.Tools {
		if _, err := tx.Exec(
			`INSERT INTO tool_calls (tool_use_id, session_id, call_id, name) VALUES (?,?,?,?)
			 ON CONFLICT(tool_use_id) DO UPDATE SET call_id=excluded.call_id, name=excluded.name`,
			t.ID, c.SessionID, id, t.Name,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveToolResult stores the size of a tool's result.
func SaveToolResult(db *sql.DB, sessionID, toolUseID string, chars, images int, isError bool) error {
	_, err := db.Exec(
		`INSERT INTO tool_calls (tool_use_id, session_id, has_result, result_chars, result_images, is_error)
		 VALUES (?,?,1,?,?,?)
		 ON CONFLICT(tool_use_id) DO UPDATE SET has_result=1, result_chars=excluded.result_chars,
		   result_images=excluded.result_images, is_error=excluded.is_error`,
		toolUseID, sessionID, chars, images, boolToInt(isError),
	)
	return err
}

// FirstCallUsage is the session's first call to the model: exactly what its initial context
// (system prompt, tools, skills...) billed, written to the cache or read from it. sql.ErrNoRows
// for sessions run before the runners reported per-call usage.
// The second result is how many of the written tokens used the 1-hour cache (billed at twice
// the input price instead of 1.25).
func FirstCallUsage(db *sql.DB, sessionID string) (TurnUsage, int, error) {
	var u TurnUsage
	var written1h int
	err := db.QueryRow(
		`SELECT id, session_id, project_id, model, input_tokens, output_tokens,
		        cache_creation_input_tokens, cache_read_input_tokens, cache_creation_1h_tokens
		 FROM api_calls WHERE session_id=? AND parent='' ORDER BY rowid ASC LIMIT 1`, sessionID,
	).Scan(&u.ID, &u.SessionID, &u.ProjectID, &u.Model, &u.InputTokens, &u.OutputTokens,
		&u.CacheCreationInputTokens, &u.CacheReadInputTokens, &written1h)
	return u, written1h, err
}

// ToolCostRow is what one tool used in one model, in tokens by price class. Prices are the
// caller's: output at the output price, Written* and Uncached at (a multiple of) the input
// price, CarriedRead at the cache read price.
type ToolCostRow struct {
	Tool         string `json:"tool"`
	Model        string `json:"model"`
	Calls        int    `json:"calls"`
	Errors       int    `json:"errors"`
	ResultTokens int    `json:"result_tokens"` // added to the context by the results
	Unmeasured   int    `json:"unmeasured"`    // calls whose result was never measured (last call of a turn)
	OutputTokens int    `json:"output_tokens"` // the model's output to ask for them
	Written1h    int    `json:"written_1h"`    // result tokens written to the cache with a 1h TTL
	Written5m    int    `json:"written_5m"`
	Uncached     int    `json:"uncached"`     // result tokens billed as plain input
	CarriedRead  int    `json:"carried_read"` // result tokens x the calls that re-read them
}

// imageWeight is how many characters of text an image result counts as, to split the growth
// of the context between parallel results (an image is about a thousand tokens).
const imageWeight = 4000

type callRow struct {
	id, parent, model                 string
	first                             bool
	input, output, cc, cr, cc1h, cc5m int
	tools                             []toolRow
}

type toolRow struct {
	name   string
	chars  int
	images int
	has    bool
	err    bool
}

func (c callRow) ctx() int { return c.input + c.cc + c.cr }

// ToolCosts totals, per tool and model, what the tool calls since the given time used.
func ToolCosts(db *sql.DB, since, until time.Time, projectID string) ([]ToolCostRow, error) {
	cond, args := timeCond("created_at", since, until)
	if projectID != "" {
		cond += ` AND project_id = ?`
		args = append(args, projectID)
	}
	return toolCosts(db, cond, args)
}

// SessionToolCosts is ToolCosts for one session: what each tool cost in that task.
func SessionToolCosts(db *sql.DB, sessionID string) ([]ToolCostRow, error) {
	return toolCosts(db, `session_id = ?`, []any{sessionID})
}

func toolCosts(db *sql.DB, cond string, args []any) ([]ToolCostRow, error) {
	q := `SELECT id, session_id, parent, first_in_turn, model, input_tokens, output_tokens,
	      cache_creation_input_tokens, cache_read_input_tokens, cache_creation_1h_tokens, cache_creation_5m_tokens
	      FROM api_calls WHERE ` + cond + ` ORDER BY session_id, rowid`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	// calls per session and per agent (a sub-agent has its own context), in the order they ran
	type key struct{ session, parent string }
	chains := map[key][]*callRow{}
	byID := map[string]*callRow{}
	var order []key
	for rows.Next() {
		var sid string
		var first int
		c := &callRow{}
		if err := rows.Scan(&c.id, &sid, &c.parent, &first, &c.model, &c.input, &c.output, &c.cc, &c.cr, &c.cc1h, &c.cc5m); err != nil {
			rows.Close()
			return nil, err
		}
		c.first = first == 1
		k := key{sid, c.parent}
		if _, ok := chains[k]; !ok {
			order = append(order, k)
		}
		chains[k] = append(chains[k], c)
		byID[c.id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := db.Query(`SELECT call_id, name, has_result, result_chars, result_images, is_error FROM tool_calls WHERE call_id <> ''`)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var callID string
		var t toolRow
		var has, isErr int
		if err := trows.Scan(&callID, &t.name, &has, &t.chars, &t.images, &isErr); err != nil {
			trows.Close()
			return nil, err
		}
		t.has, t.err = has == 1, isErr == 1
		if c := byID[callID]; c != nil {
			c.tools = append(c.tools, t)
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}

	acc := map[[2]string]*ToolCostRow{}
	row := func(tool, model string) *ToolCostRow {
		k := [2]string{tool, model}
		if acc[k] == nil {
			acc[k] = &ToolCostRow{Tool: tool, Model: model}
		}
		return acc[k]
	}
	for _, k := range order {
		chain := chains[k]
		for i, c := range chain {
			n := len(c.tools)
			if n == 0 {
				continue
			}
			// the results are what the context grew by in the next call of the same turn
			var next *callRow
			if i+1 < len(chain) && !chain[i+1].first {
				next = chain[i+1]
			}
			growth := 0
			if next != nil {
				if growth = next.ctx() - c.ctx() - c.output; growth < 0 {
					growth = 0
				}
			}
			var total int
			for _, t := range c.tools {
				total += t.chars + t.images*imageWeight
			}
			for _, t := range c.tools {
				r := row(t.name, c.model)
				r.Calls++
				if t.err {
					r.Errors++
				}
				r.OutputTokens += c.output / n
				if next == nil {
					r.Unmeasured++
					continue
				}
				share := growth / n
				if total > 0 {
					share = growth * (t.chars + t.images*imageWeight) / total
				}
				r.ResultTokens += share
				// how the next call billed the new tokens: written to the cache (1h or 5m) or plain input
				written := share
				if written > next.cc {
					written = next.cc
				}
				if split := next.cc1h + next.cc5m; split > 0 {
					w1h := written * next.cc1h / split
					r.Written1h += w1h
					r.Written5m += written - w1h
				} else {
					r.Written5m += written
				}
				r.Uncached += share - written
				r.CarriedRead += share * (len(chain) - (i + 2)) // every call after the next one re-reads it
			}
		}
	}
	out := make([]ToolCostRow, 0, len(acc))
	for _, r := range acc {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}
