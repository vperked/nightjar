package storage

import (
	"database/sql"
)

// Sample is one server's minute: averages, peaks, and Up, the share of its checks it answered.
// A nil field had no reading (no sensor, or no agent).
type Sample struct {
	Host    string   `json:"-"`
	TS      float64  `json:"t"`
	Up      float64  `json:"up"`
	CPU     *float64 `json:"cpu"`
	CPUMax  *float64 `json:"cpu_max"`
	Mem     *float64 `json:"mem"`
	MemMax  *float64 `json:"mem_max"`
	Temp    *float64 `json:"temp"`
	TempMax *float64 `json:"temp_max"`
	Disk    *float64 `json:"disk"`
	Rx      *float64 `json:"rx"`
	Tx      *float64 `json:"tx"`
	Latency *float64 `json:"latency"`
	// LatencyMax is the slowest answer; minutes stored before it was kept use their average.
	LatencyMax *float64 `json:"latency_max"`
	// Minutes is how many stored minutes a bucket stands for (1 for a stored row).
	Minutes int `json:"minutes"`
}

// AddSamples stores finished minutes.
func (s *Store) AddSamples(rows []Sample) {
	if len(rows) == 0 {
		return
	}
	err := s.tx(func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO samples (host, ts, up, cpu, cpu_max, mem, mem_max, temp, temp_max, disk, rx, tx, latency, latency_max) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.Host, r.TS, r.Up, r.CPU, r.CPUMax, r.Mem, r.MemMax, r.Temp, r.TempMax, r.Disk, r.Rx, r.Tx, r.Latency, r.LatencyMax); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && err != ErrUnavailable {
		log.Warn("Samples insert failed", "err", err)
	}
}

// SampleBuckets summarises a host's minutes since `since` into buckets of `width` seconds, oldest
// first: means, peaks, the share answered, and how many minutes each holds. Empty buckets are left out;
// with nothing to show, or no database, it is empty rather than nil.
//
// Buckets of an hour or more read the hourly rollups (see RollupSamples) for the hours rolled up and
// the minutes after them, so a range can reach back past the minutes' own retention.
func (s *Store) SampleBuckets(host string, since, width float64) []Sample {
	out := []Sample{}
	if width <= 0 {
		return out
	}
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return out
	}
	var rows *sql.Rows
	var err error
	if width >= 3600 {
		w := func(col string) string {
			return "SUM(" + col + " * n) / SUM(CASE WHEN " + col + " IS NOT NULL THEN n END)"
		}
		rows, err = db.Query(`WITH cut AS (SELECT COALESCE(MAX(ts) + 3600, 0) AS c FROM samples_hourly WHERE host = ?),
		src AS (
			SELECT ts, up, cpu, cpu_max, mem, mem_max, temp, temp_max, disk, rx, tx, latency, latency_max, minutes AS n
			FROM samples_hourly WHERE host = ? AND ts >= ?
			UNION ALL
			SELECT ts, up, cpu, cpu_max, mem, COALESCE(mem_max, mem), temp, temp_max, disk, rx, tx, latency, COALESCE(latency_max, latency), 1
			FROM samples WHERE host = ? AND ts >= ? AND ts >= (SELECT c FROM cut)
		)
		SELECT ? + CAST((ts - ?) / ? AS INTEGER) * ?, SUM(up * n) / SUM(n), `+w("cpu")+`, MAX(cpu_max), `+w("mem")+`, MAX(mem_max),
			`+w("temp")+`, MAX(temp_max), `+w("disk")+`, `+w("rx")+`, `+w("tx")+`, `+w("latency")+`, MAX(latency_max), SUM(n)
		FROM src GROUP BY CAST((ts - ?) / ? AS INTEGER) ORDER BY 1`,
			host, host, since, host, since, since, since, width, width, since, width)
	} else {
		// A minute stored before its peaks were kept stands in with its average.
		rows, err = db.Query(`SELECT ? + CAST((ts - ?) / ? AS INTEGER) * ?, AVG(up), AVG(cpu), MAX(cpu_max), AVG(mem), MAX(COALESCE(mem_max, mem)),
		AVG(temp), MAX(temp_max), AVG(disk), AVG(rx), AVG(tx), AVG(latency), MAX(COALESCE(latency_max, latency)), COUNT(*)
		FROM samples WHERE host = ? AND ts >= ? GROUP BY CAST((ts - ?) / ? AS INTEGER) ORDER BY 1`,
			since, since, width, width, host, since, since, width)
	}
	if err != nil {
		log.Warn("Samples query failed", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var b Sample
		var cpu, cpuMax, mem, memMax, temp, tempMax, disk, rx, tx, latency, latencyMax sql.NullFloat64
		if rows.Scan(&b.TS, &b.Up, &cpu, &cpuMax, &mem, &memMax, &temp, &tempMax, &disk, &rx, &tx, &latency, &latencyMax, &b.Minutes) != nil {
			continue
		}
		b.Host = host
		b.CPU, b.CPUMax, b.Mem, b.MemMax, b.Temp, b.TempMax = nf(cpu), nf(cpuMax), nf(mem), nf(memMax), nf(temp), nf(tempMax)
		b.Disk, b.Rx, b.Tx, b.Latency, b.LatencyMax = nf(disk), nf(rx), nf(tx), nf(latency), nf(latencyMax)
		out = append(out, b)
	}
	return out
}

