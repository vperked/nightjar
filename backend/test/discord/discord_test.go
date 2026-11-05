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

	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/metrics"
)

// fakeDiscord answers every post with status and keeps the last payload.
type fakeDiscord struct {
	*httptest.Server
	status atomic.Int32
	calls  atomic.Int32
	last   atomic.Value
	query  atomic.Value
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{}
	f.status.Store(http.StatusNoContent)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.last.Store(body)
		f.query.Store(r.URL.RawQuery)
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
	w.RetryWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return w, logPath
}

// An alert sent while the network is not up yet, or while Discord fails for a moment, gets
// through on a later try; a refusal (4xx) is not tried again.
func TestAFailedDeliveryIsTriedAgain(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	w, _ := newTestWebhook(t, srv.URL)
	if !w.SendEmbed("CPU", "high", discord.Critical, nil, "") || calls.Load() != 3 {
		t.Fatalf("5xx twice, then delivered: %d calls, %q", calls.Load(), w.LastError())
	}

	// Nothing listening yet: the connection is refused at once, so it is tried again, and still fails.
	ln := httptest.NewServer(http.NotFoundHandler())
	gone := ln.URL
	ln.Close()
	w2, _ := newTestWebhook(t, gone)
	start := time.Now()
	if w2.SendEmbed("CPU", "high", discord.Critical, nil, "") || time.Since(start) > 5*time.Second {
		t.Fatalf("refused: should fail, and fast")
	}

	var refused atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refused.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	w3, _ := newTestWebhook(t, bad.URL)
	if w3.SendEmbed("CPU", "high", discord.Critical, nil, "") || refused.Load() != 1 {
		t.Fatalf("a 401 is not tried again: %d calls", refused.Load())
	}
}

