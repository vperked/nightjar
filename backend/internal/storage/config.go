package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrUnavailable is returned by writes when the database could not be opened.
var ErrUnavailable = errors.New("database unavailable")

// Settings returns the settings saved from the dashboard, by environment key.
func (s *Store) Settings() map[string]string {
	out := map[string]string{}
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return out
	}
	rows, err := s.db.Query("SELECT key, value FROM settings")
	if err != nil {
		log.Warn("Settings query failed", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

// SaveSettings stores dashboard settings; an empty value removes the key, restoring the
// environment's value or the default.
func (s *Store) SaveSettings(values map[string]string) error {
	return s.tx(func(tx *sql.Tx) error { return saveSettings(tx, values) })
}

func saveSettings(tx *sql.Tx, values map[string]string) error {
	for k, v := range values {
		var err error
		if v == "" {
			_, err = tx.Exec("DELETE FROM settings WHERE key = ?", k)
		} else {
			_, err = tx.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Restore saves meta values and settings (as SaveSettings does) in one transaction, so a backup is
// put back whole or not at all.
func (s *Store) Restore(meta, settings map[string]string) error {
	return s.tx(func(tx *sql.Tx) error {
		for k, v := range meta {
			if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v); err != nil {
				return err
			}
		}
		return saveSettings(tx, settings)
	})
}

// Monitors returns the stored check definitions (JSON) in display order.
func (s *Store) Monitors() [][]byte {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := s.db.Query("SELECT spec FROM monitors ORDER BY position, name")
	if err != nil {
		log.Warn("Monitors query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var spec []byte
		if rows.Scan(&spec) == nil {
			out = append(out, spec)
		}
	}
	return out
}

// SaveMonitor inserts or replaces a check definition. Renaming (oldName != name) keeps its place
// in the list and moves its history to the new name.
func (s *Store) SaveMonitor(oldName, name string, spec []byte) error {
	return s.tx(func(tx *sql.Tx) error {
		if oldName != "" && oldName != name {
			if _, err := tx.Exec("UPDATE monitors SET name = ?, spec = ? WHERE name = ?", name, spec, oldName); err != nil {
				return err
			}
			_, err := tx.Exec("UPDATE checks SET service = ? WHERE service = ?", name, oldName)
			return err
		}
		_, err := tx.Exec(`INSERT INTO monitors (name, position, spec)
			VALUES (?, (SELECT COALESCE(MAX(position), 0) + 1 FROM monitors), ?)
			ON CONFLICT(name) DO UPDATE SET spec = excluded.spec`, name, spec)
		return err
	})
}

// DeleteMonitor removes a check definition and its history.
func (s *Store) DeleteMonitor(name string) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM monitors WHERE name = ?", name); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM checks WHERE service = ?", name)
		return err
	})
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return ErrUnavailable
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return fmt.Errorf("database: %w", err)
	}
	return tx.Commit()
}
