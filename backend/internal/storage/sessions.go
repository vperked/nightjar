package storage

import "database/sql"

// Session is a signed-in browser, known by a hash of its cookie. User is the viewer it belongs to,
// or "" for the admin.
type Session struct {
	Hash    string
	Created float64
	Seen    float64
	Agent   string
	User    string
}

// Sessions returns every stored session.
func (s *Store) Sessions() []Session {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query("SELECT hash, created, seen, agent, user FROM sessions")
	if err != nil {
		log.Warn("Session query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		if rows.Scan(&x.Hash, &x.Created, &x.Seen, &x.Agent, &x.User) == nil {
			out = append(out, x)
		}
	}
	return out
}

// SaveSession inserts a session or updates when it was last seen.
func (s *Store) SaveSession(x Session) error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (hash, created, seen, agent, user) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(hash) DO UPDATE SET seen = excluded.seen`, x.Hash, x.Created, x.Seen, x.Agent, x.User)
		return err
	})
}

// DeleteSessions removes the given sessions, or every one when none are given.
func (s *Store) DeleteSessions(hashes ...string) error {
	return s.tx(func(tx *sql.Tx) error {
		if len(hashes) == 0 {
			_, err := tx.Exec("DELETE FROM sessions")
			return err
		}
		for _, h := range hashes {
			if _, err := tx.Exec("DELETE FROM sessions WHERE hash = ?", h); err != nil {
				return err
			}
		}
		return nil
	})
}
