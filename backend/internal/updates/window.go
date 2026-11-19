package updates

import (
	"fmt"
	"time"
)

// Window is the hours (local time) when automatic updates may start. Start == End means any time;
// Start > End runs over midnight (22–2). One made with MinuteWindow counts minutes after midnight
// instead, for agents, whose window is set to the minute.
type Window struct {
	Start, End int
	minutes    bool
}

// MinuteWindow is a window from start to end, in minutes after local midnight (240 is 04:00).
func MinuteWindow(start, end int) Window { return Window{Start: start, End: end, minutes: true} }

func (w Window) Always() bool { return w.Start == w.End }

// at is where t falls in the day, in the window's unit.
func (w Window) at(t time.Time) int {
	if w.minutes {
		return t.Hour()*60 + t.Minute()
	}
	return t.Hour()
}

// Contains reports whether automatic updates may start at t.
func (w Window) Contains(t time.Time) bool {
	h := w.at(t)
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
	hour, minute := w.Start, 0
	if w.minutes {
		hour, minute = w.Start/60, w.Start%60
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, t.Location())
	if start.After(t) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

func (w Window) String() string {
	switch {
	case w.Always():
		return "any time"
	case w.minutes:
		return fmt.Sprintf("%02d:%02d–%02d:%02d", w.Start/60, w.Start%60, w.End/60, w.End%60)
	}
	return fmt.Sprintf("%02d:00–%02d:00", w.Start, w.End)
}
