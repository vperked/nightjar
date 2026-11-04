package storage

import "database/sql"

// Repair is one attempt Nightjar made to bring a down check back: what it ran, how that went,
// and Fixed once the check came back up after it.
type Repair struct {
	ID      int64   `json:"id"`
	TS      float64 `json:"ts"`
	Service string  `json:"check"`
	Action  string  `json:"action"`
	Target  string  `json:"target"`
	OK      bool    `json:"ok"`
	Detail  string  `json:"detail"`
	Output  string  `json:"output"`
	Fixed   bool    `json:"fixed"`
}

// AddRepair stores an attempt and returns its ID (0 when the store is unavailable).
func (s *Store) AddRepair(r Repair) int64 {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return 0
	}
	res, err := db.Exec("INSERT INTO repairs (ts, service, action, target, ok, detail, output) VALUES (?, ?, ?, ?, ?, ?, ?)",
		r.TS, r.Service, r.Action, r.Target, r.OK, r.Detail, r.Output)
	if err != nil {
		log.Warn("Repair insert failed", "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// MarkRepairFixed records that the check came back after the attempt.
func (s *Store) MarkRepairFixed(id int64) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE repairs SET fixed = 1 WHERE id = ?", id)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Repair update failed", "err", err)
	}
}

// Repairs returns the newest attempts, for one check or (with an empty name) all of them.
func (s *Store) Repairs(service string, limit int) []Repair {
	db, unlock := s.lock()
	defer unlock()
	out := []Repair{}
	if db == nil {
		return out
	}
	rows, err := db.Query(`SELECT id, ts, service, action, target, ok, detail, output, fixed FROM repairs
		WHERE ? = '' OR service = ? ORDER BY id DESC LIMIT ?`, service, service, limit)
	if err != nil {
		log.Warn("Repair query failed", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r Repair
		if rows.Scan(&r.ID, &r.TS, &r.Service, &r.Action, &r.Target, &r.OK, &r.Detail, &r.Output, &r.Fixed) == nil {
			out = append(out, r)
		}
	}
	return out
}

// RenameRepairs keeps a check's attempts with it when it is renamed.
func (s *Store) RenameRepairs(oldName, newName string) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE repairs SET service = ? WHERE service = ?", newName, oldName)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Repair rename failed", "err", err)
	}
}

// PruneRepairs drops attempts older than the cutoff.
func (s *Store) PruneRepairs(olderThan float64) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM repairs WHERE ts < ?", olderThan)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Repair prune failed", "err", err)
	}
}
