package monitor_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/monitor"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/services"
)

// fieldSender keeps every alert's fields as text, to check what an alert gives away.
type fieldSender struct {
	fakeSender
	mu     sync.Mutex
	fields []string
}

func (f *fieldSender) SendEmbed(title, desc string, sev discord.Severity, fields []discord.Field, footer string) bool {
	f.mu.Lock()
	for _, fl := range fields {
		f.fields = append(f.fields, fl.Name+"="+fl.Value)
	}
	f.mu.Unlock()
	return f.fakeSender.SendEmbed(title, desc, sev, fields, footer)
}

func (f *fieldSender) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.fields, "\n")
}

func TestAlertsDoNotCarryCheckSecrets(t *testing.T) {
	wh := &fieldSender{}
	ctx := testContext(nil)
	ctx.Webhook = wh
	const token = "Xy3secretHeartbeatToken"
	ctx.Services = services.NewMonitor([]services.Spec{
		// No schedule: judged down at once.
		{Name: "backup", Type: "heartbeat", Target: token, Interval: 30},
		// Nothing listens on port 1.
		{Name: "nas", Type: "http", Target: "http://admin:hunter2@127.0.0.1:1/status?apikey=abc123&page=x", Timeout: 2, Interval: 5},
	}, nil)
	eval := evaluator(ctx)
	eval(monitor.Snapshot{Now: 1000})
	if got := len(wh.all()); got != 2 {
		t.Fatalf("want both checks down, got %q", wh.all())
	}
	text := wh.text()
	for _, secret := range []string{token, "hunter2", "admin", "abc123"} {
		if strings.Contains(text, secret) {
			t.Fatalf("an alert gave away %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "127.0.0.1:1/status") || !strings.Contains(text, "page=x") {
		t.Fatalf("the address itself still belongs in the alert:\n%s", text)
	}
}

func TestSlowUptimeCheckBacksOff(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	wh := &fakeSender{}
	ctx := testContext(wh)
	// Checked every 10 minutes, longer than the 300s recovery period.
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "api", Type: "http", Target: upstream.URL, Timeout: 2, Interval: 600}}, nil)
	eval := evaluator(ctx)
	for now := 1000.0; now <= 2800; now += 600 {
		eval(monitor.Snapshot{Now: now})
	}
	// Each result is the same outage, so the cooldown escalates (300s, 600s, 1200s) instead of
	// every result starting a new episode and alerting.
	expectTitles(t, wh, "api is down", "api is down", "api is down")
}

func TestNVMeEarlyWarningIsSentOnce(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(diskContext(wh))
	mount := t.TempDir()
	eval(monitor.Snapshot{Now: 1000, Partitions: []collect.Partition{{Mountpoint: mount, Percent: 77, IsNVMe: true}}})
	expectTitles(t, wh, mount+" is filling up")
	eval(monitor.Snapshot{Now: 1005, Partitions: []collect.Partition{{Mountpoint: mount, Percent: 82, IsNVMe: true}}})
	expectTitles(t, wh, mount+" is filling up", mount+" is 82")
}

func TestTotalOnlyWhenPartitionsAreNotWatchedOneByOne(t *testing.T) {
	wh := &fakeSender{}
	ctx := diskContext(wh)
	ctx.Cfg.MonitorIndividualPartitions = false
	eval := evaluator(ctx)
	full := []collect.Partition{{Mountpoint: t.TempDir(), Percent: 82}}
	eval(monitor.Snapshot{Now: 1000, Partitions: full, DiskPercent: 82})
	expectTitles(t, wh, "Disks are 82% full")
}

func TestManyTorrentsKeepTheirState(t *testing.T) {
	qbServer := newFakeQB(t)
	qb, _ := qbittorrent.New(qbServer.URL, 0, "admin", "x")
	wh := &fakeSender{}
	ctx := testContext(nil)
	ctx.QBittorrent, ctx.QBittorrentWebhook = qb, wh
	ctx.Cfg.StalledThreshold, ctx.Cfg.StalledActionCooldown, ctx.Cfg.StalledMaxRecoveryAttempts = 300, 1800, 1
	eval := evaluator(ctx)
	const n = 1500
	list := make([]string, n)
	for i := range list {
		list[i] = fmt.Sprintf(`{"hash":"h%d","name":"t%d","state":"stalledDL","progress":0.5}`, i, i)
	}
	qbServer.set("["+strings.Join(list, ",")+"]", false)
	for _, now := range []float64{0, 200, 400} {
		eval(monitor.Snapshot{Now: now})
		eval(monitor.Snapshot{Now: now})
	}
	// Every torrent has been stalled since 0: had some been forgotten and re-tracked, their
	// stall would have restarted and not yet reached the threshold.
	if got := len(wh.all()); got != n {
		t.Fatalf("%d of %d stalled torrents were reported", got, n)
	}
}
