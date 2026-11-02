package monitor_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"servermonitor/internal/alerts"
	"servermonitor/internal/collect"
	"servermonitor/internal/config"
	"servermonitor/internal/discord"
	"servermonitor/internal/metrics"
	"servermonitor/internal/monitor"
	"servermonitor/internal/services"
)

type fakeSender struct {
	mu         sync.Mutex
	titles     []string
	severities []discord.Severity
}

func (f *fakeSender) SendEmbed(title, _ string, sev discord.Severity, _ []discord.Field, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles = append(f.titles, title)
	f.severities = append(f.severities, sev)
	return true
}

func (f *fakeSender) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.titles...)
}

func expectTitles(t *testing.T, wh *fakeSender, want ...string) {
	t.Helper()
	got := wh.all()
	if len(got) != len(want) {
		t.Fatalf("got %d alerts %q, want %q", len(got), got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("alert %d: got %q, want prefix %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

// testContext alerts to wh with ordinary thresholds; an empty snapshot trips none of them,
// and nothing that cleans, updates or restarts anything is enabled.
func testContext(wh *fakeSender) *monitor.Context {
	cfg := config.Settings{
		CPUThreshold: 80, MemoryThreshold: 80, GPUThreshold: 80, TempThreshold: 80, TempAlertDuration: 30,
		DiskThreshold: 80, DiskEarlyWarningThreshold: 75, DiskCleanupThreshold: 85, DiskAggressiveCleanupThreshold: 92, DiskEmergencyThreshold: 95,
		NetworkBandwidthThreshold: 100, ProcessCPUThreshold: 1000, ProcessMemoryThreshold: 1000,
		AlertCooldown: 300, HistoryRetentionDays: 90, MetricsReportInterval: 1e12, LogCheckInterval: 1 << 30,
	}
	ctx := &monitor.Context{
		Cfg: cfg, Metrics: metrics.New(), Gates: alerts.NewGates(300, 3600, 2, 300),
		Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(),
	}
	if wh != nil {
		ctx.Webhook = wh
	}
	return ctx
}

// evaluator runs the loop's alert handlers on synthetic snapshots.
func evaluator(ctx *monitor.Context) func(monitor.Snapshot) {
	l := monitor.NewLoop(ctx)
	return func(s monitor.Snapshot) {
		l.Evaluate(&s)
		l.Wait() // uptime checks run in the background; let them report
	}
}

func TestCPUAlertIsThrottled(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(testContext(wh))
	for i := range 5 {
		eval(monitor.Snapshot{Now: float64(i * 5), CPUPercent: 95, PerCore: []float64{95}})
	}
	expectTitles(t, wh, "CPU Usage Alert")
	eval(monitor.Snapshot{Now: 400, CPUPercent: 95})
	expectTitles(t, wh, "CPU Usage Alert", "CPU Usage Alert")
}

func TestBelowThresholdDoesNotAlert(t *testing.T) {
	wh := &fakeSender{}
	eval := evaluator(testContext(wh))
	eval(monitor.Snapshot{Now: 1, CPUPercent: 50, Memory: collect.Memory{Percent: 50}})
	expectTitles(t, wh)
}

func TestServiceDownAndRecoveryAlerts(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer upstream.Close()
	wh := &fakeSender{}
	ctx := testContext(wh)
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "api", Type: "http", Target: upstream.URL, Retries: 1, Timeout: 2, Interval: 5}}, nil)
	eval := evaluator(ctx)

	eval(monitor.Snapshot{Now: 1000}) // first failure is within retries
	expectTitles(t, wh)
	eval(monitor.Snapshot{Now: 1010})
	expectTitles(t, wh, "Service Down: api")
	failing.Store(false)
	eval(monitor.Snapshot{Now: 1020})
	expectTitles(t, wh, "Service Down: api", "Service Recovered: api")
	if wh.severities[0] != discord.Critical || wh.severities[1] != discord.OK {
		t.Fatalf("an outage is critical and its recovery ok, got %v", wh.severities)
	}
}

func TestTemperatureReasonFallsBackWhenNothingStandsOut(t *testing.T) {
	got := monitor.AnalyzeTemperatureReason(monitor.TemperatureInputs{CPUPercent: 5})
	if !strings.Contains(got, "cooling system failure") {
		t.Fatalf("got %q", got)
	}
	got = monitor.AnalyzeTemperatureReason(monitor.TemperatureInputs{CPUPercent: 90, Fans: []collect.Fan{{RPM: 300}}})
	if !strings.Contains(got, "Very high CPU usage") || !strings.Contains(got, "Very low fan speeds") {
		t.Fatalf("got %q", got)
	}
}

func TestTemperatureReasonUsesIORates(t *testing.T) {
	const mb = 1024 * 1024
	got := monitor.AnalyzeTemperatureReason(monitor.TemperatureInputs{CPUPercent: 50, DiskWriteBps: 200 * mb, NetRecvBps: 20 * mb})
	if !strings.Contains(got, "High disk write activity (200 MB/s)") || !strings.Contains(got, "High network download activity (20.0 MB/s)") {
		t.Fatalf("got %q", got)
	}
	if got := monitor.AnalyzeTemperatureReason(monitor.TemperatureInputs{CPUPercent: 50, DiskWriteBps: mb}); strings.Contains(got, "disk") {
		t.Fatalf("light disk activity is not a reason: %q", got)
	}
}

func TestSlowCheckDoesNotHoldUpTheLoop(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
	}))
	defer upstream.Close()
	ctx := testContext(&fakeSender{})
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "slow", Type: "http", Target: upstream.URL, Timeout: 10, Interval: 5}}, nil)
	l := monitor.NewLoop(ctx)

	start := time.Now()
	l.Evaluate(&monitor.Snapshot{Now: 1000})
	l.Evaluate(&monitor.Snapshot{Now: 1010}) // due again, but still running
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("cycles waited %s for a hung check", took)
	}
	close(release)
	l.Wait()
	if n := requests.Load(); n != 1 {
		t.Fatalf("a running check must not start again, got %d requests", n)
	}
	if ctx.Services.State("slow").LastCheck != 1000 {
		t.Fatal("the result should be recorded once the check finishes")
	}
}
