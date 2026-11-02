package updates

import (
	"fmt"
	"time"
)

// Window is the hours (local time) when automatic updates may start. Start == End means any time;
// Start > End runs over midnight (22–2).
type Window struct{ Start, End int }

func (w Window) Always() bool { return w.Start == w.End }

// Contains reports whether automatic updates may start at t.
func (w Window) Contains(t time.Time) bool {
	h := t.Hour()
	switch {
	case w.Always():
		return true
	case w.Start < w.End:
		return h >= w.Start && h < w.End
	}
	return h >= w.Start || h < w.End
}

// Opened is when the window containing t began.
func (w Window) Opened(t time.Time) time.Time {
	start := time.Date(t.Year(), t.Month(), t.Day(), w.Start, 0, 0, 0, t.Location())
	if start.After(t) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

func (w Window) String() string {
	if w.Always() {
		return "any time"
	}
	return fmt.Sprintf("%02d:00–%02d:00", w.Start, w.End)
}
