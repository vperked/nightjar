// Package analytics keeps every server's readings, this one's and the watched ones', as one row a
// minute, and turns them into the dashboard's analytics: trends over a day, a week or a month,
// uptime and outages, peaks, data moved, and when a disk will fill at the rate it is filling.
package analytics

import (
	"math"
	"slices"
	"sync"
	"time"

	"nightjar/internal/storage"
)

// Local is the host name the monitor's own machine is recorded under.
const Local = "local"

// Reading is one look at a server. A nil field had no reading.
type Reading struct {
	Up                                    bool
	CPU, Mem, Temp, Disk, Rx, Tx, Latency *float64
}

// Recorder folds readings into minutes and stores each minute once it is over. Its methods are
// safe for concurrent use, and do nothing on a nil Recorder.
type Recorder struct {
	store *storage.Store
	Now   func() time.Time

	mu     sync.Mutex
	acc    map[string]*minute
	pruned time.Time
}

func NewRecorder(store *storage.Store) *Recorder {
	return &Recorder{store: store, Now: time.Now, acc: map[string]*minute{}}
}

type minute struct {
	ts                                    float64
	n, ups                                int
	cpu, mem, temp, disk, rx, tx, latency mean
	cpuMax, tempMax                       *float64
}

type mean struct {
	sum float64
	n   int
}

func (m *mean) add(v *float64) {
	if v != nil && !math.IsNaN(*v) {
		m.sum += *v
		m.n++
	}
}

func (m mean) value() *float64 {
	if m.n == 0 {
		return nil
	}
	v := m.sum / float64(m.n)
	return &v
}

func maxOf(cur, v *float64) *float64 {
	if v == nil || (cur != nil && *cur >= *v) {
		return cur
	}
	x := *v
	return &x
}

// Add takes one reading of host. A minute is stored when the first reading of the next arrives.
func (r *Recorder) Add(host string, x Reading) {
	if r == nil {
		return
	}
	ts := float64(r.Now().Unix()/60) * 60
	r.mu.Lock()
	m := r.acc[host]
	var done *storage.Sample
	if m != nil && m.ts != ts {
		s := m.sample(host)
		done, m = &s, nil
	}
	if m == nil {
		m = &minute{ts: ts}
		r.acc[host] = m
	}
	m.n++
	if x.Up {
		m.ups++
	}
	m.cpu.add(x.CPU)
	m.mem.add(x.Mem)
	m.temp.add(x.Temp)
	m.disk.add(x.Disk)
	m.rx.add(x.Rx)
	m.tx.add(x.Tx)
	m.latency.add(x.Latency)
	m.cpuMax, m.tempMax = maxOf(m.cpuMax, x.CPU), maxOf(m.tempMax, x.Temp)
	r.mu.Unlock()
	if done != nil {
		r.store.AddSamples([]storage.Sample{*done})
	}
}

func (m *minute) sample(host string) storage.Sample {
	return storage.Sample{Host: host, TS: m.ts, Up: float64(m.ups) / float64(m.n), CPU: m.cpu.value(), CPUMax: m.cpuMax, Mem: m.mem.value(),
		Temp: m.temp.value(), TempMax: m.tempMax, Disk: m.disk.value(), Rx: m.rx.value(), Tx: m.tx.value(), Latency: m.latency.value()}
}

// Flush stores every minute in progress, on shutdown.
func (r *Recorder) Flush() {
	if r == nil {
		return
	}
	r.mu.Lock()
	rows := make([]storage.Sample, 0, len(r.acc))
	for host, m := range r.acc {
		rows = append(rows, m.sample(host))
	}
	r.acc = map[string]*minute{}
	r.mu.Unlock()
	r.store.AddSamples(rows)
}

// Forget drops a host's minute in progress and its history, when it stops being watched.
func (r *Recorder) Forget(host string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.acc, host)
	r.mu.Unlock()
	r.store.DeleteSamples(host)
}

// Prune drops minutes older than keep, at most once an hour.
func (r *Recorder) Prune(keep time.Duration) {
	if r == nil {
		return
	}
	now := r.Now()
	r.mu.Lock()
	due := now.Sub(r.pruned) >= time.Hour
	if due {
		r.pruned = now
	}
	r.mu.Unlock()
	if due {
		r.store.PruneSamples(float64(now.Add(-keep).Unix()))
	}
}

// ---------- reading it back ----------

// Range is a span the dashboard shows, and the bucket each point stands for.
type Range struct {
	Name  string        `json:"name"`
	Span  time.Duration `json:"-"`
	Step  time.Duration `json:"-"`
	Label string        `json:"label"`
}

// Ranges are the spans on offer, each about 300 points.
var Ranges = []Range{
	{"1h", time.Hour, time.Minute, "1 hour"},
	{"24h", 24 * time.Hour, 5 * time.Minute, "24 hours"},
	{"7d", 7 * 24 * time.Hour, 30 * time.Minute, "7 days"},
	{"30d", 30 * 24 * time.Hour, 2 * time.Hour, "30 days"},
}

// RangeNamed finds a range, defaulting to 24 hours.
func RangeNamed(name string) Range {
	if i := slices.IndexFunc(Ranges, func(r Range) bool { return r.Name == name }); i >= 0 {
		return Ranges[i]
	}
	return Ranges[1]
}

// Incident is a stretch in which a server missed most of its checks.
type Incident struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Ongoing is set when it had not answered again by the last minute stored.
	Ongoing bool `json:"ongoing,omitempty"`
}

