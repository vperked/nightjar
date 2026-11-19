package updates

// While an update runs, what it does is kept as it happens, so the dashboard can show it: each
// command, what it prints, and the steps between. The end of it stays with the run afterwards.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Live is an update under way, as far as it has got.
type Live struct {
	Started float64  `json:"started"`
	Lines   []string `json:"lines"`
	// Now is this machine's clock as it answered, which Started is on: a browser on another clock
	// counts how long it has run from the two.
	Now float64 `json:"now"`
}

// liveLines is how many lines an update keeps while it runs; a run keeps the last keptLines.
const (
	liveLines = 400
	keptLines = 200
)

// liveLog collects an update's output. Commands write to it from two streams at once.
type liveLog struct {
	mu      sync.Mutex
	started time.Time
	lines   []string
	partial string
}

func newLiveLog() *liveLog { return &liveLog{started: time.Now()} }

// Write takes a command's output, line by line. A carriage return (a progress bar redrawing)
// replaces the line instead of adding one.
func (l *liveLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.partial + string(p)
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimRight(s[:i], " \t")
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			s = s[i+2:]
			l.addLocked(line)
			continue
		}
		if s[i] == '\r' {
			l.partial = ""
			s = s[i+1:]
			// The next text redraws this line; keep only what follows.
			continue
		}
		s = s[i+1:]
		l.addLocked(line)
	}
	l.partial = s
	return len(p), nil
}

// note adds a line of its own: a step, or a command about to run.
func (l *liveLog) note(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partial != "" {
		l.addLocked(l.partial)
		l.partial = ""
	}
	l.addLocked(line)
}

func (l *liveLog) addLocked(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	l.lines = append(l.lines, line)
	if over := len(l.lines) - liveLines; over > 0 {
		l.lines = slices.Delete(l.lines, 0, over)
	}
}

func (l *liveLog) view() *Live {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := slices.Clone(l.lines)
	if l.partial != "" {
		lines = append(lines, l.partial)
	}
	if lines == nil {
		lines = []string{}
	}
	return &Live{Started: float64(l.started.Unix()), Lines: lines, Now: float64(time.Now().Unix())}
}

// tail is the last lines, to keep with the run.
func (l *liveLog) tail() []string {
	v := l.view().Lines
	return slices.Clone(v[max(0, len(v)-keptLines):])
}

type liveKey struct{}

func withLive(ctx context.Context, l *liveLog) context.Context {
	return context.WithValue(ctx, liveKey{}, l)
}

func liveFrom(ctx context.Context) *liveLog {
	l, _ := ctx.Value(liveKey{}).(*liveLog)
	return l
}

// noteStep adds a line to the update under way in ctx, if there is one.
func noteStep(ctx context.Context, line string) {
	if l := liveFrom(ctx); l != nil {
		l.note(line)
	}
}