func TestSendEmbedDelivers(t *testing.T) {
	d := newFakeDiscord(t)
	w, logPath := newTestWebhook(t, d.URL)
	w.Origin = discord.Origin{Host: "nas", DashboardURL: "https://monitor.example.com"}
	if !w.SendEmbed("CPU", "high", discord.Critical, []discord.Field{{Name: "a", Value: "b"}, {Name: "empty", Value: " "}, {Name: "unknown", Value: "n/a"}}, "Disk") {
		t.Fatal("send failed")
	}
	// A Components V2 message: one container in the severity's colour, with the title as a heading,
	// the details without the empty fields, the source in small print and a link to the dashboard.
	msg := d.last.Load().(map[string]any)
	if msg["flags"].(float64) != discord.FlagComponentsV2 || msg["embeds"] != nil || d.query.Load() != "with_components=true" {
		t.Fatalf("not a components message: %v ?%v", msg, d.query.Load())
	}
	box := msg["components"].([]any)[0].(map[string]any)
	if box["type"].(float64) != 17 || box["accent_color"].(float64) != float64(discord.Critical.Color()) {
		t.Fatalf("container: %v", box)
	}
	parts := box["components"].([]any)
	text := func(i int) string { return parts[i].(map[string]any)["content"].(string) }
	if len(parts) != 5 || text(0) != "### CPU\nhigh" || text(2) != "**a**\nb" || !strings.HasPrefix(text(3), "-# nas · Storage · <t:") {
		t.Fatalf("parts: %v", parts)
	}
	button := parts[4].(map[string]any)["components"].([]any)[0].(map[string]any)
	if button["url"] != "https://monitor.example.com" || button["style"].(float64) != 5 {
		t.Fatalf("button: %v", button)
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

func TestComponentsLayout(t *testing.T) {
	m := discord.Message("plex is down", "Connect failed", discord.Warning, []discord.Field{
		{Name: "Target", Value: "`10.0.0.2:32400`", Inline: true}, {Name: "Uptime today", Value: "98%", Inline: true},
		{Name: "Busiest", Value: "plex · 61%\nsonarr · 4%", Inline: true}, {Name: "Likely cause", Value: "Nothing listens there."},
	}, "Uptime", discord.Origin{Host: "nas"}, time.Unix(1700000000, 0))
	parts := m["components"].([]discord.Component)[0]["components"].([]discord.Component)
	want := "**Target:** `10.0.0.2:32400`\n**Uptime today:** 98%\n\n**Busiest**\nplex · 61%\nsonarr · 4%\n\n**Likely cause**\nNothing listens there."
	if len(parts) != 4 || parts[2]["content"] != want || parts[3]["content"] != "-# nas · Uptime · <t:1700000000:R>" {
		t.Fatalf("layout: %#v", parts)
	}
	// Without a dashboard address there is no button; the text stays inside Discord's limit.
	long := discord.Message("big", strings.Repeat("d", 3000), discord.Info, []discord.Field{{Name: "x", Value: strings.Repeat("v", 3000)}}, "", discord.Origin{}, time.Now())
	total := 0
	for _, p := range long["components"].([]discord.Component)[0]["components"].([]discord.Component) {
		if p["type"] == 1 {
			t.Fatal("a button without a dashboard address")
		}
		if c, ok := p["content"].(string); ok {
			total += len([]rune(c))
		}
	}
	if total > 4000 {
		t.Fatalf("text is %d characters", total)
	}
}

func TestRefusedComponentsFallBackToEmbeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["flags"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Invalid Form Body"}`))
			return
		}
		if r.URL.Query().Get("thread_id") != "9" {
			t.Errorf("thread lost: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	w, logPath := newTestWebhook(t, srv.URL+"?thread_id=9")
	if !w.SendEmbed("Disk", "full", discord.Warning, nil, "") || !w.SendEmbed("Disk", "fuller", discord.Warning, nil, "") {
		t.Fatal("the embed fallback should deliver")
	}
	// One refused components message, then embeds only, and nothing in the fallback log.
	if calls.Load() != 3 {
		t.Fatalf("calls %d", calls.Load())
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("a delivered fallback is not a failure")
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
	// Each delivery is tried 4 times (a 5xx may pass), and counts once: 5 deliveries, then none.
	if got := d.calls.Load(); got != 5*4 {
		t.Fatalf("breaker should open after 5 failed deliveries of 4 tries, server saw %d calls", got)
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
		fields[i] = discord.Field{Name: strings.Repeat("n", 300), Value: "v"}
	}
	fields[0].Value = strings.Repeat("é", 2000)
	e := discord.BuildEmbed(strings.Repeat("t", 300), strings.Repeat("d", 1000), 1, fields, "")
	if len(e.Title) != 256 || len(e.Fields) != 14 || len(e.Fields[1].Name) != 256 || e.Footer != nil {
		t.Fatalf("limits not applied: title=%d fields=%d", len(e.Title), len(e.Fields))
	}
	if n := len([]rune(e.Fields[0].Value)); n != 1024 {
		t.Fatalf("field value should be cut by runes, got %d", n)
	}
	if e = discord.BuildEmbed("t", strings.Repeat("d", 5000), 1, nil, ""); len(e.Description) != 4096 {
		t.Fatalf("description should be cut to 4096, got %d", len(e.Description))
	}
}

// Discord refuses an embed over 6000 characters in all; the description gives way first, then
// the last fields.
func TestBuildEmbedKeepsWithinTheTotal(t *testing.T) {
	total := func(e discord.Embed) int {
		n := len([]rune(e.Title)) + len([]rune(e.Description))
		for _, f := range e.Fields {
			n += len([]rune(f.Name)) + len([]rune(f.Value))
		}
		if e.Footer != nil {
			n += len([]rune(e.Footer.Text))
		}
		return n
	}
	fields := []discord.Field{{Name: "Logs", Value: strings.Repeat("l", 1024)}, {Name: "Packages", Value: strings.Repeat("p", 1024)}}
	e := discord.BuildEmbed("Updates", strings.Repeat("d", 4000), 1, fields, "nas · Updates")
	if total(e) > 6000 || len(e.Fields) != 2 || len([]rune(e.Description)) < 1024 {
		t.Fatalf("the description should shrink to fit, keeping the fields: total=%d desc=%d fields=%d", total(e), len(e.Description), len(e.Fields))
	}
	many := make([]discord.Field, 25)
	for i := range many {
		many[i] = discord.Field{Name: "f", Value: strings.Repeat("v", 1024)}
	}
	e = discord.BuildEmbed(strings.Repeat("t", 300), strings.Repeat("d", 5000), 1, many, strings.Repeat("f", 3000))
	if total(e) > 6000 || len(e.Fields) == 0 || len(e.Description) != 1024 {
		t.Fatalf("over the total: %d, desc=%d fields=%d", total(e), len(e.Description), len(e.Fields))
	}
}

// A webhook's URL holds its token; a network failure's error never carries it.
func TestNetworkErrorsHideTheWebhookToken(t *testing.T) {
	d := newFakeDiscord(t)
	d.Close()
	w, _ := newTestWebhook(t, d.URL+"/api/webhooks/1/SECRETTOKEN")
	if w.SendEmbed("Disk", "full", discord.Warning, nil, "") {
		t.Fatal("a closed server should fail")
	}
	if e := w.LastError(); e == "" || strings.Contains(e, "SECRETTOKEN") {
		t.Fatalf("error %q", e)
	}
}

type capture struct {
	title, desc string
	fields      []discord.Field
}

func (c *capture) SendEmbed(title, desc string, _ discord.Severity, fields []discord.Field, _ string) bool {
	c.title, c.desc, c.fields = title, desc, fields
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
	if c.title != "CPU at 93%" || c.field("Busiest cores") != "Core 0 · 99%\nCore 1 · 10%" {
		t.Fatalf("cpu: %s %v", c.title, c.fields)
	}
	discord.MemoryAlert(c, 91, 80, collect.Memory{Total: 8 << 30, Used: 7 << 30, Percent: 91}, collect.Memory{})
	if c.title != "Memory at 91%" || c.field("Used") != "7.00 GB" {
		t.Fatalf("memory: %s", c.title)
	}
	discord.ProcessAlert(c, collect.Process{PID: 42, Name: "worker", CPUPercent: 99}, "cpu", 50)
	if c.title != "worker is using 99% CPU" || c.field("PID") != "42" {
		t.Fatalf("process: %s %v", c.title, c.fields)
	}
	discord.LogCleanupAlert(c, []discord.CleanedLog{{Path: "/var/log/syslog", OriginalSize: "2 GB", CurrentSize: "0 B"}}, "100 MB")
	if !strings.Contains(fieldsText(c.fields), "/var/log/syslog") {
		t.Fatalf("log cleanup: %v", c.fields)
	}
	discord.StalledTorrent(c, discord.TorrentInfo{Name: "linux.iso", State: "stalleddl", SavePath: "/data"}, 600, []string{"reannounce", "resume"})
	if c.title != "Stalled: linux.iso" || c.desc != "No progress for 10m. Nightjar asked its trackers again, then resumed it." {
		t.Fatalf("stalled: %s %q %v", c.title, c.desc, c.fields)
	}
	discord.MetricsReport(c, metrics.New().Summary())
	if c.title == "" {
		t.Fatal("metrics report sent nothing")
	}
}

func TestSpan(t *testing.T) {
	for in, want := range map[float64]string{0: "0s", 42: "42s", 60: "1m", 192: "3m 12s", 3600: "1h", 7500: "2h 5m", 86400: "1d", 273600: "3d 4h"} {
		if got := discord.Span(in); got != want {
			t.Errorf("Span(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFooterReadsAsTheArea(t *testing.T) {
	o := discord.Origin{Host: "nas"}
	for cat, want := range map[string]string{"Storage Monitoring": "nas · Storage", "Uptime": "nas · Uptime", "Watchdog": "nas · Nightjar", "": "nas"} {
		if got := o.Footer(cat); got != want {
			t.Errorf("Footer(%q) = %q, want %q", cat, got, want)
		}
	}
	if got := (discord.Origin{}).Footer("Monitor"); got != "Nightjar" {
		t.Errorf("no host: %q", got)
	}
}

func fieldsText(fields []discord.Field) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Name + "=" + f.Value + "\n")
	}
	return b.String()
}
