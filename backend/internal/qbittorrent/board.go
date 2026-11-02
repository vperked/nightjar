package qbittorrent

import (
	"slices"
	"sync"
)

// maxSamples is an hour of speed history at the loop's default pace (a fetch every 10s).
const maxSamples = 360

// Entry is a torrent as the dashboard shows it, with what the monitor knows about its stalls.
type Entry struct {
	Torrent
	StalledFor float64 `json:"stalled_for,omitempty"` // seconds the download has been stalled
	Recoveries int     `json:"recoveries,omitempty"`  // reannounce/resume attempts so far
}

// Sample is the global speed at one fetch.
type Sample struct {
	TS float64 `json:"ts"`
	DL int64   `json:"dl"`
	UP int64   `json:"up"`
}

// View is the latest state for the dashboard.
type View struct {
	Connected bool      `json:"connected"`
	Error     string    `json:"error,omitempty"`
	Version   string    `json:"version,omitempty"`
	URL       string    `json:"url"`
	Updated   float64   `json:"updated"`
	Transfer  *Transfer `json:"transfer"`
	Torrents  []Entry   `json:"torrents"`
	History   []Sample  `json:"history"`
}

// Board holds what the monitoring loop last saw, so the dashboard never waits on qBittorrent
// and the client is only used from the loop.
type Board struct {
	mu   sync.Mutex
	view View
}

// Publish records a fetch. When it failed, the last torrent list stays, marked as disconnected.
func (b *Board) Publish(now float64, url, version, failure string, torrents []Entry, transfer *Transfer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := &b.view
	v.URL, v.Updated, v.Connected, v.Error = url, now, failure == "", failure
	if version != "" {
		v.Version = version
	}
	if failure != "" {
		return
	}
	v.Torrents = torrents
	v.Transfer = transfer
	if transfer != nil {
		v.History = append(v.History, Sample{TS: now, DL: transfer.DLSpeed, UP: transfer.UPSpeed})
		if over := len(v.History) - maxSamples; over > 0 {
			v.History = slices.Delete(v.History, 0, over)
		}
	}
}

// View returns a copy of the latest state.
func (b *Board) View() View {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.view
	v.Torrents = slices.Clone(v.Torrents)
	v.History = slices.Clone(v.History)
	if v.Torrents == nil {
		v.Torrents = []Entry{}
	}
	if v.History == nil {
		v.History = []Sample{}
	}
	return v
}

// Refresh replaces the torrent list between the loop's fetches (after a dashboard action), keeping
// what the loop knows about stalls. Speeds and history wait for the loop.
func (b *Board) Refresh(now float64, torrents []Torrent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	known := map[string]Entry{}
	for _, e := range b.view.Torrents {
		known[e.Hash] = e
	}
	entries := make([]Entry, 0, len(torrents))
	for _, t := range torrents {
		e := Entry{Torrent: t}
		if k, ok := known[t.Hash]; ok && IsDownloadStalled(t) {
			e.StalledFor, e.Recoveries = k.StalledFor, k.Recoveries
		}
		entries = append(entries, e)
	}
	b.view.Torrents, b.view.Updated, b.view.Connected, b.view.Error = entries, now, true, ""
}
