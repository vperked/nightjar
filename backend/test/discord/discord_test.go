package discord_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"servermonitor/internal/collect"
	"servermonitor/internal/discord"
	"servermonitor/internal/metrics"
)

// fakeDiscord answers every post with status and keeps the last payload.
type fakeDiscord struct {
	*httptest.Server
	status atomic.Int32
	calls  atomic.Int32
	last   atomic.Value
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{}
	f.status.Store(http.StatusNoContent)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.last.Store(body)
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.Close)
	return f
}

func newTestWebhook(t *testing.T, url string) (*discord.Webhook, string) {
	logPath := filepath.Join(t.TempDir(), "alerts.log")
	w, err := discord.NewWebhook(url, logPath)
	if err != nil {
		t.Fatal(err)
	}
	return w, logPath
}

func TestSendEmbedDelivers(t *testing.T) {
	d := newFakeDiscord(t)
	w, logPath := newTestWebhook(t, d.URL)
	w.Origin = discord.Origin{Host: "nas", DashboardURL: "https://monitor.example.com"}
	if !w.SendEmbed("CPU", "high", discord.Critical, []discord.Field{{Name: "a", Value: "b"}}, "Disk") {
		t.Fatal("send failed")
	}
	embed := d.last.Load().(map[string]any)["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != "🔴 CPU" || embed["color"].(float64) != float64(discord.Critical.Color()) || embed["timestamp"] == "" {
		t.Fatalf("severity should colour the embed and mark the title: %v", embed)
	}
	if embed["footer"].(map[string]any)["text"] != "servermonitor · nas · Disk" || embed["url"] != "https://monitor.example.com" {
		t.Fatalf("footer and link: %v", embed)
	}
	if w.LastError() != "" {
		t.Fatalf("last error after success: %q", w.LastError())
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("successful sends must not write the fallback log")
	}
	if _, err := discord.NewWebhook("", ""); err == nil {
		t.Fatal("empty URL should be rejected")
	}
}

func TestFailedSendFallsBackToLog(t *testing.T) {
	d := newFakeDiscord(t)
	w, logPath := newTestWebhook(t, d.URL)
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		d.status.Store(int32(status))
		if w.SendEmbed("Disk", "full", discord.Warning, nil, "") {
			t.Fatalf("HTTP %d should report failure", status)
		}
	}
	data, _ := os.ReadFile(logPath)
	if strings.Count(string(data), "Disk: full") != 2 {
		t.Fatalf("fallback log %q", data)
	}
}

func TestBreakerStopsCallingAFailingWebhook(t *testing.T) {
	d := newFakeDiscord(t)
	d.status.Store(http.StatusInternalServerError)
	w, logPath := newTestWebhook(t, d.URL)
	for range 8 {
		w.SendMessage("down")
	}
	if got := d.calls.Load(); got != 5 {
		t.Fatalf("breaker should open after 5 failures, server saw %d calls", got)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Count(string(data), "down") != 8 {
		t.Fatal("every dropped message belongs in the fallback log")
	}
}

func TestRateLimiterDropsBeyondCap(t *testing.T) {
	d := newFakeDiscord(t)
	w, _ := newTestWebhook(t, d.URL)
	sent := 0
	for range 35 {
		if w.SendMessage("x") {
			sent++
		}
	}
	if sent != 30 || d.calls.Load() != 30 {
		t.Fatalf("sent %d, server saw %d; want 30", sent, d.calls.Load())
	}
}

func TestBreakerHalfOpenRecovery(t *testing.T) {
	b := discord.NewCircuitBreaker(2, 10*time.Millisecond, 2)
	fail := errors.New("boom")
	b.Call(func() error { return fail })
	b.Call(func() error { return fail })
	if !b.IsOpen() || !errors.Is(b.Call(func() error { return nil }), discord.ErrBreakerOpen) {
		t.Fatal("breaker should be open")
	}
	time.Sleep(15 * time.Millisecond)
	if b.IsOpen() {
		t.Fatal("breaker should allow a probe after the timeout")
	}
	b.Call(func() error { return fail })
	if !b.IsOpen() {
		t.Fatal("a failing probe re-opens the breaker")
	}
	time.Sleep(15 * time.Millisecond)
	b.Call(func() error { return nil })
	b.Call(func() error { return nil })
	// Closed again: one failure is below the threshold of two.
	b.Call(func() error { return fail })
	if b.IsOpen() {
		t.Fatal("two good probes should close the breaker and reset its failure count")
	}
}

func TestBuildEmbedLimits(t *testing.T) {
	fields := make([]discord.Field, 30)
	for i := range fields {
		fields[i] = discord.Field{Name: strings.Repeat("n", 300), Value: strings.Repeat("é", 2000)}
	}
	e := discord.BuildEmbed(strings.Repeat("t", 300), strings.Repeat("d", 5000), 1, fields, "")
	if len(e.Title) != 256 || len(e.Description) != 4096 || len(e.Fields) != 25 || e.Footer != nil {
		t.Fatalf("limits not applied: title=%d desc=%d fields=%d", len(e.Title), len(e.Description), len(e.Fields))
	}
	if n := len([]rune(e.Fields[0].Value)); n != 1024 {
		t.Fatalf("field value should be cut by runes, got %d", n)
	}
}

type capture struct {
	title  string
	fields []discord.Field
}

func (c *capture) SendEmbed(title, _ string, _ discord.Severity, fields []discord.Field, _ string) bool {
	c.title, c.fields = title, fields
	return true
}

func (c *capture) field(name string) string {
	for _, f := range c.fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

func TestEmbedBuilders(t *testing.T) {
	c := &capture{}
	discord.CPUAlert(c, 93.25, 80, []float64{99, 10})
	if c.title != "CPU Usage Alert" || c.field("Overall CPU Usage") != "93.2%" {
		t.Fatalf("cpu: %s %v", c.title, c.fields)
	}
	discord.MemoryAlert(c, 91, 80, collect.Memory{Total: 8 << 30, Used: 7 << 30, Percent: 91}, collect.Memory{})
	if c.title != "Memory Usage Alert" {
		t.Fatalf("memory: %s", c.title)
	}
	discord.ProcessAlert(c, collect.Process{PID: 42, Name: "worker", CPUPercent: 99}, "cpu", 50)
	if !strings.Contains(c.title, "Process") || !strings.Contains(fieldsText(c.fields), "worker") {
		t.Fatalf("process: %s %v", c.title, c.fields)
	}
	discord.LogCleanupAlert(c, []discord.CleanedLog{{Path: "/var/log/syslog", OriginalSize: "2 GB", CurrentSize: "0 B"}}, "100 MB")
	if !strings.Contains(fieldsText(c.fields), "/var/log/syslog") {
		t.Fatalf("log cleanup: %v", c.fields)
	}
	discord.StalledTorrent(c, discord.TorrentInfo{Name: "linux.iso", SavePath: "/data"}, 600, []string{"reannounce", "resume"})
	if !strings.Contains(c.title, "Stalled") || !strings.Contains(fieldsText(c.fields), "linux.iso") {
		t.Fatalf("stalled: %s %v", c.title, c.fields)
	}
	discord.MetricsReport(c, metrics.New().Summary())
	if c.title == "" {
		t.Fatal("metrics report sent nothing")
	}
}

func fieldsText(fields []discord.Field) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Name + "=" + f.Value + "\n")
	}
	return b.String()
}
