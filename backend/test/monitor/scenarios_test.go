package monitor_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"servermonitor/internal/collect"
	"servermonitor/internal/discord"
	"servermonitor/internal/kernel"
	"servermonitor/internal/monitor"
	"servermonitor/internal/plex"
	"servermonitor/internal/qbittorrent"
)

func TestTemperatureAlertWaitsForSustainedHeat(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(testContext(wh))
	temp := func(now, c float64) { eval(monitor.Snapshot{Now: now, CPUTemp: &c, CPUPercent: 50}) }

	temp(0, 90)
	temp(20, 90)
	expectTitles(t, wh)
	temp(25, 60) // cooling down resets the clock
	temp(40, 90)
	temp(65, 90)
	expectTitles(t, wh)
	temp(70, 91)
	expectTitles(t, wh, "Temperature Alert")
	temp(75, 91)
	expectTitles(t, wh, "Temperature Alert")
}

func TestNetworkAndGPUAlerts(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(testContext(wh))
	mbps := 150.0
	eval(monitor.Snapshot{Now: 1, NetworkMBps: &mbps, Bandwidth: &collect.Bandwidth{SentPerSec: 1e6, RecvPerSec: 2e6}})
	eval(monitor.Snapshot{Now: 2, GPU: &collect.GPU{Utilization: 50}})
	eval(monitor.Snapshot{Now: 3, GPU: &collect.GPU{Utilization: 95}})
	expectTitles(t, wh, "Network Bandwidth Alert", "GPU Usage Alert")
}

func diskContext(wh *fakeSender) *monitor.Context {
	ctx := testContext(nil)
	ctx.DiskWebhook = wh
	ctx.Cfg.MonitorIndividualPartitions = true
	return ctx
}

func TestFullPartitionAlertsOnce(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(diskContext(wh))
	// Above the alert threshold but below cleanup, so nothing on disk is touched.
	mount := t.TempDir()
	full := []collect.Partition{{Mountpoint: mount, Percent: 82, Total: 100 << 30, Used: 82 << 30}}
	for i := range 3 {
		eval(monitor.Snapshot{Now: 1000 + float64(i*5), Partitions: full})
	}
	expectTitles(t, wh, "Disk Usage Alert: "+mount)

	eval(monitor.Snapshot{Now: 2000, Partitions: []collect.Partition{{Mountpoint: t.TempDir(), Percent: 77}}})
	expectTitles(t, wh, "Disk Usage Alert: "+mount, "Drive Early Warning")
}

func TestMediaDirectoryAlert(t *testing.T) {
	wh := &fakeSender{}
	ctx := diskContext(wh)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "movies"), 0o755)
	os.WriteFile(filepath.Join(dir, "movies", "a.mkv"), make([]byte, 4096), 0o644)
	ctx.Cfg.MediaMonitorEnabled, ctx.Cfg.MediaDirectory = true, dir
	ctx.Cfg.MediaCheckInterval, ctx.Cfg.MediaDirectoryThresholdBytes = 2, 1024
	eval := evaluator(ctx)
	eval(monitor.Snapshot{Now: 1})
	expectTitles(t, wh)
	eval(monitor.Snapshot{Now: 2})
	expectTitles(t, wh, "Directory Size Alert")
}

func TestReportErrorIsRateLimited(t *testing.T) {
	crash := &fakeSender{}
	ctx := testContext(&fakeSender{})
	ctx.CrashWebhook = crash
	ctx.ErrorLogFile = filepath.Join(t.TempDir(), "errors.log")
	ctx.ReportError(errors.New("boom"), "cycle")
	ctx.ReportError(errors.New("again"), "cycle")
	expectTitles(t, crash, "Monitoring Error Detected")
	data, _ := os.ReadFile(ctx.ErrorLogFile)
	if !strings.Contains(string(data), "boom") || !strings.Contains(string(data), "again") {
		t.Fatalf("every error belongs in the error log: %q", data)
	}
}

func TestMetricsReportInterval(t *testing.T) {
	wh := &fakeSender{}
	ctx := testContext(wh)
	ctx.Cfg.MetricsReportInterval = 3600
	eval := evaluator(ctx)
	eval(monitor.Snapshot{Now: 10_000})
	eval(monitor.Snapshot{Now: 12_000})
	eval(monitor.Snapshot{Now: 13_600})
	expectTitles(t, wh, "Server Monitor Metrics Report", "Server Monitor Metrics Report")
	if ctx.Latest().Now != 13_600 {
		t.Fatal("evaluated snapshots are published for the API")
	}
}