// RollupSamples folds every finished hour's minutes into samples_hourly (and its down minutes into
// samples_down), for every host, and returns
// how many hours it wrote. The first run rolls up all the minutes there are. Later runs redo the last
// hour already rolled up too, so a minute stored late (after a stall) still counts.
func (s *Store) RollupSamples(now float64) int {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return 0
	}
	var last sql.NullFloat64
	if err := db.QueryRow("SELECT MAX(ts) FROM samples_hourly").Scan(&last); err != nil {
		log.Warn("Samples rollup failed", "err", err)
		return 0
	}
	from := 0.0
	if last.Valid {
		from = last.Float64
	}
	until := float64(int64(now)/3600) * 3600
	if until <= from {
		return 0
	}
	res, err := db.Exec(`INSERT OR REPLACE INTO samples_hourly (host, ts, up, cpu, cpu_max, mem, mem_max, temp, temp_max, disk, rx, tx, latency, latency_max, minutes)
		SELECT host, CAST(ts / 3600 AS INTEGER) * 3600, AVG(up), AVG(cpu), MAX(COALESCE(cpu_max, cpu)), AVG(mem), MAX(COALESCE(mem_max, mem)),
			AVG(temp), MAX(COALESCE(temp_max, temp)), AVG(disk), AVG(rx), AVG(tx), AVG(latency), MAX(COALESCE(latency_max, latency)), COUNT(*)
		FROM samples WHERE ts >= ? AND ts < ? GROUP BY host, CAST(ts / 3600 AS INTEGER)`, from, until)
	if err != nil {
		log.Warn("Samples rollup failed", "err", err)
		return 0
	}
	n, _ := res.RowsAffected()
	// The down minutes of the same hours, for listing outages after the minutes are gone.
	if _, err := db.Exec(`INSERT OR IGNORE INTO samples_down (host, ts) SELECT host, ts FROM samples WHERE ts >= ? AND ts < ? AND up < 0.5`, from, until); err != nil {
		log.Warn("Samples rollup failed", "err", err)
	}
	return int(n)
}

// PruneHourlySamples deletes rolled-up hours older than the cutoff.
func (s *Store) PruneHourlySamples(olderThan float64) {
	db, unlock := s.lock()
	defer unlock()
	if db != nil {
		if _, err := db.Exec("DELETE FROM samples_hourly WHERE ts < ?", olderThan); err != nil {
			log.Warn("Hourly samples prune failed", "err", err)
		}
		db.Exec("DELETE FROM samples_down WHERE ts < ?", olderThan)
	}
}

// DownMinutes are a host's minutes since `since` in which it missed most of its checks, oldest first:
// from the minutes still kept, and from samples_down for older ones.
func (s *Store) DownMinutes(host string, since float64) []float64 {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT ts FROM samples WHERE host = ? AND ts >= ? AND up < 0.5
		UNION SELECT ts FROM samples_down WHERE host = ? AND ts >= ? ORDER BY 1`, host, since, host, since)
	if err != nil {
		log.Warn("Samples query failed", "err", err)
		return nil
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var ts float64
		if rows.Scan(&ts) == nil {
			out = append(out, ts)
		}
	}
	return out
}

// DeleteSamples forgets a host's history, when it stops being watched.
func (s *Store) DeleteSamples(host string) {
	db, unlock := s.lock()
	defer unlock()
	if db != nil {
		db.Exec("DELETE FROM samples WHERE host = ?", host)
		db.Exec("DELETE FROM samples_hourly WHERE host = ?", host)
		db.Exec("DELETE FROM samples_down WHERE host = ?", host)
	}
}

// PruneSamples deletes minutes older than the cutoff.
func (s *Store) PruneSamples(olderThan float64) {
	db, unlock := s.lock()
	defer unlock()
	if db != nil {
		if _, err := db.Exec("DELETE FROM samples WHERE ts < ?", olderThan); err != nil {
			log.Warn("Samples prune failed", "err", err)
		}
	}
}

func nf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}

// RecordSamples adds a host's stored minutes to a check's history, one result a minute: up when
// most of the minute's polls answered. It moves a server watched with no agent into an uptime check.
func (s *Store) RecordSamples(service string, samples []Sample) error {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return ErrUnavailable
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO checks (service, ts, up, latency_ms, detail) VALUES (?, ?, ?, ?, '')")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, x := range samples {
		if _, err := stmt.Exec(service, x.TS, x.Up >= 0.5, x.Latency); err != nil {
			return err
		}
	}
	return tx.Commit()
}
