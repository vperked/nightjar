package logx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"nightjar/internal/logx"
)

func line(t *testing.T, style logx.Style, log func(*slog.Logger)) string {
	t.Helper()
	var b bytes.Buffer
	log(slog.New(logx.NewHandler(&b, style, slog.LevelDebug)).With("component", "Web"))
	return strings.TrimRight(b.String(), "\n")
}

func TestLinesAreAlignedAndReadable(t *testing.T) {
	got := line(t, logx.Style{}, func(l *slog.Logger) {
		l.Warn("Failed sign-in", "ip", "203.0.113.9", "reason", "wrong password", "took", 1500*time.Millisecond, "err", errors.New("bad"))
	})
	want := `WARN   web         Failed sign-in  ip=203.0.113.9  reason="wrong password"  took=1.5s  err=bad`
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestJournaldGetsPrioritiesAndNoTime(t *testing.T) {
	for level, prefix := range map[slog.Level]string{slog.LevelError: "<3>ERROR", slog.LevelWarn: "<4>WARN", slog.LevelInfo: "<6>INFO", slog.LevelDebug: "<7>DEBUG"} {
		got := line(t, logx.Style{Priority: true}, func(l *slog.Logger) { l.Log(t.Context(), level, "x") })
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("%v: got %q, want prefix %q", level, got, prefix)
		}
	}
}

func TestTerminalLinesAreColouredWithAClock(t *testing.T) {
	got := line(t, logx.Style{Time: "clock", Color: true}, func(l *slog.Logger) { l.Error("Boom") })
	if !strings.Contains(got, "\x1b[") || !strings.Contains(got, "Boom") || len(strings.SplitN(got, ":", 3)) < 3 {
		t.Fatalf("expected colour and a clock: %q", got)
	}
}

func TestSecretsNeverReachTheLog(t *testing.T) {
	got := line(t, logx.Style{}, func(l *slog.Logger) {
		l.Info("Connected", "token", "abc123", "api_key", "k", "url", "https://discord.com/api/webhooks/1/SECRET", "detail", "password=hunter2")
	})
	for _, leak := range []string{"abc123", "=k ", "SECRET", "hunter2"} {
		if strings.Contains(got+" ", leak) {
			t.Fatalf("%q leaked: %s", leak, got)
		}
	}
}

func TestJSONLines(t *testing.T) {
	got := line(t, logx.Style{JSON: true}, func(l *slog.Logger) { l.WithGroup("qb").Info("Added", "count", 2) })
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["level"] != "info" || m["msg"] != "Added" || m["component"] != "web" || m["qb.count"] != "2" {
		t.Fatalf("json: %v", m)
	}
}

func TestRecentKeepsTheTailForTheDashboard(t *testing.T) {
	r := logx.NewRecent(slog.LevelInfo)
	l := slog.New(r.Handler())
	l.Debug("hidden")
	l.With("component", "Disk").Info("Cleanup performed", "freed", "2GB")
	l.With("component", "Web").Warn("Failed sign-in", "ip", "203.0.113.9")
	l.With("component", "Web").Error("Listener died")

	all, latest := r.Entries(logx.Query{})
	if len(all) != 3 || latest != 3 || all[0].Message != "Cleanup performed" || all[0].Attrs[0] != (logx.KV{Key: "freed", Value: "2GB"}) {
		t.Fatalf("debug is below the level; the rest oldest first: %+v", all)
	}
	if got, _ := r.Entries(logx.Query{MinLevel: "warn"}); len(got) != 2 {
		t.Fatalf("warnings and up: %+v", got)
	}
	if got, _ := r.Entries(logx.Query{Component: "web", Search: "203.0.113"}); len(got) != 1 || got[0].Level != "warn" {
		t.Fatalf("component and search: %+v", got)
	}
	if got, _ := r.Entries(logx.Query{After: 2}); len(got) != 1 || got[0].Message != "Listener died" {
		t.Fatalf("following: %+v", got)
	}
	if c := r.Components(); strings.Join(c, ",") != "disk,web" {
		t.Fatalf("components: %v", c)
	}

	for i := range logx.RecentSize + 10 {
		l.Info(fmt.Sprint("line ", i))
	}
	got, _ := r.Entries(logx.Query{Limit: logx.RecentSize})
	if len(got) != logx.RecentSize || got[len(got)-1].Message != fmt.Sprint("line ", logx.RecentSize+9) {
		t.Fatalf("the buffer keeps the newest %d, got %d ending %q", logx.RecentSize, len(got), got[len(got)-1].Message)
	}
}

func TestURLsThatAreSecretsAreMasked(t *testing.T) {
	for _, s := range []string{
		`Post "https://api.telegram.org/bot123456789:AAHsecretTOKEN/sendMessage": dial tcp: i/o timeout`,
		`Post "https://hooks.slack.com/services/T000/B000/secretTOKEN": context deadline exceeded`,
		`Post "https://ptb.discord.com/api/v10/webhooks/123/secretTOKEN?wait=true": EOF`,
		`Post "https://discord.com/api/webhooks/123/secretTOKEN": EOF`,
	} {
		if got := logx.Redact(s); strings.Contains(got, "secretTOKEN") || !strings.Contains(got, "***") {
			t.Errorf("not masked: %s", got)
		}
	}
}
