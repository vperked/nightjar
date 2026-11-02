package logx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Style is how a destination wants its lines.
type Style struct {
	// Time is "" (none: journald stamps lines itself), "clock" (15:04:05.000) or "full".
	Time string
	// Color uses ANSI colours, for a terminal.
	Color bool
	// Priority prefixes each line with its syslog priority (<3> error … <7> debug), which journald
	// reads to file the line at the right level: journalctl -p warning then shows only problems,
	// and journalctl colours them.
	Priority bool
	// JSON writes one JSON object per line instead, for log shippers.
	JSON bool
}

// lineHandler writes one line per record:
//
//	04:02:56.123  WARN   accounts     No admin account yet  setup_code=F3508-1F63B
//
// The level and component are padded so messages line up, attributes follow as key=value, and
// secrets are masked before anything is written.
type lineHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	style Style
	level slog.Leveler
	attrs []slog.Attr // from WithAttrs, the component among them
	group string
}

// NewHandler writes records to w in style, from level up.
func NewHandler(w io.Writer, style Style, level slog.Leveler) slog.Handler {
	return &lineHandler{mu: &sync.Mutex{}, w: w, style: style, level: level}
}

func (h *lineHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *lineHandler) WithAttrs(a []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), qualify(h.group, a)...)
	return &c
}

func (h *lineHandler) WithGroup(g string) slog.Handler {
	c := *h
	c.group = joinKey(h.group, g)
	return &c
}

func qualify(group string, attrs []slog.Attr) []slog.Attr {
	if group == "" {
		return attrs
	}
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = slog.Attr{Key: joinKey(group, a.Key), Value: a.Value}
	}
	return out
}

func joinKey(group, key string) string {
	if group == "" {
		return key
	}
	return group + "." + key
}

func (h *lineHandler) Handle(_ context.Context, r slog.Record) error {
	e := entryOf(r, h.attrs, h.group)
	var line string
	if h.style.JSON {
		line = e.json()
	} else {
		line = e.line(h.style)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line+"\n")
	return err
}

// ---------- entries ----------

// Entry is one log record, flattened: what the dashboard's log viewer shows.
type Entry struct {
	Seq       uint64 `json:"seq"`
	Time      int64  `json:"time"` // unix milliseconds
	Level     string `json:"level"`
	Component string `json:"component,omitempty"`
	Message   string `json:"message"`
	Attrs     []KV   `json:"attrs,omitempty"`
}

// KV is one attribute, its value already formatted and redacted.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func entryOf(r slog.Record, attrs []slog.Attr, group string) Entry {
	e := Entry{Time: r.Time.UnixMilli(), Level: levelName(r.Level), Message: Redact(r.Message)}
	var add func(prefix string, a slog.Attr)
	add = func(prefix string, a slog.Attr) {
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			return
		}
		key := joinKey(prefix, a.Key)
		if a.Value.Kind() == slog.KindGroup {
			for _, g := range a.Value.Group() {
				add(key, g)
			}
			return
		}
		if key == "component" {
			e.Component = strings.ToLower(a.Value.String())
			return
		}
		value := Redact(formatValue(a.Value))
		if secretKey.MatchString(key) && value != "" {
			value = "***"
		}
		e.Attrs = append(e.Attrs, KV{Key: key, Value: value})
	}
	for _, a := range attrs {
		add("", a)
	}
	r.Attrs(func(a slog.Attr) bool {
		add(group, a)
		return true
	})
	return e
}

func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindDuration:
		return v.Duration().Round(time.Millisecond).String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'f', -1, 64)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return err.Error()
		}
		if s, ok := v.Any().(fmt.Stringer); ok {
			return s.String()
		}
		return fmt.Sprint(v.Any())
	}
	return v.String()
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
	gray   = "\x1b[90m"
	purple = "\x1b[35m"
)

var levelLook = map[string]struct {
	label, color string
	priority     int
}{
	"debug": {"DEBUG", gray, 7},
	"info":  {"INFO ", cyan, 6},
	"warn":  {"WARN ", yellow, 4},
	"error": {"ERROR", bold + red, 3},
}

// componentWidth lines messages up; longer names push their line along.
const componentWidth = 11

func (e Entry) line(s Style) string {
	look := levelLook[e.Level]
	paint := func(color, text string) string {
		if !s.Color || color == "" {
			return text
		}
		return color + text + reset
	}
	var b strings.Builder
	if s.Priority {
		fmt.Fprintf(&b, "<%d>", look.priority)
	}
	switch t := time.UnixMilli(e.Time); s.Time {
	case "clock":
		b.WriteString(paint(gray, t.Format("15:04:05.000")) + "  ")
	case "full":
		b.WriteString(paint(gray, t.Format("2006-01-02 15:04:05.000")) + "  ")
	}
	b.WriteString(paint(look.color, look.label) + "  ")
	b.WriteString(paint(purple, fmt.Sprintf("%-*s", componentWidth, e.Component)) + " ")
	msg := e.Message
	if e.Level == "error" {
		msg = paint(bold, msg)
	}
	b.WriteString(msg)
	for _, kv := range e.Attrs {
		b.WriteString("  " + paint(dim, kv.Key+"=") + quote(kv.Value))
	}
	return b.String()
}

// quote leaves simple values bare and quotes the rest, so a line splits cleanly on spaces.
func quote(v string) string {
	if v == "" {
		return `""`
	}
	for _, r := range v {
		if unicode.IsSpace(r) || r == '"' || r == '=' || !unicode.IsPrint(r) {
			return strconv.Quote(v)
		}
	}
	return v
}

func (e Entry) json() string {
	m := map[string]any{"time": time.UnixMilli(e.Time).UTC().Format(time.RFC3339Nano), "level": e.Level, "msg": e.Message}
	if e.Component != "" {
		m["component"] = e.Component
	}
	for _, kv := range e.Attrs {
		if _, taken := m[kv.Key]; !taken {
			m[kv.Key] = kv.Value
		}
	}
	out, _ := json.Marshal(m)
	return string(out)
}
