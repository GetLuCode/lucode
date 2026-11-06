package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Flows: event-driven automations of a project. flow_events is the log of what
// happened (a card changed list, a turn ended...). It is written in the same
// transaction as the change it describes, so no event is lost and none exists for a
// change that did not happen. internal/flows reads it and runs the published graphs.

// Event types.
const (
	EventCardMoved   = "card.moved"
	EventCardCreated = "card.created"
	EventTurnEnded   = "turn.ended"
	EventTurnFailed  = "turn.failed"
	// The answer to a step that waits on wait_key in the payload.
	EventAIEvaluated     = "ai.evaluated"
	EventApprovalDecided = "approval.decided"
	EventCallDone        = "flow.call_done"
	EventTaskDone        = "task.done"
	// An agent wants to use a tool and no rule decided it. Someone is waiting for the answer.
	EventToolRequested = "tool.requested"
)

// Run and step statuses.
const (
	RunQueued    = "queued" // another run of the same flow is active on the same card
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"

	StepPending   = "pending"
	StepRunning   = "running"
	StepWaiting   = "waiting"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepCancelled = "cancelled"
)

// EventCause says who changed a card. Depth counts how many flows led to the change,
// which is what stops flows from triggering each other forever.
type EventCause struct {
	Source string // "user", "agent", "system" or "flow"
	FlowID string
	RunID  string
	Depth  int
}