type failingSender struct{ fakeSender }

func (f *failingSender) SendEmbed(title, d string, c discord.Severity, fields []discord.Field, footer string) bool {
	f.fakeSender.SendEmbed(title, d, c, fields, footer)
	return false
}

func TestFailedMetricsReportBacksOff(t *testing.T) {
	wh := &failingSender{}
	ctx := testContext(nil)
	ctx.Webhook = wh
	ctx.Cfg.MetricsReportInterval = 3600
	eval := evaluator(ctx)
	for now := 10_000.0; now < 10_300; now += 10 {
		eval(monitor.Snapshot{Now: now})
	}
	eval(monitor.Snapshot{Now: 10_300})
	expectTitles(t, &wh.fakeSender, "Server Monitor Metrics Report", "Server Monitor Metrics Report")
}

func TestWatchdogAlertsOnceWhenTheLoopStops(t *testing.T) {
	wh := &fakeSender{}
	w := &monitor.Watchdog{Timeout: 20 * time.Millisecond, AlertCooldown: time.Hour, Webhook: wh}
	w.Start()
	defer w.Stop()
	time.Sleep(120 * time.Millisecond)
	expectTitles(t, wh, "Monitoring Loop Stopped")
	// A beating loop re-arms the alert.
	w.Heartbeat()
	w.ResetAlertState()
	time.Sleep(120 * time.Millisecond)
	expectTitles(t, wh, "Monitoring Loop Stopped", "Monitoring Loop Stopped")
}

func TestFreezeMonitorFlagsStalls(t *testing.T) {
	// Built directly to get timeouts shorter than NewFreezeMonitor allows.
	f := &monitor.FreezeMonitor{Threshold: 0.05, HeartbeatTimeout: 0.05}
	if st := f.Check(); !st.Frozen || !strings.Contains(strings.Join(st.Reasons, ","), "Heartbeat timeout") {
		t.Fatalf("a monitor that never beat is frozen: %v", st.Reasons)
	}
	f.Heartbeat()
	f.UpdateMetrics(50, 40, &collect.IO{ReadBytes: 1}, nil)
	time.Sleep(100 * time.Millisecond)
	f.UpdateMetrics(50, 40, &collect.IO{ReadBytes: 1}, nil) // busy CPU, nothing moved
	if st := f.Check(); !st.Frozen || !strings.Contains(strings.Join(st.Reasons, ","), "Metrics stalled") {
		t.Fatalf("unchanged metrics on a busy CPU should count as stalled: %v", st.Reasons)
	}
	f.Heartbeat()
	f.UpdateMetrics(60, 40, &collect.IO{ReadBytes: 2}, nil)
	if st := f.Check(); st.Frozen {
		t.Fatalf("progress should clear the freeze: %v", st.Reasons)
	}
}

// fakeQB is a qBittorrent Web API serving a mutable torrent list and recording actions.
type fakeQB struct {
	*httptest.Server
	mu      sync.Mutex
	list    string
	fail    bool
	actions []string
}

func newFakeQB(t *testing.T) *fakeQB {
	f := &fakeQB{list: "[]"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/api/v2/auth/login":
			w.Write([]byte("Ok."))
		case p == "/api/v2/app/version":
			w.Write([]byte("v5"))
		case p == "/api/v2/transfer/info":
			w.Write([]byte(`{"dl_info_speed":2048,"up_info_speed":512,"connection_status":"connected","dht_nodes":300}`))
		case p == "/api/v2/torrents/info":
			if f.fail {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write([]byte(f.list))
		default:
			r.ParseForm()
			f.actions = append(f.actions, strings.TrimPrefix(p, "/api/v2/torrents/")+":"+r.Form.Get("hashes"))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeQB) set(list string, fail bool) {
	f.mu.Lock()
	f.list, f.fail = list, fail
	f.mu.Unlock()
}

func (f *fakeQB) recorded() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.actions, " ")
}

