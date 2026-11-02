package logx

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// Recent keeps the last few thousand log entries in memory for the dashboard's log viewer, so it
// needs neither the log file nor journald access.
type Recent struct {
	mu      sync.Mutex
	entries []Entry // a ring: next is where the next entry goes once it is full
	next    int
	seq     uint64
	level   slog.Leveler
}

// RecentSize is how many entries the viewer can go back.
const RecentSize = 3000

// NewRecent keeps entries from level up; Setup makes the one the dashboard reads.
func NewRecent(level slog.Leveler) *Recent {
	return &Recent{entries: make([]Entry, 0, RecentSize), level: level}
}

func (r *Recent) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.Seq = r.seq
	if len(r.entries) < RecentSize {
		r.entries = append(r.entries, e)
		return
	}
	r.entries[r.next] = e
	r.next = (r.next + 1) % RecentSize
}

// Query filters the recent entries. Zero values match everything.
type Query struct {
	After     uint64 // only entries newer than this sequence number, for following the log
	MinLevel  string // debug, info, warn or error
	Component string
	Search    string
	Limit     int // the newest this many; 500 when zero
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Entries returns the matching entries, oldest first, and the newest sequence number overall.
func (r *Recent) Entries(q Query) ([]Entry, uint64) {
	r.mu.Lock()
	all := append(slices.Clone(r.entries[r.next:]), r.entries[:r.next]...)
	latest := r.seq
	r.mu.Unlock()

	limit := q.Limit
	if limit <= 0 || limit > RecentSize {
		limit = 500
	}
	min := levelRank[strings.ToLower(q.MinLevel)]
	search := strings.ToLower(q.Search)
	var out []Entry
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		e := all[i]
		if e.Seq <= q.After {
			break
		}
		if levelRank[e.Level] < min || (q.Component != "" && e.Component != q.Component) || (search != "" && !e.matches(search)) {
			continue
		}
		out = append(out, e)
	}
	slices.Reverse(out)
	return out, latest
}

func (e Entry) matches(search string) bool {
	if strings.Contains(strings.ToLower(e.Message), search) || strings.Contains(e.Component, search) {
		return true
	}
	for _, kv := range e.Attrs {
		if strings.Contains(strings.ToLower(kv.Key+"="+kv.Value), search) {
			return true
		}
	}
	return false
}

// Components lists the components seen, sorted, for the viewer's filter.
func (r *Recent) Components() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, e := range r.entries {
		if e.Component != "" {
			seen[e.Component] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Handler feeds r from slog.
func (r *Recent) Handler() slog.Handler { return recentHandler{r: r} }

// recentHandler feeds a Recent from slog.
type recentHandler struct {
	r     *Recent
	attrs []slog.Attr
	group string
}

func (h recentHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.r.level.Level() }
func (h recentHandler) Handle(_ context.Context, rec slog.Record) error {
	h.r.add(entryOf(rec, h.attrs, h.group))
	return nil
}
func (h recentHandler) WithAttrs(a []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr{}, h.attrs...), qualify(h.group, a)...)
	return h
}
func (h recentHandler) WithGroup(g string) slog.Handler {
	h.group = joinKey(h.group, g)
	return h
}