func migrateFlows(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS flow_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT NOT NULL,
    type          TEXT NOT NULL,
    card_id       TEXT NOT NULL DEFAULT '',
    session_id    TEXT NOT NULL DEFAULT '',
    payload       TEXT NOT NULL DEFAULT '{}',
    source        TEXT NOT NULL DEFAULT '',
    flow_id       TEXT NOT NULL DEFAULT '',
    run_id        TEXT NOT NULL DEFAULT '',
    depth         INTEGER NOT NULL DEFAULT 0,
    dispatched    INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_flow_events_pending ON flow_events(dispatched, id);
CREATE INDEX IF NOT EXISTS idx_flow_events_card ON flow_events(card_id, id);

CREATE TABLE IF NOT EXISTS flows (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    name              TEXT NOT NULL,
    draft             TEXT NOT NULL DEFAULT '{}',
    published_version INTEGER NOT NULL DEFAULT 0,
    enabled           INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_flows_project ON flows(project_id);

CREATE TABLE IF NOT EXISTS flow_versions (
    flow_id    TEXT NOT NULL,
    version    INTEGER NOT NULL,
    graph      TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (flow_id, version)
);

CREATE TABLE IF NOT EXISTS flow_runs (
    id          TEXT PRIMARY KEY,
    flow_id     TEXT NOT NULL,
    version     INTEGER NOT NULL,
    project_id  TEXT NOT NULL,
    card_id     TEXT NOT NULL DEFAULT '',
    event_id    INTEGER NOT NULL DEFAULT 0,
    event       TEXT NOT NULL DEFAULT '{}',
    depth       INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    finished_at TEXT NOT NULL DEFAULT ''
);
-- one run per flow and event, so handling an event twice cannot run a flow twice
CREATE UNIQUE INDEX IF NOT EXISTS idx_flow_runs_event ON flow_runs(flow_id, event_id) WHERE event_id > 0;
CREATE INDEX IF NOT EXISTS idx_flow_runs_flow ON flow_runs(flow_id, started_at);
CREATE INDEX IF NOT EXISTS idx_flow_runs_status ON flow_runs(status);

CREATE TABLE IF NOT EXISTS flow_steps (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    from_node   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    port        TEXT NOT NULL DEFAULT '',
    output      TEXT NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT '',
    wait_key    TEXT NOT NULL DEFAULT '',
    wake_at     TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL DEFAULT '',
    finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_flow_steps_run ON flow_steps(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_flow_steps_status ON flow_steps(status, seq);

CREATE TABLE IF NOT EXISTS flow_schedules (
    flow_id    TEXT NOT NULL,
    node_id    TEXT NOT NULL,
    last_fired TEXT NOT NULL,
    PRIMARY KEY (flow_id, node_id)
);

CREATE TABLE IF NOT EXISTS flow_seeds (
    project_id TEXT NOT NULL,
    key        TEXT NOT NULL,
    PRIMARY KEY (project_id, key)
);

CREATE TABLE IF NOT EXISTS flow_settings (
    project_id TEXT PRIMARY KEY,
    paused     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
    endpoint   TEXT PRIMARY KEY,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
`)
	if err != nil {
		return fmt.Errorf("create flow tables: %w", err)
	}
	return nil
}

type dbExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// inheritedDepth is the flow depth of a change. A flow's change carries its own and a
// user's action starts a new chain. Anything else (the system, an agent, the end of a
// turn) stays in the chain the last flow or user left the card in, however long ago.
func inheritedDepth(db *sql.DB, cardID string, cause EventCause) int {
	if cause.Source == "flow" || cause.Source == "user" {
		return cause.Depth
	}
	var depth sql.NullInt64
	_ = db.QueryRow(`SELECT depth FROM flow_events WHERE card_id=? AND source IN ('flow','user') ORDER BY id DESC LIMIT 1`, cardID).Scan(&depth)
	if depth.Valid && int(depth.Int64) > cause.Depth {
		return int(depth.Int64)
	}
	return cause.Depth
}

func insertFlowEvent(x dbExec, projectID, typ, cardID, sessionID string, payload map[string]any, cause EventCause, depth int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = x.Exec(`INSERT INTO flow_events (project_id, type, card_id, session_id, payload, source, flow_id, run_id, depth)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		projectID, typ, cardID, sessionID, string(body), cause.Source, cause.FlowID, cause.RunID, depth)
	return err
}

// RecordFlowEvent logs an event that does not come with a card change (a turn ended).
func RecordFlowEvent(db *sql.DB, projectID, typ, cardID, sessionID string, payload map[string]any) error {
	cause := EventCause{Source: "system"}
	return insertFlowEvent(db, projectID, typ, cardID, sessionID, payload, cause, inheritedDepth(db, cardID, cause))
}

// RecordToolRequest logs a tool request. It starts its own chain: it changes nothing on the
// card, and a card deep in a chain of flows must not lose its approvals to the loop limit.
func RecordToolRequest(db *sql.DB, projectID, cardID, sessionID string, payload map[string]any) error {
	return insertFlowEvent(db, projectID, EventToolRequested, cardID, sessionID, payload, EventCause{Source: "agent"}, 0)
}

// OpenToolFlowRuns are the runs still deciding a tool request. Their answer has nowhere to go
// after a hub restart, because the request it answers was lost with the restart.
func OpenToolFlowRuns(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT id FROM flow_runs WHERE status IN (?,?) AND json_extract(event,'$.type')=?`, RunRunning, RunQueued, EventToolRequested)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PruneToolFlowRuns drops old finished runs of tool requests. There is one per tool call, and
// each one keeps the tool's input.
func PruneToolFlowRuns(db *sql.DB, age time.Duration) error {
	cut := time.Now().UTC().Add(-age).Format("2006-01-02T15:04:05.000Z")
	const old = `SELECT id FROM flow_runs WHERE status NOT IN (?,?) AND finished_at<? AND json_extract(event,'$.type')=?`
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM flow_steps WHERE run_id IN (`+old+`)`, RunRunning, RunQueued, cut, EventToolRequested); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM flow_runs WHERE id IN (`+old+`)`, RunRunning, RunQueued, cut, EventToolRequested); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordFlowChain leaves a mark that a flow acted on a card without changing its list, such
// as starting a turn on a card already in progress. What that turn causes then counts as
// part of the flow's chain, so a flow cannot keep restarting an agent unnoticed.
func RecordFlowChain(db *sql.DB, projectID, cardID string, cause EventCause) error {
	return insertFlowEvent(db, projectID, "flow.acted", cardID, "", map[string]any{}, cause, cause.Depth)
}

// FlowEvent is one row of the event log.
type FlowEvent struct {
	ID        int64
	ProjectID string
	Type      string
	CardID    string
	SessionID string
	Payload   map[string]any
	Source    string
	FlowID    string
	RunID     string
	Depth     int
	CreatedAt time.Time
}

// PendingFlowEvents returns the events not handled yet, oldest first.
func PendingFlowEvents(db *sql.DB, limit int) ([]FlowEvent, error) {
	rows, err := db.Query(`SELECT id, project_id, type, card_id, session_id, payload, source, flow_id, run_id, depth, created_at
		FROM flow_events WHERE dispatched=0 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlowEvent
	for rows.Next() {
		var e FlowEvent
		var payload, created string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Type, &e.CardID, &e.SessionID, &payload, &e.Source, &e.FlowID, &e.RunID, &e.Depth, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &e.Payload)
		e.CreatedAt = parseFlowTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func MarkFlowEventDispatched(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE flow_events SET dispatched=1 WHERE id=?`, id)
	return err
}

// PruneFlowEvents drops handled events older than the given age.
func PruneFlowEvents(db *sql.DB, age time.Duration) error {
	cut := time.Now().UTC().Add(-age).Format("2006-01-02T15:04:05.000Z")
	_, err := db.Exec(`DELETE FROM flow_events WHERE dispatched=1 AND created_at<?`, cut)
	return err
}

func parseFlowTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// FlowNow is the timestamp format of the flow tables.
func FlowNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// ---- Flow ----

// Flow is an automation of a project. Draft is what the editor last saved; runs use
// the published version they started with.
type Flow struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"project_id"`
	Name             string          `json:"name"`
	Draft            json.RawMessage `json:"draft"`
	PublishedVersion int             `json:"published_version"`
	Enabled          bool            `json:"enabled"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

const flowCols = `id, project_id, name, draft, published_version, enabled, created_at, updated_at`

func scanFlow(row interface{ Scan(...any) error }) (Flow, error) {
	var f Flow
	var draft string
	var enabled int
	err := row.Scan(&f.ID, &f.ProjectID, &f.Name, &draft, &f.PublishedVersion, &enabled, &f.CreatedAt, &f.UpdatedAt)
	f.Draft = json.RawMessage(draft)
	f.Enabled = enabled != 0
	return f, err
}

func InsertFlow(db *sql.DB, f Flow) (Flow, error) {
	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	if len(f.Draft) == 0 {
		f.Draft = json.RawMessage(`{"nodes":[],"edges":[]}`)
	}
	if _, err := db.Exec(`INSERT INTO flows (id, project_id, name, draft) VALUES (?,?,?,?)`, f.ID, f.ProjectID, f.Name, string(f.Draft)); err != nil {
		return f, err
	}
	return GetFlow(db, f.ID)
}

func GetFlow(db *sql.DB, id string) (Flow, error) {
	return scanFlow(db.QueryRow(`SELECT `+flowCols+` FROM flows WHERE id=?`, id))
}

func queryFlows(db *sql.DB, where string, args ...any) ([]Flow, error) {
	rows, err := db.Query(`SELECT `+flowCols+` FROM flows WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flow{}
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func ListFlows(db *sql.DB, projectID string) ([]Flow, error) {
	return queryFlows(db, `project_id=?`, projectID)
}

// ActiveFlows are the flows that react to events: enabled and published.
func ActiveFlows(db *sql.DB, projectID string) ([]Flow, error) {
	return queryFlows(db, `project_id=? AND enabled=1 AND published_version>0`, projectID)
}

// AllActiveFlows is ActiveFlows across projects (schedules have no event to name one).
func AllActiveFlows(db *sql.DB) ([]Flow, error) {
	return queryFlows(db, `enabled=1 AND published_version>0`)
}

func UpdateFlowDraft(db *sql.DB, id, name string, draft json.RawMessage) error {
	_, err := db.Exec(`UPDATE flows SET name=?, draft=?, updated_at=? WHERE id=?`, name, string(draft), FlowNow(), id)
	return err
}

func SetFlowEnabled(db *sql.DB, id string, enabled bool) error {
	_, err := db.Exec(`UPDATE flows SET enabled=?, updated_at=? WHERE id=?`, boolToInt(enabled), FlowNow(), id)
	return err
}

// PublishFlow freezes a graph as the next version and enables the flow.
func PublishFlow(db *sql.DB, id string, graph json.RawMessage) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM flow_versions WHERE flow_id=?`, id).Scan(&v); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO flow_versions (flow_id, version, graph) VALUES (?,?,?)`, id, v, string(graph)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE flows SET published_version=?, enabled=1, updated_at=? WHERE id=?`, v, FlowNow(), id); err != nil {
		return 0, err
	}
	return v, tx.Commit()
}

func GetFlowVersion(db *sql.DB, id string, version int) (json.RawMessage, error) {
	var g string
	err := db.QueryRow(`SELECT graph FROM flow_versions WHERE flow_id=? AND version=?`, id, version).Scan(&g)
	return json.RawMessage(g), err
}

// DeleteFlow removes a flow with its versions, runs and steps.
func DeleteFlow(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM flow_steps WHERE run_id IN (SELECT id FROM flow_runs WHERE flow_id=?)`,
		`DELETE FROM flow_runs WHERE flow_id=?`,
		`DELETE FROM flow_versions WHERE flow_id=?`,
		`DELETE FROM flow_schedules WHERE flow_id=?`,
		`DELETE FROM flows WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimFlowSeed records that a base flow was given to a project. It returns false when it
// already was, so a flow the user deleted stays deleted.
func ClaimFlowSeed(db *sql.DB, projectID, key string) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO flow_seeds (project_id, key) VALUES (?,?)`, projectID, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func FlowsPaused(db *sql.DB, projectID string) bool {
	var paused int
	_ = db.QueryRow(`SELECT paused FROM flow_settings WHERE project_id=?`, projectID).Scan(&paused)
	return paused != 0
}

func SetFlowsPaused(db *sql.DB, projectID string, paused bool) error {
	_, err := db.Exec(`INSERT INTO flow_settings (project_id, paused) VALUES (?,?)
		ON CONFLICT(project_id) DO UPDATE SET paused=excluded.paused`, projectID, boolToInt(paused))
	return err
}

// ---- Schedules ----

// FlowScheduleLastFired returns when a schedule node last fired ("" = never).
func FlowScheduleLastFired(db *sql.DB, flowID, nodeID string) string {
	var s string
	_ = db.QueryRow(`SELECT last_fired FROM flow_schedules WHERE flow_id=? AND node_id=?`, flowID, nodeID).Scan(&s)
	return s
}

func SetFlowScheduleLastFired(db *sql.DB, flowID, nodeID, at string) error {
	_, err := db.Exec(`INSERT INTO flow_schedules (flow_id, node_id, last_fired) VALUES (?,?,?)
		ON CONFLICT(flow_id, node_id) DO UPDATE SET last_fired=excluded.last_fired`, flowID, nodeID, at)
	return err
}

// ---- Runs ----

type FlowRun struct {
	ID         string          `json:"id"`
	FlowID     string          `json:"flow_id"`
	Version    int             `json:"version"`
	ProjectID  string          `json:"project_id"`
	CardID     string          `json:"card_id"`
	EventID    int64           `json:"event_id"`
	Event      json.RawMessage `json:"event"`
	Depth      int             `json:"depth"`
	Status     string          `json:"status"`
	Error      string          `json:"error"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at"`
}

const flowRunCols = `id, flow_id, version, project_id, card_id, event_id, event, depth, status, error, started_at, finished_at`

func scanFlowRun(row interface{ Scan(...any) error }) (FlowRun, error) {
	var r FlowRun
	var ev string
	err := row.Scan(&r.ID, &r.FlowID, &r.Version, &r.ProjectID, &r.CardID, &r.EventID, &ev, &r.Depth, &r.Status, &r.Error, &r.StartedAt, &r.FinishedAt)
	r.Event = json.RawMessage(ev)
	return r, err
}

// ErrFlowRunExists means the event already started this flow.
var ErrFlowRunExists = errors.New("this event already started this flow")

// CreateFlowRun creates a run with its first steps in one transaction.
func CreateFlowRun(db *sql.DB, r FlowRun, startNodes []string) (FlowRun, error) {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if len(r.Event) == 0 {
		r.Event = json.RawMessage(`{}`)
	}
	tx, err := db.Begin()
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO flow_runs (id, flow_id, version, project_id, card_id, event_id, event, depth, status, error, started_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.FlowID, r.Version, r.ProjectID, r.CardID, r.EventID, string(r.Event), r.Depth, r.Status, r.Error, FlowNow(), r.FinishedAt)
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, ErrFlowRunExists
	}
	for _, node := range startNodes {
		if _, err := tx.Exec(`INSERT INTO flow_steps (run_id, node_id, status) VALUES (?,?,?)`, r.ID, node, StepPending); err != nil {
			return r, err
		}
	}
	if err := tx.Commit(); err != nil {
		return r, err
	}
	return GetFlowRun(db, r.ID)
}

func GetFlowRun(db *sql.DB, id string) (FlowRun, error) {
	return scanFlowRun(db.QueryRow(`SELECT `+flowRunCols+` FROM flow_runs WHERE id=?`, id))
}

// ListFlowRuns returns the newest runs of a flow.
func ListFlowRuns(db *sql.DB, flowID string, limit int) ([]FlowRun, error) {
	rows, err := db.Query(`SELECT `+flowRunCols+` FROM flow_runs WHERE flow_id=? ORDER BY started_at DESC, id LIMIT ?`, flowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowRun{}
	for rows.Next() {
		r, err := scanFlowRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasActiveFlowRun reports whether the flow is running on the card.
func HasActiveFlowRun(db *sql.DB, flowID, cardID string) bool {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM flow_runs WHERE flow_id=? AND card_id=? AND status=?`, flowID, cardID, RunRunning).Scan(&n)
	return n > 0
}

// NextQueuedFlowRun returns the oldest run waiting for the flow to be free on the card.
func NextQueuedFlowRun(db *sql.DB, flowID, cardID string) (FlowRun, error) {
	return scanFlowRun(db.QueryRow(`SELECT `+flowRunCols+` FROM flow_runs WHERE flow_id=? AND card_id=? AND status=? ORDER BY started_at, id LIMIT 1`, flowID, cardID, RunQueued))
}

func SetFlowRunRunning(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE flow_runs SET status=? WHERE id=? AND status=?`, RunRunning, id, RunQueued)
	return err
}

// FinishFlowRun closes a run and cancels whatever it still had open. It returns false
// when the run was already closed.
func FinishFlowRun(db *sql.DB, id, status, errMsg string) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := FlowNow()
	res, err := tx.Exec(`UPDATE flow_runs SET status=?, error=?, finished_at=? WHERE id=? AND status IN (?,?)`,
		status, errMsg, now, id, RunRunning, RunQueued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE flow_steps SET status=?, finished_at=? WHERE run_id=? AND status IN (?,?,?)`,
		StepCancelled, now, id, StepPending, StepRunning, StepWaiting); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ---- Steps ----

type FlowStep struct {
	Seq        int64           `json:"seq"`
	RunID      string          `json:"run_id"`
	NodeID     string          `json:"node_id"`
	FromNode   string          `json:"from_node"`
	Status     string          `json:"status"`
	Port       string          `json:"port"`
	Output     json.RawMessage `json:"output"`
	Error      string          `json:"error"`
	WaitKey    string          `json:"wait_key"`
	WakeAt     string          `json:"wake_at"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at"`
}

const flowStepCols = `seq, run_id, node_id, from_node, status, port, output, error, wait_key, wake_at, started_at, finished_at`

func scanFlowStep(row interface{ Scan(...any) error }) (FlowStep, error) {
	var s FlowStep
	var out string
	err := row.Scan(&s.Seq, &s.RunID, &s.NodeID, &s.FromNode, &s.Status, &s.Port, &out, &s.Error, &s.WaitKey, &s.WakeAt, &s.StartedAt, &s.FinishedAt)
	s.Output = json.RawMessage(out)
	return s, err
}

func queryFlowSteps(db *sql.DB, where string, args ...any) ([]FlowStep, error) {
	rows, err := db.Query(`SELECT `+flowStepCols+` FROM flow_steps WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FlowStep{}
	for rows.Next() {
		s, err := scanFlowStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ListFlowSteps(db *sql.DB, runID string) ([]FlowStep, error) {
	return queryFlowSteps(db, `run_id=? ORDER BY seq`, runID)
}

// ReadyFlowSteps are the pending steps of running runs, oldest first.
func ReadyFlowSteps(db *sql.DB, limit int) ([]FlowStep, error) {
	return queryFlowSteps(db, `status=? AND run_id IN (SELECT id FROM flow_runs WHERE status=?) ORDER BY seq LIMIT ?`, StepPending, RunRunning, limit)
}

// WaitingFlowSteps are the steps parked on a key.
func WaitingFlowSteps(db *sql.DB, key string) ([]FlowStep, error) {
	return queryFlowSteps(db, `status=? AND wait_key=? ORDER BY seq`, StepWaiting, key)
}

// WaitingFlowStepsWithPrefix are the steps parked on any key that starts this way.
func WaitingFlowStepsWithPrefix(db *sql.DB, prefix string) ([]FlowStep, error) {
	return queryFlowSteps(db, `status=? AND wait_key LIKE ? ORDER BY seq`, StepWaiting, prefix+"%")
}

// GetFlowStep returns one step.
func GetFlowStep(db *sql.DB, seq int64) (FlowStep, error) {
	return scanFlowStep(db.QueryRow(`SELECT `+flowStepCols+` FROM flow_steps WHERE seq=?`, seq))
}

// DueFlowSteps are the waiting steps whose timer has expired.
func DueFlowSteps(db *sql.DB, now string) ([]FlowStep, error) {
	return queryFlowSteps(db, `status=? AND wake_at<>'' AND wake_at<=? ORDER BY seq`, StepWaiting, now)
}

// InterruptedFlowSteps are the steps left running by a hub that stopped.
func InterruptedFlowSteps(db *sql.DB) ([]FlowStep, error) {
	return queryFlowSteps(db, `status=? ORDER BY seq`, StepRunning)
}

func StartFlowStep(db *sql.DB, seq int64) error {
	_, err := db.Exec(`UPDATE flow_steps SET status=?, started_at=? WHERE seq=?`, StepRunning, FlowNow(), seq)
	return err
}

// RetryFlowStep puts a step interrupted by a stop back in the queue.
func RetryFlowStep(db *sql.DB, seq int64) error {
	_, err := db.Exec(`UPDATE flow_steps SET status=? WHERE seq=?`, StepPending, seq)
	return err
}

// WaitFlowStep parks a step on a key and/or a timer.
func WaitFlowStep(db *sql.DB, seq int64, key, wakeAt string, output json.RawMessage) error {
	_, err := db.Exec(`UPDATE flow_steps SET status=?, wait_key=?, wake_at=?, output=? WHERE seq=?`, StepWaiting, key, wakeAt, string(output), seq)
	return err
}

// FinishFlowStep closes a step and queues the nodes that follow it, in one transaction,
// so a stop can neither repeat the step nor lose what comes after it.
func FinishFlowStep(db *sql.DB, s FlowStep, status, port string, output json.RawMessage, errMsg string, next []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(output) == 0 {
		output = json.RawMessage(`{}`)
	}
	if _, err := tx.Exec(`UPDATE flow_steps SET status=?, port=?, output=?, error=?, wait_key='', wake_at='', finished_at=? WHERE seq=?`,
		status, port, string(output), errMsg, FlowNow(), s.Seq); err != nil {
		return err
	}
	for _, node := range next {
		if _, err := tx.Exec(`INSERT INTO flow_steps (run_id, node_id, from_node, status) VALUES (?,?,?,?)`, s.RunID, node, s.NodeID, StepPending); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FlowRunHasOpenSteps reports whether a run still has work pending.
func FlowRunHasOpenSteps(db *sql.DB, runID string) bool {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM flow_steps WHERE run_id=? AND status IN (?,?,?)`, runID, StepPending, StepRunning, StepWaiting).Scan(&n)
	return n > 0
}

// CountFlowSteps counts all the steps of a run, optionally only those of one node.
func CountFlowSteps(db *sql.DB, runID, nodeID string) int {
	var n int
	if nodeID == "" {
		_ = db.QueryRow(`SELECT COUNT(*) FROM flow_steps WHERE run_id=?`, runID).Scan(&n)
	} else {
		_ = db.QueryRow(`SELECT COUNT(*) FROM flow_steps WHERE run_id=? AND node_id=?`, runID, nodeID).Scan(&n)
	}
	return n
}

// ActiveFlowRun is a run in progress as the Running tab shows it.
type ActiveFlowRun struct {
	Run      FlowRun  `json:"run"`
	FlowName string   `json:"flow_name"`
	Open     []string `json:"open"` // the node ids it is on right now
}

// ListActiveFlowRuns returns the project's runs in progress, newest first.
func ListActiveFlowRuns(db *sql.DB, projectID string) ([]ActiveFlowRun, error) {
	rows, err := db.Query(`SELECT `+flowRunCols+` FROM flow_runs WHERE project_id=? AND status=? ORDER BY started_at DESC, id LIMIT 100`, projectID, RunRunning)
	if err != nil {
		return nil, err
	}
	var runs []FlowRun
	for rows.Next() {
		r, err := scanFlowRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []ActiveFlowRun{}
	for _, r := range runs {
		a := ActiveFlowRun{Run: r, Open: []string{}}
		if f, err := GetFlow(db, r.FlowID); err == nil {
			a.FlowName = f.Name
		}
		steps, err := queryFlowSteps(db, `run_id=? AND status IN (?,?,?) ORDER BY seq`, r.ID, StepPending, StepRunning, StepWaiting)
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			a.Open = append(a.Open, s.NodeID)
		}
		out = append(out, a)
	}
	return out, nil
}
