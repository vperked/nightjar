package plex

import (
	"errors"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Source is a Plex server: the HTTP Client, or a fake in tests.
type Source interface {
	URL() string
	Server() (Server, error)
	AvailableUpdate(current string) string
	Sessions() ([]Stream, error)
	Libraries() ([]Library, error)
	Terminate(sessionID, reason string) error
}

const (
	StatusPending = "pending"
	StatusUp      = "up"
	StatusDown    = "down"

	EventDown        = "down"
	EventUp          = "up"
	EventStreamStart = "stream_start"
	EventStreamEnd   = "stream_end"

	infoInterval    = 300.0
	libraryInterval = 900.0
	maxSamples      = 2880
	maxRecent       = 50
	// chartPoints caps the samples one API response carries; older ones are merged by bucket.
	chartPoints = 360
)

// Sample is one poll's stream counts, for the concurrency and bandwidth charts.
type Sample struct {
	TS            float64 `json:"ts"`
	Direct        int     `json:"direct"`
	Transcode     int     `json:"transcode"`
	BandwidthKbps int     `json:"bandwidth_kbps"`
}

// Play is a finished stream, as last seen before it disappeared.
type Play struct {
	User     string  `json:"user"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	Player   string  `json:"player"`
	Decision string  `json:"decision"`
	Started  float64 `json:"started"`
	Ended    float64 `json:"ended"`
	Progress float64 `json:"progress"`
	Local    bool    `json:"local"`
	// DurationMS is the length of what was played.
	DurationMS int64 `json:"duration_ms"`
	// Reason is why it was converted, if it was.
	Reason string `json:"reason,omitempty"`
	// Last is the stream as it was last seen playing: how it was delivered, its formats, the player.
	Last *Stream `json:"last,omitempty"`
}

// Event is a state change the caller may want to notify about.
type Event struct {
	Kind     string
	Detail   string
	Downtime float64
	Stream   Stream
	Play     Play // a finished stream, with EventStreamEnd
}

// Snapshot is the monitor's state for the API.
type Snapshot struct {
	URL           string    `json:"url"`
	Status        string    `json:"status"`
	Detail        string    `json:"detail"`
	Since         float64   `json:"since"`
	CheckedAt     float64   `json:"checked_at"`
	LatencyMS     *float64  `json:"latency_ms"`
	Server        *Server   `json:"server"`
	Streams       []Stream  `json:"streams"`
	Direct        int       `json:"direct"`
	Transcode     int       `json:"transcode"`
	BandwidthKbps int       `json:"bandwidth_kbps"`
	WANKbps       int       `json:"wan_kbps"`
	Libraries     []Library `json:"libraries"`
	LibrariesAt   float64   `json:"libraries_at"`
	Samples       []Sample  `json:"samples"`
	Recent        []Play    `json:"recent"`
}

// Monitor polls on its own interval. A server is down after more than Retries consecutive failures.
type Monitor struct {
	src      Source
	interval float64
	retries  int

	mu        sync.RWMutex
	status    string
	detail    string
	since     float64
	checkedAt float64
	latency   *float64
	failures  int
	server    *Server
	streams   []Stream
	active    map[string]Stream
	seeded    bool
	libraries []Library
	libsAt    float64
	samples   []Sample
	recent    []Play

	nextPoll, nextInfo, nextLibs float64
	libsBusy                     atomic.Bool
}

// NewMonitor polls src every interval seconds; the server is down after more than retries failed polls.
func NewMonitor(src Source, interval float64, retries int) *Monitor {
	return &Monitor{src: src, interval: interval, retries: retries, status: StatusPending, active: map[string]Stream{}}
}

// Poll checks the server if its interval has elapsed and returns what changed.
func (m *Monitor) Poll(now float64) []Event {
	if now < m.nextPoll {
		return nil
	}
	m.nextPoll = now + m.interval
	start := time.Now()
	streams, err := m.src.Sessions()
	latency := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return m.fail(now, err)
	}
	var server *Server
	if now >= m.nextInfo {
		if srv, err := m.src.Server(); err == nil {
			srv.UpdateAvailable = m.src.AvailableUpdate(srv.Version)
			server = &srv
			m.nextInfo = now + infoInterval
		}
	}
	if now >= m.nextLibs && m.libsBusy.CompareAndSwap(false, true) {
		m.nextLibs = now + libraryInterval
		go m.refreshLibraries()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var events []Event
	if m.status != StatusUp {
		if m.status == StatusDown {
			events = append(events, Event{Kind: EventUp, Downtime: now - m.since})
		}
		m.status, m.since = StatusUp, now
	}
	m.failures, m.detail, m.checkedAt, m.latency = 0, "", now, &latency
	if server != nil {
		m.server = server
	}
	sample := Sample{TS: now}
	seen := map[string]bool{}
	for i := range streams {
		s := &streams[i]
		seen[s.ID] = true
		s.PeakKbps = s.BandwidthKbps
		if prev, ok := m.active[s.ID]; ok {
			s.StartedAt, s.PeakKbps = prev.StartedAt, max(prev.PeakKbps, s.BandwidthKbps)
		} else {
			s.StartedAt = now
			if m.seeded {
				events = append(events, Event{Kind: EventStreamStart, Stream: *s})
			}
		}
		m.active[s.ID] = *s
		if s.Decision == Transcode {
			sample.Transcode++
		} else {
			sample.Direct++
		}
		sample.BandwidthKbps += s.BandwidthKbps
	}
	for id, s := range m.active {
		if !seen[id] {
			delete(m.active, id)
			last := s
			play := Play{Last: &last, User: s.User, Kind: s.Kind, Title: s.Title, Subtitle: s.Subtitle, Player: s.Player,
				Decision: s.Decision, Started: s.StartedAt, Ended: now, Progress: s.Progress, Local: s.Local, DurationMS: s.DurationMS, Reason: s.Reason}
			m.recent = append([]Play{play}, m.recent...)
			events = append(events, Event{Kind: EventStreamEnd, Play: play})
		}
	}
	if len(m.recent) > maxRecent {
		m.recent = m.recent[:maxRecent]
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].StartedAt < streams[j].StartedAt })
	m.streams, m.seeded = streams, true
	m.samples = append(m.samples, sample)
	if len(m.samples) > maxSamples {
		m.samples = m.samples[len(m.samples)-maxSamples:]
	}
	return events
}

func (m *Monitor) fail(now float64, err error) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	m.detail, m.checkedAt, m.latency = err.Error(), now, nil
	if m.status == StatusDown || m.failures <= m.retries {
		return nil
	}
	// Streams are unknown while the server is unreachable; forget them so recovery does not
	// announce every stream that was already playing.
	m.status, m.since = StatusDown, now
	m.streams, m.active, m.seeded = nil, map[string]Stream{}, false
	return []Event{{Kind: EventDown, Detail: m.detail}}
}

func (m *Monitor) refreshLibraries() {
	defer m.libsBusy.Store(false)
	libs, err := m.src.Libraries()
	if err != nil {
		return
	}
	sort.Slice(libs, func(i, j int) bool { return libs[i].Title < libs[j].Title })
	m.mu.Lock()
	m.libraries, m.libsAt = libs, float64(time.Now().UnixNano())/1e9
	m.mu.Unlock()
}

// Stop ends the stream with the given ID, showing reason on its player. It leaves the list at once;
// the next poll records it among the finished plays.
func (m *Monitor) Stop(id, reason string) (Stream, error) {
	m.mu.RLock()
	var target Stream
	found := false
	for _, s := range m.streams {
		if s.ID == id {
			target, found = s, true
			break
		}
	}
	m.mu.RUnlock()
	if !found {
		return Stream{}, ErrNoStream
	}
	if target.SessionID == "" {
		return Stream{}, errors.New("Plex gave this stream no session id, so it cannot be stopped from here")
	}
	if err := m.src.Terminate(target.SessionID, reason); err != nil {
		return Stream{}, err
	}
	m.mu.Lock()
	m.streams = slices.DeleteFunc(m.streams, func(s Stream) bool { return s.ID == id })
	m.mu.Unlock()
	return target, nil
}

// Transcodes is how many active streams are being transcoded.
func (m *Monitor) Transcodes() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, s := range m.streams {
		if s.Decision == Transcode {
			n++
		}
	}
	return n
}

// Failures is the current run of consecutive failed polls.
func (m *Monitor) Failures() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.failures
}

func (m *Monitor) Retries() int { return m.retries }

func (m *Monitor) URL() string { return m.src.URL() }

// Detail is the last poll's error, or "" after a successful poll.
func (m *Monitor) Detail() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.detail
}

// Snapshot is the full state for the Plex page. With summary set it leaves out the chart samples,
// recent plays and libraries, which the overview does not show.
func (m *Monitor) Snapshot(summary bool) Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Snapshot{
		URL: m.src.URL(), Status: m.status, Detail: m.detail, Since: m.since, CheckedAt: m.checkedAt, LatencyMS: m.latency,
		Server: m.server, Streams: append([]Stream{}, m.streams...), Libraries: []Library{}, LibrariesAt: m.libsAt,
		Samples: []Sample{}, Recent: []Play{},
	}
	if !summary {
		s.Libraries = append(s.Libraries, m.libraries...)
		s.Samples = downsample(m.samples, chartPoints)
		s.Recent = append(s.Recent, m.recent...)
	}
	for _, st := range m.streams {
		if st.Decision == Transcode {
			s.Transcode++
		} else {
			s.Direct++
		}
		s.BandwidthKbps += st.BandwidthKbps
		if !st.Local {
			s.WANKbps += st.BandwidthKbps
		}
	}
	return s
}

// downsample merges consecutive samples into n buckets, keeping each bucket's peak so short
// spikes in streams or bandwidth stay visible.
func downsample(samples []Sample, n int) []Sample {
	if len(samples) <= n {
		return append([]Sample{}, samples...)
	}
	out := make([]Sample, n)
	for k := range n {
		bucket := samples[k*len(samples)/n : (k+1)*len(samples)/n]
		b := bucket[0]
		for _, s := range bucket[1:] {
			b.Direct, b.Transcode, b.BandwidthKbps = max(b.Direct, s.Direct), max(b.Transcode, s.Transcode), max(b.BandwidthKbps, s.BandwidthKbps)
		}
		out[k] = b
	}
	return out
}

// Viewed is one entry of Plex's own watch history: something watched to the end, or marked watched.
type Viewed struct {
	At    float64 `json:"at"`
	User  string  `json:"user"`
	Kind  string  `json:"kind"` // movie, episode, track
	Title string  `json:"title"`
	Show  string  `json:"show,omitempty"` // an episode's show, a track's artist
}

// historySource is a Plex server that can say what was watched, as the HTTP Client can.
type historySource interface {
	History(since float64) ([]Viewed, error)
}

// History is Plex's own watch history since a time, newest first: what was watched even while
// Nightjar was not running. It is nil when the source keeps none.
func (m *Monitor) History(since float64) ([]Viewed, error) {
	if h, ok := m.src.(historySource); ok {
		return h.History(since)
	}
	return nil, nil
}

// Active is the streams playing now.
func (m *Monitor) Active() []Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.streams)
}

// Libraries is the libraries as last counted.
func (m *Monitor) Libraries() []Library {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.libraries)
}
