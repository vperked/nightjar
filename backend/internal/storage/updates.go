package storage

import (
	"database/sql"
	"errors"
)

// UpdateTarget is one stored update target: its definition and its last known state (both JSON).
type UpdateTarget struct {
	ID          string
	Spec, State []byte
}

// UpdateTargets returns the stored update targets in display order.
func (s *Store) UpdateTargets() []UpdateTarget {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := s.db.Query("SELECT id, spec, state FROM update_targets ORDER BY position, id")
	if err != nil {
		log.Warn("Update targets query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []UpdateTarget
	for rows.Next() {
		var t UpdateTarget
		if rows.Scan(&t.ID, &t.Spec, &t.State) == nil {
			out = append(out, t)
		}
	}
	return out
}

// SaveUpdateTarget inserts or replaces a target's definition, keeping its place and state.
func (s *Store) SaveUpdateTarget(id string, spec []byte) error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO update_targets (id, position, spec)
			VALUES (?, (SELECT COALESCE(MAX(position), 0) + 1 FROM update_targets), ?)
			ON CONFLICT(id) DO UPDATE SET spec = excluded.spec`, id, spec)
		return err
	})
}

// SaveUpdateState stores a target's last known state.
func (s *Store) SaveUpdateState(id string, state []byte) error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE update_targets SET state = ? WHERE id = ?", state, id)
		return err
	})
}

// DeleteUpdateTarget removes a target.
func (s *Store) DeleteUpdateTarget(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM update_targets WHERE id = ?", id)
		return err
	})
}

// Meta returns a value the monitor keeps for itself ("" when unset, or when it cannot be read).
func (s *Store) Meta(key string) string {
	v, _ := s.MetaValue(key)
	return v
}

// MetaValue returns a value the monitor keeps for itself, "" when unset. Unlike Meta it says when
// the value could not be read (ErrUnavailable when there is no database), for state where "unset"
// and "unknown" must not be confused.
func (s *Store) MetaValue(key string) (string, error) {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return "", ErrUnavailable
	}
	var v string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return v, nil
}

// SetMeta stores a value the monitor keeps for itself.
func (s *Store) SetMeta(key, value string) error {
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
		return err
	})
}