func TestTorrentLifecycle(t *testing.T) {
	qbServer := newFakeQB(t)
	qb, _ := qbittorrent.New(qbServer.URL, 0, "admin", "x")
	wh := &fakeSender{}
	ctx := testContext(nil)
	ctx.QBittorrent, ctx.QBittorrentWebhook = qb, wh
	c := &ctx.Cfg
	c.StalledThreshold, c.StalledActionCooldown, c.StalledRecoveryGrace = 300, 1800, 120
	c.StalledAutoHandle, c.StalledRetryBackoff, c.StalledMaxRecoveryAttempts = true, 60, 2
	c.SeedingRatioLimit, c.SeedingRatioActionCooldown, c.SeedingRatioRetryBackoff = 2, 1800, 60
	eval := evaluator(ctx)
	poll := func(now float64, list string, fail bool) {
		qbServer.set(list, fail)
		// The watcher acts on every second cycle.
		eval(monitor.Snapshot{Now: now})
		eval(monitor.Snapshot{Now: now})
	}
	const (
		stalled     = `[{"hash":"a","name":"distro.iso","state":"stalledDL","progress":0.5}]`
		downloading = `[{"hash":"a","name":"distro.iso","state":"downloading","progress":0.9,"dlspeed":1000}]`
		recovery    = "reannounce:a start:a"
	)

	poll(0, stalled, false)
	poll(200, stalled, false)
	expectTitles(t, wh)
	poll(400, stalled, false) // stalled past the threshold: first recovery and one notification
	poll(500, stalled, false) // action cooldown
	poll(2300, stalled, false)
	poll(5000, stalled, false) // out of recovery attempts
	expectTitles(t, wh, "Torrent Stalled")
	if got := qbServer.recorded(); got != recovery+" "+recovery {
		t.Fatalf("recovery actions %q", got)
	}

	// A failed list must not drop what the watcher knows: had it forgotten the torrent,
	// the stall would count as new and be announced again.
	poll(5050, "[]", true)
	poll(5060, stalled, false)
	poll(5400, stalled, false)
	expectTitles(t, wh, "Torrent Stalled")

	// Moving again for the grace period clears the stall, so a new stall is reported.
	poll(5500, downloading, false)
	poll(5700, downloading, false)
	poll(5800, stalled, false)
	poll(6200, stalled, false)
	expectTitles(t, wh, "Torrent Stalled", "Torrent Stalled")

	poll(6300, `[{"hash":"a","name":"distro.iso","state":"uploading","progress":1,"ratio":0.5}]`, false)
	expectTitles(t, wh, "Torrent Stalled", "Torrent Stalled", "Download Complete", "Torrent Seeding")
	poll(6400, `[{"hash":"a","name":"distro.iso","state":"uploading","progress":1,"ratio":2.5}]`, false)
	expectTitles(t, wh, "Torrent Stalled", "Torrent Stalled", "Download Complete", "Torrent Seeding", "Seeding Ratio Limit Reached")
	if got := qbServer.recorded(); !strings.HasSuffix(got, recovery+" delete:a") {
		t.Fatalf("ratio limit should delete the torrent, actions %q", got)
	}

	// Gone from qBittorrent: forgotten, so its return is a fresh torrent with a fresh stall.
	poll(6500, "[]", false)
	poll(6600, stalled, false)
	poll(6900, stalled, false)
	expectTitles(t, wh, "Torrent Stalled", "Torrent Stalled", "Download Complete", "Torrent Seeding", "Seeding Ratio Limit Reached", "Torrent Stalled")
}

func TestTorrentBoardForTheDashboard(t *testing.T) {
	qbServer := newFakeQB(t)
	qb, _ := qbittorrent.New(qbServer.URL, 0, "admin", "x")
	ctx := testContext(nil)
	ctx.QBittorrent, ctx.QBittorrentBoard = qb, &qbittorrent.Board{}
	ctx.Cfg.StalledThreshold, ctx.Cfg.StalledAutoHandle = 300, false
	eval := evaluator(ctx)
	poll := func(now float64, list string, fail bool) {
		qbServer.set(list, fail)
		eval(monitor.Snapshot{Now: now})
		eval(monitor.Snapshot{Now: now})
	}
	stalled := `[{"hash":"a","name":"distro.iso","state":"stalledDL","progress":0.5,"size":100}]`
	poll(1000, stalled, false)
	poll(1090, stalled, false)
	v := ctx.QBittorrentBoard.View()
	if !v.Connected || v.Version != "v5" || len(v.Torrents) != 1 || v.Transfer == nil || v.Transfer.DLSpeed != 2048 || len(v.History) != 2 {
		t.Fatalf("view: %+v", v)
	}
	if e := v.Torrents[0]; e.State != "stalleddl" || e.StalledFor != 90 {
		t.Fatalf("the board says how long a torrent has been stalled: %+v", e)
	}

	// A failed fetch keeps the last list but says the connection is down.
	poll(1100, "[]", true)
	v = ctx.QBittorrentBoard.View()
	if v.Connected || v.Error == "" || len(v.Torrents) != 1 || len(v.History) != 2 {
		t.Fatalf("after a failed fetch: %+v", v)
	}
}

