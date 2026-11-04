package storage

import "database/sql"

// PlexPlay is one finished Plex stream.
type PlexPlay struct {
	Started    float64 `json:"started"`
	Ended      float64 `json:"ended"`
	User       string  `json:"user"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Subtitle   string  `json:"subtitle"`
	Player     string  `json:"player"`
	Decision   string  `json:"decision"`
	Local      bool    `json:"local"`
	Progress   float64 `json:"progress"`
	DurationMS int64   `json:"duration_ms"`
	Reason     string  `json:"reason,omitempty"`
}

// AddPlexPlay keeps a finished stream.
func (s *Store) AddPlexPlay(p PlexPlay) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO plex_plays (started, ended, user, kind, title, subtitle, player, decision, local, progress, duration_ms, reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Started, p.Ended, p.User, p.Kind, p.Title, p.Subtitle, p.Player, p.Decision, p.Local, p.Progress, p.DurationMS, p.Reason)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Plex play insert failed", "err", err)
	}
}

// PlexPlays returns the streams that started since a time, oldest first.
func (s *Store) PlexPlays(since float64) []PlexPlay {
	db, unlock := s.lock()
	defer unlock()
	out := []PlexPlay{}
	if db == nil {
		return out
	}
	rows, err := db.Query(`SELECT started, ended, user, kind, title, subtitle, player, decision, local, progress, duration_ms, reason
		FROM plex_plays WHERE started >= ? ORDER BY started LIMIT 50000`, since)
	if err != nil {
		log.Warn("Plex play query failed", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p PlexPlay
		if rows.Scan(&p.Started, &p.Ended, &p.User, &p.Kind, &p.Title, &p.Subtitle, &p.Player, &p.Decision, &p.Local, &p.Progress, &p.DurationMS, &p.Reason) == nil {
			out = append(out, p)
		}
	}
	return out
}

// PrunePlexPlays drops streams older than the cutoff.
func (s *Store) PrunePlexPlays(olderThan float64) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM plex_plays WHERE started < ?", olderThan)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Plex play prune failed", "err", err)
	}
}
