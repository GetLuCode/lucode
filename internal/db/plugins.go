package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Plugins: what is installed on the hub and what each plugin keeps. Every read and write names a
// plugin id, which comes from the host and never from the plugin, so one cannot reach another's.

// SessionKindPlugin is a chat that lives on a plugin's page, not in the session list.
const SessionKindPlugin = "plugin"

// EventPluginEvent is what lucode.flows.emit logs.
const EventPluginEvent = "plugin.event"

// PluginStorageQuota is how much lucode.storage keeps per plugin.
const PluginStorageQuota = 10 << 20

// ErrPluginQuota means a plugin's storage is full.
var ErrPluginQuota = errors.New("this plugin's storage is full (10 MB)")

// ErrFlowLocked means the flow belongs to a plugin: fork it to change it.
var ErrFlowLocked = errors.New("this flow belongs to a plugin and cannot be edited: create a new version (a copy) to change it")

func migratePlugins(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS plugins (
    id           TEXT PRIMARY KEY,
    version      TEXT NOT NULL,
    manifest     TEXT NOT NULL,
    dir          TEXT NOT NULL,
    zip          TEXT NOT NULL DEFAULT '',
    sha256       TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    granted      TEXT NOT NULL DEFAULT '[]',
    error        TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT 'upload',
    installed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS plugin_storage (
    plugin_id TEXT NOT NULL,
    key       TEXT NOT NULL,
    value     TEXT NOT NULL,
    PRIMARY KEY (plugin_id, key)
);

CREATE TABLE IF NOT EXISTS plugin_settings (
    plugin_id TEXT NOT NULL,
    key       TEXT NOT NULL,
    value     TEXT NOT NULL,
    PRIMARY KEY (plugin_id, key)
);

CREATE TABLE IF NOT EXISTS plugin_conversations (
    id         TEXT PRIMARY KEY,
    plugin_id  TEXT NOT NULL,
    session_id TEXT NOT NULL,
    project_id TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    turns      INTEGER NOT NULL DEFAULT 0,
    meta       TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (plugin_id, session_id)
);
CREATE INDEX IF NOT EXISTS idx_plugin_conv_project ON plugin_conversations(plugin_id, project_id, updated_at);

-- the secret a hub shares with each runner it was paired with
CREATE TABLE IF NOT EXISTS plugin_runner_pairings (
    runner_id  TEXT PRIMARY KEY,
    secret     BLOB NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- a bundled plugin that was installed once: removed, it stays removed
CREATE TABLE IF NOT EXISTS plugin_seeds (
    plugin_id  TEXT PRIMARY KEY,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
`)
	if err != nil {
		return fmt.Errorf("create plugin tables: %w", err)
	}
	// a plugin's blueprint in a project is a flow of its own, only one each
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_flows_plugin_blueprint ON flows(project_id, plugin_id, plugin_blueprint) WHERE locked=1`)
	return nil
}

// migrateFlowPluginColumns runs before anything reads the flows table.
func migrateFlowPluginColumns(db *sql.DB) {
	// flows.plugin_id / plugin_blueprint — the plugin and blueprint a locked flow comes from
	db.Exec(`ALTER TABLE flows ADD COLUMN plugin_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE flows ADD COLUMN plugin_blueprint TEXT NOT NULL DEFAULT ''`)
	// flows.locked — visible, runnable, can be switched on and off, not editable
	db.Exec(`ALTER TABLE flows ADD COLUMN locked INTEGER NOT NULL DEFAULT 0`)
	// flows.forked_from — the locked flow an editable copy started from
	db.Exec(`ALTER TABLE flows ADD COLUMN forked_from TEXT NOT NULL DEFAULT ''`)
}

// PluginRow is an installed plugin.
type PluginRow struct {
	ID          string
	Version     string
	Manifest    json.RawMessage
	Dir         string
	Zip         string
	SHA256      string
	Enabled     bool
	Granted     []string
	Error       string
	Source      string
	InstalledAt string
	UpdatedAt   string
}

const pluginCols = `id, version, manifest, dir, zip, sha256, enabled, granted, error, source, installed_at, updated_at`

func scanPlugin(row interface{ Scan(...any) error }) (PluginRow, error) {
	var p PluginRow
	var manifest, granted string
	var enabled int
	err := row.Scan(&p.ID, &p.Version, &manifest, &p.Dir, &p.Zip, &p.SHA256, &enabled, &granted, &p.Error, &p.Source, &p.InstalledAt, &p.UpdatedAt)
	p.Manifest = json.RawMessage(manifest)
	p.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(granted), &p.Granted)
	return p, err
}

// SavePlugin installs or upgrades a plugin.
func SavePlugin(db *sql.DB, p PluginRow) error {
	granted, _ := json.Marshal(p.Granted)
	_, err := db.Exec(`INSERT INTO plugins (id, version, manifest, dir, zip, sha256, enabled, granted, error, source) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET version=excluded.version, manifest=excluded.manifest, dir=excluded.dir, zip=excluded.zip,
			sha256=excluded.sha256, enabled=excluded.enabled, granted=excluded.granted, error=excluded.error, source=excluded.source,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		p.ID, p.Version, string(p.Manifest), p.Dir, p.Zip, p.SHA256, boolToInt(p.Enabled), string(granted), p.Error, p.Source)
	return err
}

// GetPlugin finds one.
func GetPlugin(db *sql.DB, id string) (PluginRow, error) {
	return scanPlugin(db.QueryRow(`SELECT `+pluginCols+` FROM plugins WHERE id=?`, id))
}

// ListPlugins lists what is installed.
func ListPlugins(db *sql.DB) ([]PluginRow, error) {
	rows, err := db.Query(`SELECT ` + pluginCols + ` FROM plugins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginRow{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPluginEnabled switches a plugin on or off.
func SetPluginEnabled(db *sql.DB, id string, enabled bool) error {
	_, err := db.Exec(`UPDATE plugins SET enabled=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, boolToInt(enabled), id)
	return err
}

// SetPluginError records what is wrong with an installed plugin ("" clears it).
func SetPluginError(db *sql.DB, id, msg string) error {
	_, err := db.Exec(`UPDATE plugins SET error=? WHERE id=?`, msg, id)
	return err
}

// DeletePlugin removes a plugin. With keepData its storage, settings and conversations stay
// for a later install. Either way its seed stays, so a bundled plugin does not come back.
func DeletePlugin(db *sql.DB, id string, keepData bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM plugins WHERE id=?`, id); err != nil {
		return err
	}
	if !keepData {
		for _, q := range []string{
			`DELETE FROM plugin_storage WHERE plugin_id=?`,
			`DELETE FROM plugin_settings WHERE plugin_id=?`,
			`DELETE FROM plugin_conversations WHERE plugin_id=?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ClaimPluginSeed returns true the first time a bundled plugin is offered to this hub.
func ClaimPluginSeed(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO plugin_seeds (plugin_id) VALUES (?)`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReleasePluginSeed lets a bundled plugin whose install failed be tried again.
func ReleasePluginSeed(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM plugin_seeds WHERE plugin_id=?`, id)
	return err
}

// SavePluginRunnerSecret stores the secret of a runner, replacing any earlier one.
func SavePluginRunnerSecret(db *sql.DB, runnerID string, secret []byte) error {
	_, err := db.Exec(`INSERT INTO plugin_runner_pairings (runner_id, secret) VALUES (?,?)
		ON CONFLICT(runner_id) DO UPDATE SET secret=excluded.secret, created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, runnerID, secret)
	return err
}

// PluginRunnerSecret reads a runner's secret; nil if it was never paired.
func PluginRunnerSecret(db *sql.DB, runnerID string) []byte {
	var secret []byte
	if err := db.QueryRow(`SELECT secret FROM plugin_runner_pairings WHERE runner_id=?`, runnerID).Scan(&secret); err != nil {
		return nil
	}
	return secret
}

// ---- storage ----

// PluginStorageGet reads a key; found says whether it exists.
func PluginStorageGet(db *sql.DB, pluginID, key string) (value string, found bool, err error) {
	err = db.QueryRow(`SELECT value FROM plugin_storage WHERE plugin_id=? AND key=?`, pluginID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// PluginStorageSet writes a key, inside the quota. One statement, because a transaction that reads
// and then writes fails at once in WAL mode if another writer got in between.
func PluginStorageSet(db *sql.DB, pluginID, key, value string) error {
	res, err := db.Exec(`INSERT INTO plugin_storage (plugin_id, key, value)
		SELECT ?, ?, ? WHERE (SELECT COALESCE(SUM(LENGTH(CAST(key AS BLOB))+LENGTH(CAST(value AS BLOB))),0) FROM plugin_storage WHERE plugin_id=? AND key<>?) + ? <= ?
		ON CONFLICT(plugin_id, key) DO UPDATE SET value=excluded.value`,
		pluginID, key, value, pluginID, key, len(key)+len(value), PluginStorageQuota)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPluginQuota
	}
	return nil
}

// PluginStorageDelete removes a key.
func PluginStorageDelete(db *sql.DB, pluginID, key string) error {
	_, err := db.Exec(`DELETE FROM plugin_storage WHERE plugin_id=? AND key=?`, pluginID, key)
	return err
}

// PluginStorageList lists keys under a prefix.
func PluginStorageList(db *sql.DB, pluginID, prefix string) ([]string, error) {
	rows, err := db.Query(`SELECT key FROM plugin_storage WHERE plugin_id=? AND substr(key,1,?)=? ORDER BY key LIMIT 10000`, pluginID, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ---- settings ----

// PluginSettings reads a plugin's stored values, as JSON.
func PluginSettings(db *sql.DB, pluginID string) (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM plugin_settings WHERE plugin_id=?`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetPluginSetting stores a value.
func SetPluginSetting(db *sql.DB, pluginID, key, value string) error {
	_, err := db.Exec(`INSERT INTO plugin_settings (plugin_id, key, value) VALUES (?,?,?)
		ON CONFLICT(plugin_id, key) DO UPDATE SET value=excluded.value`, pluginID, key, value)
	return err
}

// DeletePluginSetting forgets a value.
func DeletePluginSetting(db *sql.DB, pluginID, key string) error {
	_, err := db.Exec(`DELETE FROM plugin_settings WHERE plugin_id=? AND key=?`, pluginID, key)
	return err
}

// ---- conversations ----

// PluginConversation is a conversation a plugin keeps.
type PluginConversation struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	ProjectID string          `json:"project_id"`
	PluginID  string          `json:"plugin_id"`
	Source    string          `json:"source"`
	URL       string          `json:"url"`
	Title     string          `json:"title"`
	Turns     int             `json:"turns"`
	Meta      json.RawMessage `json:"meta"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

const pluginConvCols = `id, session_id, project_id, plugin_id, source, url, title, turns, meta, created_at, updated_at`

func scanPluginConv(row interface{ Scan(...any) error }) (PluginConversation, error) {
	var c PluginConversation
	var meta string
	err := row.Scan(&c.ID, &c.SessionID, &c.ProjectID, &c.PluginID, &c.Source, &c.URL, &c.Title, &c.Turns, &meta, &c.CreatedAt, &c.UpdatedAt)
	c.Meta = json.RawMessage(meta)
	return c, err
}

// SavePluginConversation creates or updates the conversation of a session. A title once set stays.
func SavePluginConversation(db *sql.DB, c PluginConversation) (PluginConversation, error) {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if len(c.Meta) == 0 {
		c.Meta = json.RawMessage(`{}`)
	}
	_, err := db.Exec(`INSERT INTO plugin_conversations (id, plugin_id, session_id, project_id, source, url, title, turns, meta) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(plugin_id, session_id) DO UPDATE SET
			project_id=excluded.project_id, url=excluded.url, turns=excluded.turns, meta=excluded.meta,
			source=CASE WHEN excluded.source='' THEN plugin_conversations.source ELSE excluded.source END,
			title=CASE WHEN plugin_conversations.title='' THEN excluded.title ELSE plugin_conversations.title END,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		c.ID, c.PluginID, c.SessionID, c.ProjectID, c.Source, c.URL, c.Title, c.Turns, string(c.Meta))
	if err != nil {
		return c, err
	}
	return GetPluginConversationBySession(db, c.PluginID, c.SessionID)
}

// GetPluginConversationBySession finds a session's conversation of this plugin.
func GetPluginConversationBySession(db *sql.DB, pluginID, sessionID string) (PluginConversation, error) {
	return scanPluginConv(db.QueryRow(`SELECT `+pluginConvCols+` FROM plugin_conversations WHERE plugin_id=? AND session_id=?`, pluginID, sessionID))
}

// GetPluginConversation finds a conversation of this plugin by its id.
func GetPluginConversation(db *sql.DB, pluginID, id string) (PluginConversation, error) {
	return scanPluginConv(db.QueryRow(`SELECT `+pluginConvCols+` FROM plugin_conversations WHERE plugin_id=? AND id=?`, pluginID, id))
}

// ListPluginConversations lists a plugin's conversations of a project, newest first.
func ListPluginConversations(db *sql.DB, pluginID, projectID, source string) ([]PluginConversation, error) {
	rows, err := db.Query(`SELECT `+pluginConvCols+` FROM plugin_conversations WHERE plugin_id=? AND project_id=? AND (?='' OR source=?) ORDER BY updated_at DESC LIMIT 500`,
		pluginID, projectID, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginConversation{}
	for rows.Next() {
		c, err := scanPluginConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BindPluginConversation moves a conversation of this plugin to another session.
func BindPluginConversation(db *sql.DB, pluginID, id, sessionID string) error {
	_, err := db.Exec(`UPDATE plugin_conversations SET session_id=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE plugin_id=? AND id=?`, sessionID, pluginID, id)
	return err
}

// ---- locked flows ----

// UpsertLockedFlow makes a plugin's blueprint a flow of the project. A new one is published and
// off, because installing a plugin never starts anything. A later graph is a new version, and an
// enabled flow stays enabled.
func UpsertLockedFlow(db *sql.DB, projectID, pluginID, key, name string, graph json.RawMessage) (Flow, error) {
	tx, err := db.Begin()
	if err != nil {
		return Flow{}, err
	}
	defer tx.Rollback()
	// take the write lock first: a read followed by a write fails if another writer got in between
	if _, err := tx.Exec(`UPDATE flows SET updated_at=updated_at WHERE id=''`); err != nil {
		return Flow{}, err
	}
	var id string
	err = tx.QueryRow(`SELECT id FROM flows WHERE project_id=? AND plugin_id=? AND plugin_blueprint=? AND locked=1`, projectID, pluginID, key).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = uuid.New().String()
		if _, err := tx.Exec(`INSERT INTO flows (id, project_id, name, draft, published_version, enabled, plugin_id, plugin_blueprint, locked)
			VALUES (?,?,?,?,1,0,?,?,1)`, id, projectID, name, string(graph), pluginID, key); err != nil {
			return Flow{}, err
		}
		if _, err := tx.Exec(`INSERT INTO flow_versions (flow_id, version, graph) VALUES (?,1,?)`, id, string(graph)); err != nil {
			return Flow{}, err
		}
	case err != nil:
		return Flow{}, err
	default:
		var last string
		var v int
		if err := tx.QueryRow(`SELECT version, graph FROM flow_versions WHERE flow_id=? ORDER BY version DESC LIMIT 1`, id).Scan(&v, &last); err != nil {
			return Flow{}, err
		}
		if last != string(graph) {
			v++
			if _, err := tx.Exec(`INSERT INTO flow_versions (flow_id, version, graph) VALUES (?,?,?)`, id, v, string(graph)); err != nil {
				return Flow{}, err
			}
		}
		if _, err := tx.Exec(`UPDATE flows SET name=?, draft=?, published_version=?, updated_at=? WHERE id=?`, name, string(graph), v, FlowNow(), id); err != nil {
			return Flow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Flow{}, err
	}
	return GetFlow(db, id)
}

// LockedFlowsOf lists a plugin's flows in every project.
func LockedFlowsOf(db *sql.DB, pluginID string) ([]Flow, error) {
	return queryFlows(db, `plugin_id=? AND locked=1`, pluginID)
}

// ForkFlow copies a locked flow as an ordinary draft. It starts unpublished and off.
func ForkFlow(db *sql.DB, id string) (Flow, error) {
	src, err := GetFlow(db, id)
	if err != nil {
		return Flow{}, err
	}
	if !src.Locked {
		return Flow{}, errors.New("only a flow that belongs to a plugin is copied this way")
	}
	nid := uuid.New().String()
	if _, err := db.Exec(`INSERT INTO flows (id, project_id, name, draft, forked_from) VALUES (?,?,?,?,?)`,
		nid, src.ProjectID, src.Name+" (copy)", string(src.Draft), src.ID); err != nil {
		return Flow{}, err
	}
	return GetFlow(db, nid)
}

// RecordPluginEvent logs lucode.flows.emit. Its depth continues the chain that led to the plugin
// running, which is what stops plugins and flows from triggering each other forever.
func RecordPluginEvent(db *sql.DB, projectID, cardID, sessionID string, payload map[string]any, cause EventCause) error {
	return insertFlowEvent(db, projectID, EventPluginEvent, cardID, sessionID, payload, cause, inheritedDepth(db, cardID, cause))
}