// fakePlex serves a PMS whose availability and sessions the test controls.
type fakePlex struct {
	*httptest.Server
	down     atomic.Bool
	sessions atomic.Value
}

func newFakePlex(t *testing.T) *fakePlex {
	f := &fakePlex{}
	f.sessions.Store(`[]`)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/status/sessions":
			w.Write([]byte(`{"MediaContainer":{"Metadata":` + f.sessions.Load().(string) + `}}`))
		case "/":
			w.Write([]byte(`{"MediaContainer":{"friendlyName":"nas","version":"1.40"}}`))
		default:
			w.Write([]byte(`{"MediaContainer":{}}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func plexSession(key, title string, transcode bool) string {
	ts := ""
	if transcode {
		ts = `,"TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy"}`
	}
	return `{"sessionKey":"` + key + `","type":"movie","title":"` + title + `","User":{"title":"sam"},"Player":{"title":"TV"}` + ts + `}`
}

func TestPlexAlerts(t *testing.T) {
	pms := newFakePlex(t)
	client, _ := plex.New(pms.URL, "tok", true, 2*time.Second)
	wh := &fakeSender{}
	ctx := testContext(nil)
	ctx.PlexWebhook, ctx.Plex = wh, plex.NewMonitor(client, 15, 1)
	ctx.Cfg.PlexNotifyStreams, ctx.Cfg.PlexTranscodeAlert = true, 2
	eval := evaluator(ctx)

	pms.sessions.Store("[" + plexSession("1", "Heat", true) + "," + plexSession("2", "Ronin", true) + "]")
	eval(monitor.Snapshot{Now: 0}) // streams already playing at startup are not announced
	expectTitles(t, wh, "Plex Transcode Load")

	pms.sessions.Store("[" + plexSession("1", "Heat", true) + "," + plexSession("2", "Ronin", true) + "," + plexSession("3", "Thief", false) + "]")
	eval(monitor.Snapshot{Now: 15})
	expectTitles(t, wh, "Plex Transcode Load", "Now playing: Thief")

	pms.down.Store(true)
	eval(monitor.Snapshot{Now: 30}) // first failure is within PLEX_RETRIES
	expectTitles(t, wh, "Plex Transcode Load", "Now playing: Thief")
	eval(monitor.Snapshot{Now: 45})
	expectTitles(t, wh, "Plex Transcode Load", "Now playing: Thief", "Plex Down")

	pms.down.Store(false)
	eval(monitor.Snapshot{Now: 60})
	expectTitles(t, wh, "Plex Transcode Load", "Now playing: Thief", "Plex Down", "Plex Recovered")
}

func TestHardwareErrorDescriptions(t *testing.T) {
	event := func(typ, device, source string, count, increase int64, message string) kernel.Event {
		return kernel.Event{Type: typ, Device: device, Source: source, Count: count, CountIncrease: increase, ErrorType: "io", Message: message}
	}
	cases := map[string]string{
		monitor.DescribeHardwareError(event("high_interrupts", "eth0", "", 2_500_000, 0, "")): "2.50M interrupts",
		monitor.DescribeHardwareError(event("disk", "sda", "smart", 7, 2, "")):                "2 new",
		monitor.DescribeHardwareError(event("disk", "sda", "smart", 7, 0, "")):                "(sda): 7",
		monitor.DescribeHardwareError(event("mce", "", "", 0, 0, strings.Repeat("x", 300))):   "**unknown**: xxx",
		monitor.DescribeHardwareError(event("mce", "", "edac", 0, 0, "")):                     "mce error detected",
	}
	for got, want := range cases {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not contain %q", got, want)
		}
	}
}
