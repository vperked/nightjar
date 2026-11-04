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
	Temp    *float64 `json:"temp"`
	TempMax *float64 `json:"temp_max"`
	Disk    *float64 `json:"disk"`
	Rx      *float64 `json:"rx"`
	Tx      *float64 `json:"tx"`
	Latency *float64 `json:"latency"`
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
			if _, err := tx.Exec(`INSERT INTO samples (host, ts, up, cpu, cpu_max, mem, temp, temp_max, disk, rx, tx, latency) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.Host, r.TS, r.Up, r.CPU, r.CPUMax, r.Mem, r.Temp, r.TempMax, r.Disk, r.Rx, r.Tx, r.Latency); err != nil {
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
// first: means, peaks, the share answered, and how many minutes each holds. Empty buckets are left out.
func (s *Store) SampleBuckets(host string, since, width float64) []Sample {
	if width <= 0 {
		return nil
	}
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT ? + CAST((ts - ?) / ? AS INTEGER) * ?, AVG(up), AVG(cpu), MAX(cpu_max), AVG(mem), AVG(temp), MAX(temp_max), AVG(disk), AVG(rx), AVG(tx), AVG(latency), COUNT(*)
		FROM samples WHERE host = ? AND ts >= ? GROUP BY CAST((ts - ?) / ? AS INTEGER) ORDER BY 1`,
		since, since, width, width, host, since, since, width)
	if err != nil {
		log.Warn("Samples query failed", "err", err)
		return nil
	}
	defer rows.Close()
	out := []Sample{}
	for rows.Next() {
		var b Sample
		var cpu, cpuMax, mem, temp, tempMax, disk, rx, tx, latency sql.NullFloat64
		if rows.Scan(&b.TS, &b.Up, &cpu, &cpuMax, &mem, &temp, &tempMax, &disk, &rx, &tx, &latency, &b.Minutes) != nil {
			continue
		}
		b.Host = host
		b.CPU, b.CPUMax, b.Mem, b.Temp, b.TempMax, b.Disk, b.Rx, b.Tx, b.Latency = nf(cpu), nf(cpuMax), nf(mem), nf(temp), nf(tempMax), nf(disk), nf(rx), nf(tx), nf(latency)
		out = append(out, b)
	}
	return out
}

// DownMinutes are a host's minutes since `since` in which it missed most of its checks, oldest first.
func (s *Store) DownMinutes(host string, since float64) []float64 {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT ts FROM samples WHERE host = ? AND ts >= ? AND up < 0.5 ORDER BY ts`, host, since)
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
