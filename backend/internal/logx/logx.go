// Package logx configures slog: one clean line per record on stderr (styled for journald, a terminal
// or a pipe), a size-rotated log file, and an in-memory log for the dashboard, with secrets masked
// everywhere.
package logx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
)

var secretPatterns = []*regexp.Regexp{
	// Discord webhooks (and ptb., canary., versioned API paths), Telegram bot tokens and Slack
	// webhooks carry their secret in the URL.
	regexp.MustCompile(`(https?://(?:[a-z]+\.)?(?:discord|discordapp)\.com/api/(?:v\d+/)?webhooks/\d+/)[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`(/bot\d+:)[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`(hooks\.slack\.com/(?:services|workflows|triggers)/)[^\s"'<>]+`),
	regexp.MustCompile(`(?i)((?:password|token|secret|api[_-]?key)["']?\s*[:=]\s*["']?)[^\s"',]+`),
}

// secretKey names attributes whose whole value is a secret.
var secretKey = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|webhook)$`)

// Redact masks webhook URLs, bot tokens and key=value secrets.
func Redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "${1}***")
	}
	return s
}

// fanout sends each record to several handlers.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (f fanout) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(a)
	}
	return out
}

func (f fanout) WithGroup(g string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(g)
	}
	return out
}

func parseLevel(s string) slog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARNING", "WARN":
		return slog.LevelWarn
	case "ERROR", "CRITICAL":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// level is shared by every destination, so SetLevel changes them all at once.
var level = new(slog.LevelVar)

// SetLevel changes the log level while running (LOG_LEVEL is a live setting).
func SetLevel(name string) { level.Set(parseLevel(name)) }

// Options configure Setup.
type Options struct {
	Level    string
	File     string // "" for none
	MaxBytes int64
	Backups  int
	JSON     bool // one JSON object per line, everywhere
}

var recent = NewRecent(level)

// RecentEntries is the in-memory log the dashboard shows.
func RecentEntries() *Recent { return recent }

// Setup installs the default logger: stderr styled for where it goes (journald, a terminal, or a
// pipe), the rotating file, and the in-memory log. A file that cannot be opened is reported and skipped.
func Setup(o Options) {
	SetLevel(o.Level)
	stderr := consoleStyle()
	stderr.JSON = o.JSON
	handlers := fanout{NewHandler(os.Stderr, stderr, level), recentHandler{r: recent}}
	if o.File != "" {
		if rf, err := OpenRotating(o.File, o.MaxBytes, o.Backups); err == nil {
			handlers = append(handlers, NewHandler(rf, Style{Time: "full", JSON: o.JSON}, level))
		} else {
			fmt.Fprintf(os.Stderr, "log file %s unavailable: %v\n", o.File, err)
		}
	}
	h := slog.Handler(handlers)
	base.Store(&h)
	slog.SetDefault(slog.New(handlers))
}

// consoleStyle picks stderr's style: under systemd, journald adds the time and reads the priority;
// in a terminal, colour and a short clock; anywhere else (a pipe, Docker), plain lines with the date.
func consoleStyle() Style {
	if os.Getenv("JOURNAL_STREAM") != "" {
		return Style{Priority: true}
	}
	if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" {
		return Style{Time: "clock", Color: true}
	}
	return Style{Time: "full"}
}

// base is the handler every Component logger delegates to; Setup swaps it.
var base atomic.Pointer[slog.Handler]

func init() {
	h := slog.Handler(fanout{NewHandler(os.Stderr, consoleStyle(), level), recentHandler{r: recent}})
	base.Store(&h)
}

type dynamic struct{ attrs []slog.Attr }

func (d dynamic) handler() slog.Handler {
	h := *base.Load()
	if len(d.attrs) > 0 {
		h = h.WithAttrs(d.attrs)
	}
	return h
}

func (d dynamic) Enabled(ctx context.Context, l slog.Level) bool {
	return (*base.Load()).Enabled(ctx, l)
}
func (d dynamic) Handle(ctx context.Context, r slog.Record) error { return d.handler().Handle(ctx, r) }
func (d dynamic) WithAttrs(a []slog.Attr) slog.Handler {
	return dynamic{attrs: append(append([]slog.Attr{}, d.attrs...), a...)}
}
func (d dynamic) WithGroup(g string) slog.Handler { return d.handler().WithGroup(g) }

// Component returns a logger tagged component=name that follows later Setup calls,
// so it is safe to create in a package-level var.
func Component(name string) *slog.Logger {
	return slog.New(dynamic{attrs: []slog.Attr{slog.String("component", name)}})
}