// Summary is a host's range in numbers. A nil field had nothing to summarise.
type Summary struct {
	// Coverage is the share of the range with readings, so a server added yesterday says so.
	Coverage float64 `json:"coverage"`
	// Uptime is the share of checks answered, in percent; DowntimeS is the time that adds up to.
	Uptime    *float64 `json:"uptime"`
	DowntimeS float64  `json:"downtime_s"`
	Incidents int      `json:"incidents"`
	CPUAvg    *float64 `json:"cpu_avg"`
	CPUPeak   *float64 `json:"cpu_peak"`
	MemAvg    *float64 `json:"mem_avg"`
	MemPeak   *float64 `json:"mem_peak"`
	TempAvg   *float64 `json:"temp_avg"`
	TempPeak  *float64 `json:"temp_peak"`
	DiskNow   *float64 `json:"disk_now"`
	// DiskChange is how much the fullest disk filled over the range, in points; DiskFullDays when it
	// will be full at that rate (nil when it is not filling, or the range is too short to say).
	DiskChange   *float64 `json:"disk_change"`
	DiskFullDays *float64 `json:"disk_full_days"`
	RxBytes      float64  `json:"rx_bytes"`
	TxBytes      float64  `json:"tx_bytes"`
	LatencyAvg   *float64 `json:"latency_avg"`
	LatencyPeak  *float64 `json:"latency_peak"`
}

// History is one host's points and summary over a range.
type History struct {
	Points    []storage.Sample `json:"points"`
	Summary   Summary          `json:"summary"`
	Incidents []Incident       `json:"incidents"`
}

// Read summarises host over rng, ending now. uptimeKnown is false for the monitor's own machine,
// whose minutes all say up (when it is down nothing is recorded).
func Read(store *storage.Store, host string, rng Range, now time.Time, uptimeKnown bool) History {
	since := float64(now.Add(-rng.Span).Unix())
	points := store.SampleBuckets(host, since, rng.Step.Seconds())
	h := History{Points: points, Summary: summarize(points, rng), Incidents: []Incident{}}
	if uptimeKnown {
		h.Incidents = incidents(store.DownMinutes(host, since), float64(now.Unix()))
		h.Summary.Incidents = len(h.Incidents)
	} else {
		h.Summary.Uptime, h.Summary.DowntimeS = nil, 0
	}
	return h
}

func summarize(points []storage.Sample, rng Range) Summary {
	var s Summary
	var minutes, upMinutes float64
	var cpu, mem, temp, latency mean
	var cpuPeak, memPeak, tempPeak, latencyPeak *float64
	for _, p := range points {
		m := float64(p.Minutes)
		minutes += m
		upMinutes += p.Up * m
		weigh := func(acc *mean, v *float64) {
			if v != nil {
				acc.sum += *v * m
				acc.n += p.Minutes
			}
		}
		weigh(&cpu, p.CPU)
		weigh(&mem, p.Mem)
		weigh(&temp, p.Temp)
		weigh(&latency, p.Latency)
		cpuPeak, memPeak, tempPeak, latencyPeak = maxOf(cpuPeak, p.CPUMax), maxOf(memPeak, p.Mem), maxOf(tempPeak, p.TempMax), maxOf(latencyPeak, p.Latency)
		if p.Rx != nil {
			s.RxBytes += *p.Rx * m * 60
		}
		if p.Tx != nil {
			s.TxBytes += *p.Tx * m * 60
		}
		if p.Disk != nil {
			s.DiskNow = p.Disk
		}
	}
	s.Coverage = min(1, minutes*60/rng.Span.Seconds())
	if minutes > 0 {
		u := 100 * upMinutes / minutes
		s.Uptime, s.DowntimeS = &u, (minutes-upMinutes)*60
	}
	weighted := func(acc mean) *float64 {
		if acc.n == 0 {
			return nil
		}
		v := acc.sum / float64(acc.n)
		return &v
	}
	s.CPUAvg, s.MemAvg, s.TempAvg, s.LatencyAvg = weighted(cpu), weighted(mem), weighted(temp), weighted(latency)
	s.CPUPeak, s.MemPeak, s.TempPeak, s.LatencyPeak = cpuPeak, memPeak, tempPeak, latencyPeak
	s.DiskChange, s.DiskFullDays = diskTrend(points)
	return s
}

// diskTrend fits a line through the fullest disk's points: how much it filled over them, and in
// how many days it reaches 100% at that rate. Under half a day of readings says nothing yet.
func diskTrend(points []storage.Sample) (change, fullDays *float64) {
	var xs, ys []float64
	for _, p := range points {
		if p.Disk != nil {
			xs, ys = append(xs, p.TS), append(ys, *p.Disk)
		}
	}
	if len(xs) < 6 || xs[len(xs)-1]-xs[0] < 12*3600 {
		return nil, nil
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(xs))
	var sxy, sxx float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
	}
	if sxx == 0 {
		return nil, nil
	}
	slope := sxy / sxx // points a second
	c := slope * (xs[len(xs)-1] - xs[0])
	change = &c
	perDay := slope * 86400
	last := ys[len(ys)-1]
	// Filling by less than a hundredth of a point a day is not filling.
	if perDay > 0.01 && last < 100 {
		d := (100 - last) / perDay
		if d < 3650 {
			fullDays = &d
		}
	}
	return change, fullDays
}

// incidents groups down minutes into outages: minutes less than two apart are one.
func incidents(down []float64, now float64) []Incident {
	out := []Incident{}
	for _, ts := range down {
		if n := len(out); n > 0 && ts-out[n-1].End <= 60 {
			out[n-1].End = ts + 60
			continue
		}
		out = append(out, Incident{Start: ts, End: ts + 60})
	}
	// The current minute is not stored yet, so an outage that ran up to it is still going.
	if n := len(out); n > 0 && now-out[n-1].End < 120 {
		out[n-1].Ongoing = true
	}
	slices.Reverse(out) // newest first
	return out
}
