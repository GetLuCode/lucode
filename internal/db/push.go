package db

import "database/sql"

// PushSubscription is a browser that asked for push notifications. The endpoint is a secret
// URL of its push service, and the keys encrypt what is sent to it.
type PushSubscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// SavePushSubscription stores a subscription, or refreshes the keys of a known endpoint.
func SavePushSubscription(db *sql.DB, s PushSubscription) error {
	_, err := db.Exec(`INSERT INTO push_subscriptions (endpoint, p256dh, auth) VALUES (?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth`, s.Endpoint, s.P256dh, s.Auth)
	return err
}

// DeletePushSubscription forgets a subscription. Forgetting an unknown one is not an error.
func DeletePushSubscription(db *sql.DB, endpoint string) error {
	_, err := db.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint)
	return err
}

// GetPushSubscription finds one subscription.
func GetPushSubscription(db *sql.DB, endpoint string) (PushSubscription, error) {
	var s PushSubscription
	err := db.QueryRow(`SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE endpoint=?`, endpoint).
		Scan(&s.Endpoint, &s.P256dh, &s.Auth)
	return s, err
}

// ListPushSubscriptions returns every subscription of this hub.
func ListPushSubscriptions(db *sql.DB) ([]PushSubscription, error) {
	rows, err := db.Query(`SELECT endpoint, p256dh, auth FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		var s PushSubscription
		if err := rows.Scan(&s.Endpoint, &s.P256dh, &s.Auth); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
