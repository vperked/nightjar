// Package storage keeps the monitor's SQLite database: check results (for uptime percentages and
// response-time series), the checks themselves, settings changed in the dashboard, what to keep
// updated, every alert the monitor raised, and who is signed in.
package storage

import (
	"database/sql"
	"os"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"nightjar/internal/logx"
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
-- The unread ones alone, which the header's bell counts every time it polls.
CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications (severity) WHERE read = 0;
CREATE TABLE IF NOT EXISTS sessions (hash TEXT PRIMARY KEY, created REAL NOT NULL, seen REAL NOT NULL, agent TEXT NOT NULL DEFAULT '', user TEXT NOT NULL DEFAULT '');
-- One row per server per minute, for analytics: averages, peaks and the share of checks it answered.
CREATE TABLE IF NOT EXISTS samples (
    host TEXT NOT NULL,
    ts REAL NOT NULL,
    up REAL NOT NULL,
    cpu REAL, cpu_max REAL, mem REAL, temp REAL, temp_max REAL, disk REAL, rx REAL, tx REAL, latency REAL,
    mem_max REAL, latency_max REAL
);
CREATE INDEX IF NOT EXISTS idx_samples_host_ts ON samples (host, ts);
-- For rolling up and pruning every host's minutes at once.
CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples (ts);
-- The same minutes rolled up into hours, kept far longer than the minutes, for trends over months.
-- minutes is how many stored minutes each hour holds; averages are over those minutes.
CREATE TABLE IF NOT EXISTS samples_hourly (
    host TEXT NOT NULL,
    ts REAL NOT NULL,
    up REAL NOT NULL,
    cpu REAL, cpu_max REAL, mem REAL, mem_max REAL, temp REAL, temp_max REAL, disk REAL, rx REAL, tx REAL,
    latency REAL, latency_max REAL,
    minutes INTEGER NOT NULL,
    PRIMARY KEY (host, ts)
);
CREATE INDEX IF NOT EXISTS idx_samples_hourly_ts ON samples_hourly (ts);
-- The minutes a server missed most of its checks in, kept as long as the hourly rows, so outages
-- can be listed over months.
CREATE TABLE IF NOT EXISTS samples_down (host TEXT NOT NULL, ts REAL NOT NULL, PRIMARY KEY (host, ts));
-- What Nightjar did to repair a check that went down, and whether the check came back after it.
CREATE TABLE IF NOT EXISTS repairs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    service TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    ok INTEGER NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    output TEXT NOT NULL DEFAULT '',
    fixed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_repairs_service_ts ON repairs (service, ts);
-- Every Plex stream that finished, for the weekly report and the Plex page's stats.
CREATE TABLE IF NOT EXISTS plex_plays (
    started REAL NOT NULL,
    ended REAL NOT NULL,
    user TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    subtitle TEXT NOT NULL DEFAULT '',
    player TEXT NOT NULL DEFAULT '',
    decision TEXT NOT NULL DEFAULT '',
    local INTEGER NOT NULL DEFAULT 0,
    progress REAL NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_plex_plays_started ON plex_plays (started);
-- What was done, and by whom: on the dashboard, through Discord's buttons, or by Nightjar.
CREATE TABLE IF NOT EXISTS activity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    who TEXT NOT NULL,
    kind TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT ''
);
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

// added are columns later versions added to tables an older database already has.
var added = []struct{ table, column, kind string }{
	{"samples", "mem_max", "REAL"},
	{"samples", "latency_max", "REAL"},
	{"sessions", "user", "TEXT NOT NULL DEFAULT ''"},
}

// migrate adds the columns an older database lacks.
func migrate(db *sql.DB) error {
	for _, a := range added {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", a.table, a.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE " + a.table + " ADD COLUMN " + a.column + " " + a.kind); err != nil {
				return err
			}
		}
	}
	return nil
}

// Open opens (creating if needed) the database at path. The returned store may be unavailable.
func Open(path string) *Store {
	// Another process holding the database for a moment (a backup, the sqlite3 shell) is waited
	// for rather than leaving the monitor without its database.
	dsn := path + "?_pragma=busy_timeout(5000)"
	if strings.Contains(path, "?") {
		dsn = path + "&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err == nil {
		db.SetMaxOpenConns(1)
		_, err = db.Exec("PRAGMA journal_mode=WAL;" + schema)
	}
	if err == nil {
		err = migrate(db)
	}
	if err != nil {
		log.Warn("Database unavailable", "path", path, "err", err)
		if db != nil {
			db.Close()
		}
		return &Store{}
	}
	// It holds agent tokens, password and session hashes and the two-factor secret: readable by its
	// owner alone, whatever the umask, and so are the WAL files SQLite keeps beside it.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		os.Chmod(p, 0o600)
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

// LastCheckTime is when the latest check result of any service was stored, or 0 for none: about
// when Nightjar was last running.
func (s *Store) LastCheckTime() float64 {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return 0
	}
	var ts sql.NullFloat64
	s.db.QueryRow("SELECT MAX(ts) FROM checks").Scan(&ts)
	return ts.Float64
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

// Prune deletes results older than the cutoff, and the repair attempts made as long ago, and
// returns how many results were removed.
func (s *Store) Prune(olderThan float64) int64 {
	n := s.pruneChecks(olderThan)
	s.PruneRepairs(olderThan)
	return n
}

func (s *Store) pruneChecks(olderThan float64) int64 {
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

// Day is one calendar day of a service's checks.
type Day struct {
	Start  float64 // local midnight, unix seconds
	Checks int
	Ups    int
}

// Days summarises a service's checks in each day, from its start to the next one's (the last day's
// to end), oldest first. Days without checks are left out. The caller gives every day's start, its
// own local midnight: today's UTC offset, used for every day, puts each day before a clock change
// an hour out.
func (s *Store) Days(service string, starts []float64, end float64) []Day {
	db, unlock := s.lock()
	defer unlock()
	if db == nil || len(starts) == 0 {
		return nil
	}
	var args []any
	values := make([]string, len(starts))
	for i, start := range starts {
		next := end
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		values[i] = "(?, ?)"
		args = append(args, start, next)
	}
	rows, err := db.Query(`WITH days(start, stop) AS (VALUES `+strings.Join(values, ", ")+`)
		SELECT days.start, COUNT(*), SUM(checks.up) FROM days JOIN checks ON checks.service = ? AND checks.ts >= days.start AND checks.ts < days.stop
		GROUP BY days.start ORDER BY days.start`, append(args, service)...)
	if err != nil {
		log.Warn("History query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []Day
	for rows.Next() {
		var d Day
		if rows.Scan(&d.Start, &d.Checks, &d.Ups) == nil {
			out = append(out, d)
		}
	}
	return out
}
