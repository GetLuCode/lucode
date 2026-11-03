package db

import "database/sql"

// AIProfile is a preconfigured AI of a runner: a claude_config_dir together with
// the account information Anthropic reports for its credentials.
type AIProfile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ClaudeConfigDir string `json:"claude_config_dir"`
	Email           string `json:"email"`
	DisplayName     string `json:"display_name"`
	Organization    string `json:"organization"`
	Plan            string `json:"plan"`
	Error           string `json:"error"`      // why the last profile lookup failed ("" = ok)
	CheckedAt       string `json:"checked_at"` // RFC3339 of the last lookup
}

const aiProfileCols = `id, name, claude_config_dir, email, display_name, organization, plan, error, checked_at`

func scanAIProfile(sc interface{ Scan(...any) error }) (AIProfile, error) {
	var p AIProfile
	err := sc.Scan(&p.ID, &p.Name, &p.ClaudeConfigDir, &p.Email, &p.DisplayName, &p.Organization, &p.Plan, &p.Error, &p.CheckedAt)
	return p, err
}

func ListAIProfiles(db *sql.DB) ([]AIProfile, error) {
	rows, err := db.Query(`SELECT ` + aiProfileCols + ` FROM ai_profiles ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIProfile{}
	for rows.Next() {
		p, err := scanAIProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetAIProfile(db *sql.DB, id string) (AIProfile, error) {
	return scanAIProfile(db.QueryRow(`SELECT `+aiProfileCols+` FROM ai_profiles WHERE id=?`, id))
}

func InsertAIProfile(db *sql.DB, p AIProfile) error {
	_, err := db.Exec(`INSERT INTO ai_profiles (`+aiProfileCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.ClaudeConfigDir, p.Email, p.DisplayName, p.Organization, p.Plan, p.Error, p.CheckedAt)
	return err
}

// UpdateAIProfileAccount stores the result of a profile lookup.
func UpdateAIProfileAccount(db *sql.DB, p AIProfile) error {
	_, err := db.Exec(`UPDATE ai_profiles SET email=?, display_name=?, organization=?, plan=?, error=?, checked_at=? WHERE id=?`,
		p.Email, p.DisplayName, p.Organization, p.Plan, p.Error, p.CheckedAt, p.ID)
	return err
}

func DeleteAIProfile(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM ai_profiles WHERE id=?`, id)
	return err
}
