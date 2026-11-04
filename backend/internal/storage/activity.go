package storage

import "database/sql"

// Activity is one thing done: by someone on the dashboard, through a Discord button, or by
// Nightjar itself.
type Activity struct {
	ID     int64   `json:"id"`
	TS     float64 `json:"ts"`
	Who    string  `json:"who"`
	Kind   string  `json:"kind"` // check, settings, account, discord, repair, cleanup…
	Action string  `json:"action"`
	Target string  `json:"target,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

// AddActivity keeps one entry.
func (s *Store) AddActivity(a Activity) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO activity (ts, who, kind, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)", a.TS, a.Who, a.Kind, a.Action, a.Target, a.Detail)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Activity insert failed", "err", err)
	}
}

// Activities returns the newest entries, older than before (an ID) when it is set.
func (s *Store) Activities(limit int, before int64) []Activity {
	db, unlock := s.lock()
	defer unlock()
	out := []Activity{}
	if db == nil {
		return out
	}
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := db.Query("SELECT id, ts, who, kind, action, target, detail FROM activity WHERE id < ? ORDER BY id DESC LIMIT ?", before, limit)
	if err != nil {
		log.Warn("Activity query failed", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a Activity
		if rows.Scan(&a.ID, &a.TS, &a.Who, &a.Kind, &a.Action, &a.Target, &a.Detail) == nil {
			out = append(out, a)
		}
	}
	return out
}

// PruneActivity drops entries older than the cutoff.
func (s *Store) PruneActivity(olderThan float64) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM activity WHERE ts < ?", olderThan)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Activity prune failed", "err", err)
	}
}
