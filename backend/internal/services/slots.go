package services

import "servermonitor/internal/storage"

// Checks are summarised in 5-minute slots covering the longest reported window (30 days),
// so uptime and the 7d / 30d charts are short in-memory scans with no database work per request.
const (
	slotSeconds = 300
	slotCount   = 30 * 86400 / slotSeconds
	slotSpan    = slotSeconds * slotCount
)

// slot summarises the checks that landed in one 5-minute slot.
type slot struct {
	index      int64
	checks     int32
	ups        int32
	latencyN   int32
	latencySum float64
	failDetail string
}

// slotRing is indexed by slot number modulo slotCount; a slot from a previous lap is reset on reuse.
type slotRing []slot

func (r slotRing) at(ts float64) *slot {
	idx := int64(ts) / slotSeconds
	s := &r[idx%slotCount]
	if s.index != idx {
		*s = slot{index: idx}
	}
	return s
}

// record adds one live check result.
func (r slotRing) record(ts float64, res Result) {
	s := r.at(ts)
	s.checks++
	if !res.Up {
		s.failDetail = res.Detail
		return
	}
	s.ups++
	if res.LatencyMS != nil {
		s.latencyN++
		s.latencySum += *res.LatencyMS
	}
}

// merge adds a slot's worth of stored history, at startup.
func (r slotRing) merge(c storage.Count) {
	s := r.at(c.TS)
	s.checks += int32(c.Checks)
	s.ups += int32(c.Ups)
	s.latencyN += int32(c.LatencyN)
	s.latencySum += c.LatencySum
	if c.FailDetail != "" {
		s.failDetail = c.FailDetail
	}
}

// uptime is the percent of checks up from the slot containing `cutoff`, or nil with no checks.
func (r slotRing) uptime(cutoff float64) *float64 {
	first := int64(cutoff) / slotSeconds
	var checks, ups int64
	for _, s := range r {
		if s.index >= first && s.checks > 0 {
			checks += int64(s.checks)
			ups += int64(s.ups)
		}
	}
	if checks == 0 {
		return nil
	}
	v := 100 * float64(ups) / float64(checks)
	return &v
}

// buckets merges the slots between since and until into at most n points, oldest first, in the
// same shape the store's bucketed query returns.
func (r slotRing) buckets(since, until float64, n int) []storage.Point {
	type acc struct {
		slot
		ts, failTS float64
		seen       bool
	}
	width := (until - since) / float64(n)
	first, last := int64(since)/slotSeconds, int64(until)/slotSeconds
	accs := make([]acc, n)
	for _, s := range r {
		if s.checks == 0 || s.index < first || s.index > last {
			continue
		}
		ts := float64(s.index * slotSeconds)
		a := &accs[min(n-1, max(0, int((ts-since)/width)))]
		if !a.seen || ts < a.ts {
			a.ts, a.seen = ts, true
		}
		a.checks += s.checks
		a.ups += s.ups
		a.latencyN += s.latencyN
		a.latencySum += s.latencySum
		if s.failDetail != "" && ts >= a.failTS {
			a.failDetail, a.failTS = s.failDetail, ts
		}
	}
	var out []storage.Point
	for _, a := range accs {
		if !a.seen {
			continue
		}
		p := storage.Point{TS: a.ts, Fails: int(a.checks - a.ups), Detail: a.failDetail}
		p.Up = p.Fails == 0
		if a.latencyN > 0 {
			v := a.latencySum / float64(a.latencyN)
			p.LatencyMS = &v
		}
		out = append(out, p)
	}
	return out
}
