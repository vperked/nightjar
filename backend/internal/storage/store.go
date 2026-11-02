// Package storage keeps the monitor's SQLite database: check results (for uptime percentages and
// response-time series), the checks themselves, settings changed in the dashboard, what to keep
// updated, every alert the monitor raised, and who is signed in.
package storage

import (
	"database/sql"
	"sync"

	_ "modernc.org/sqlite"

	"servermonitor/internal/logx"
)

var log = logx.Component("Storage")

const schema = `
CREATE TABLE IF NOT EXISTS checks (
    service TEXT NOT NULL,
    ts REAL NOT NULL,
    up INTEGER NOT NULL,
    latency_ms REAL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_checks_ts ON checks (ts);
-- Serves the per-service range scans; replaces the older (service, ts) index.
CREATE INDEX IF NOT EXISTS idx_checks_service_ts_up ON checks (service, ts, up);
DROP INDEX IF EXISTS idx_checks_service_ts;
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS monitors (name TEXT PRIMARY KEY, position INTEGER NOT NULL, spec TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS update_targets (id TEXT PRIMARY KEY, position INTEGER NOT NULL, spec TEXT NOT NULL, state TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    area TEXT NOT NULL,
    severity TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    fields TEXT NOT NULL DEFAULT '[]',
    subject TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    channels TEXT NOT NULL DEFAULT '[]',
    error TEXT NOT NULL DEFAULT '',
    read INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_notifications_ts ON notifications (ts);
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications (status);
CREATE TABLE IF NOT EXISTS sessions (hash TEXT PRIMARY KEY, created REAL NOT NULL, seen REAL NOT NULL, agent TEXT NOT NULL DEFAULT '');
`

// Point is one stored check result.
type Point struct {
	TS        float64  `json:"ts"`
	Up        bool     `json:"up"`
	LatencyMS *float64 `json:"latency_ms"`
	Detail    string   `json:"detail,omitempty"`
	// Fails counts the failed checks a bucketed point stands for; Up is false when it is non-zero.
	Fails int `json:"fails,omitempty"`
}

// Store is safe for concurrent use and never returns database errors to callers:
// a broken store is logged and monitoring carries on without history.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// Open opens (creating if needed) the database at path. The returned store may be unavailable.
func Open(path string) *Store {
	db, err := sql.Open("sqlite", path)
	if err == nil {
		db.SetMaxOpenConns(1)
		_, err = db.Exec("PRAGMA journal_mode=WAL;" + schema)
	}
	if err != nil {
		log.Warn("Database unavailable", "path", path, "err", err)
		if db != nil {
			db.Close()
		}
		return &Store{}
	}
	return &Store{db: db}
}

// lock takes the store's lock and returns the database, nil when it is closed or was never open
// (a nil store included). Checking under the lock keeps a write from racing Close.
func (s *Store) lock() (*sql.DB, func()) {
	if s == nil {
		return nil, func() {}
	}
	s.mu.Lock()
	return s.db, s.mu.Unlock
}

func (s *Store) Available() bool {
	db, unlock := s.lock()
	defer unlock()
	return db != nil
}

// Close closes the database; later calls see it as unavailable.
func (s *Store) Close() {
	db, unlock := s.lock()
	defer unlock()
	if db != nil {
		db.Close()
		s.db = nil
	}
}

// Record appends one result.
func (s *Store) Record(service string, ts float64, up bool, latency *float64, detail string) {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return
	}
	if _, err := s.db.Exec("INSERT INTO checks (service, ts, up, latency_ms, detail) VALUES (?, ?, ?, ?, ?)",
		service, ts, up, latency, detail); err != nil {
		log.Warn("History insert failed", "err", err)
	}
}

// Count summarises the checks in one time slot starting at TS: how many, how many were up,
// the latency total of successful checks, and one failure's detail.
type Count struct {
	TS         float64
	Checks     int
	Ups        int
	LatencyN   int
	LatencySum float64
	FailDetail string
}

// Counts groups the last `slots` slots of `width` seconds by slot, for rebuilding the in-memory
// uptime and chart summaries at startup.
func (s *Store) Counts(service string, width float64, slots int) []Count {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT CAST(ts / ?1 AS INTEGER) * ?1, COUNT(*), SUM(up),
		COUNT(CASE WHEN up THEN latency_ms END), COALESCE(SUM(CASE WHEN up THEN latency_ms END), 0),
		COALESCE(MAX(CASE WHEN NOT up THEN detail END), '')
		FROM checks WHERE service = ?2 AND ts >= (CAST(strftime('%s', 'now') AS REAL) - ?1 * ?3) GROUP BY 1`, width, service, slots)
	if err != nil {
		log.Warn("History query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		if rows.Scan(&c.TS, &c.Checks, &c.Ups, &c.LatencyN, &c.LatencySum, &c.FailDetail) == nil {
			out = append(out, c)
		}
	}
	return out
}

// LastStatus reports whether the most recent stored check was up; ok is false when there is none.
func (s *Store) LastStatus(service string) (isUp, ok bool) {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return false, false
	}
	err := s.db.QueryRow("SELECT up FROM checks WHERE service = ? ORDER BY ts DESC LIMIT 1", service).Scan(&isUp)
	return isUp, err == nil
}

// Series returns the most recent `limit` results since `since`, oldest first.
func (s *Store) Series(service string, since float64, limit int) []Point {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := s.db.Query("SELECT ts, up, latency_ms, detail FROM checks WHERE service = ? AND ts >= ? ORDER BY ts DESC LIMIT ?",
		service, since, max(1, limit))
	if err != nil {
		log.Warn("History query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		var latency sql.NullFloat64
		if rows.Scan(&p.TS, &p.Up, &latency, &p.Detail) == nil {
			if latency.Valid {
				v := latency.Float64
				p.LatencyMS = &v
			}
			out = append(out, p)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Buckets summarises results since `since` into at most n time buckets, oldest first: the first
// timestamp, mean latency of successful checks, failure count and the latest failure detail.
// Buckets narrower than the check interval hold one check each, so short ranges come back unchanged.
func (s *Store) Buckets(service string, since, until float64, n int) []Point {
	if until <= since || n < 1 {
		return nil
	}
	width := (until - since) / float64(n)
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT MIN(ts), AVG(CASE WHEN up THEN latency_ms END), COUNT(*) - SUM(up),
		COALESCE(MAX(CASE WHEN NOT up THEN detail END), '')
		FROM checks WHERE service = ? AND ts >= ? GROUP BY MIN(CAST((ts - ?) / ? AS INTEGER), ?) ORDER BY 1`,
		service, since, since, width, n-1)
	if err != nil {
		log.Warn("History query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		var latency sql.NullFloat64
		if rows.Scan(&p.TS, &latency, &p.Fails, &p.Detail) != nil {
			continue
		}
		p.Up = p.Fails == 0
		if latency.Valid {
			v := latency.Float64
			p.LatencyMS = &v
		}
		out = append(out, p)
	}
	return out
}

// Prune deletes results older than the cutoff and returns how many were removed.
func (s *Store) Prune(olderThan float64) int64 {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return 0
	}
	res, err := s.db.Exec("DELETE FROM checks WHERE ts < ?", olderThan)
	if err != nil {
		log.Warn("History prune failed", "err", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}
