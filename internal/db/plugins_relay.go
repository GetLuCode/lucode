package db

import (
	"database/sql"
	"errors"
)

// The addresses plugins asked the webhook relay for. The relay only knows an id; this is where the hub
// remembers whose it is and which project its events belong to. Every call names the plugin id, which
// comes from the host and never from the plugin, so one plugin cannot reach another's.

// ErrRelayEndpointNotFound means no plugin has an endpoint with that id.
var ErrRelayEndpointNotFound = errors.New("no such webhook endpoint")

// RelayEndpointRow is one endpoint of a plugin.
type RelayEndpointRow struct {
	EndpointID string `json:"id"`
	PluginID   string `json:"pluginId"`
	Provider   string `json:"provider"`
	ProjectID  string `json:"projectId"`
	Label      string `json:"label"`
	URL        string `json:"url"`
	CreatedAt  string `json:"created"`
}

// InsertRelayEndpoint records an endpoint for a plugin.
func InsertRelayEndpoint(db *sql.DB, r RelayEndpointRow) error {
	_, err := db.Exec(`INSERT INTO plugin_relay_endpoints (endpoint_id, plugin_id, provider, project_id, label, url) VALUES (?,?,?,?,?,?)`,
		r.EndpointID, r.PluginID, r.Provider, r.ProjectID, r.Label, r.URL)
	return err
}

// GetRelayEndpoint finds the plugin an endpoint belongs to.
func GetRelayEndpoint(db *sql.DB, endpointID string) (RelayEndpointRow, error) {
	var r RelayEndpointRow
	err := db.QueryRow(`SELECT endpoint_id, plugin_id, provider, project_id, label, url, created_at FROM plugin_relay_endpoints WHERE endpoint_id=?`, endpointID).
		Scan(&r.EndpointID, &r.PluginID, &r.Provider, &r.ProjectID, &r.Label, &r.URL, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRelayEndpointNotFound
	}
	return r, err
}

// ListRelayEndpoints lists a plugin's endpoints, oldest first.
func ListRelayEndpoints(db *sql.DB, pluginID string) ([]RelayEndpointRow, error) {
	rows, err := db.Query(`SELECT endpoint_id, plugin_id, provider, project_id, label, url, created_at FROM plugin_relay_endpoints WHERE plugin_id=? ORDER BY created_at, endpoint_id`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelayEndpointRow{}
	for rows.Next() {
		var r RelayEndpointRow
		if err := rows.Scan(&r.EndpointID, &r.PluginID, &r.Provider, &r.ProjectID, &r.Label, &r.URL, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRelayEndpoint forgets an endpoint, only if it is the plugin's. It says whether there was one.
func DeleteRelayEndpoint(db *sql.DB, pluginID, endpointID string) (bool, error) {
	res, err := db.Exec(`DELETE FROM plugin_relay_endpoints WHERE plugin_id=? AND endpoint_id=?`, pluginID, endpointID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
