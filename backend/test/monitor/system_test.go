package monitor_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/discord"
	"nightjar/internal/monitor"
	"nightjar/internal/smart"
	"nightjar/internal/storage"
	"nightjar/internal/units"
)

// bodySender also keeps descriptions and fields, to check what an alert says.
type bodySender struct {
	fakeSender
	mu     sync.Mutex
	bodies []string
}

func (b *bodySender) SendEmbed(title, desc string, sev discord.Severity, fields []discord.Field, footer string) bool {
	b.mu.Lock()
	text := desc
	for _, f := range fields {
		text += "\n" + f.Name + ": " + f.Value
	}
	b.bodies = append(b.bodies, text)
	b.mu.Unlock()
	return b.fakeSender.SendEmbed(title, desc, sev, fields, footer)
}

func TestDriveWarningsAreAnnouncedOnce(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	reallocated := 8
	var mu sync.Mutex
	reader := smart.NewReader()
	reader.Run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "--scan" {
			return []byte(`{"devices":[{"name":"/dev/sda","type":"sat"}]}`), nil
		}
		mu.Lock()
		defer mu.Unlock()
		out, _ := json.Marshal(map[string]any{
			"model_name": "WDC WD80EFAX", "serial_number": "S1", "rotation_rate": 5400, "smart_status": map[string]bool{"passed": true},
			"temperature":          map[string]int{"current": 40},
			"ata_smart_attributes": map[string]any{"table": []map[string]any{{"id": 5, "name": "Reallocated_Sector_Ct", "raw": map[string]int{"value": reallocated}}}},
		})
		return out, nil
	}
	wh := &bodySender{}
	ctx := testContext(&wh.fakeSender)
	ctx.Webhook, ctx.Store, ctx.Drives = wh, store, reader
	ctx.Cfg.SmartMonitorEnabled, ctx.Cfg.SmartCheckInterval, ctx.Cfg.SmartHDDTempLimit = true, 60, 55
	eval := evaluator(ctx)

	eval(monitor.Snapshot{Now: 1000})
	expectTitles(t, &wh.fakeSender, "Drive health warning: WDC WD80EFAX (/dev/sda)")
	if !strings.Contains(wh.bodies[0], "8 reallocated sectors (+8)") {
		t.Fatalf("body %q", wh.bodies[0])
	}
	eval(monitor.Snapshot{Now: 1030}) // not due
	eval(monitor.Snapshot{Now: 1100}) // due, nothing new
	expectTitles(t, &wh.fakeSender, "Drive health warning")

	mu.Lock()
	reallocated = 24
	mu.Unlock()
	eval(monitor.Snapshot{Now: 1200})
	expectTitles(t, &wh.fakeSender, "Drive health warning", "Drive health warning")
	if !strings.Contains(wh.bodies[1], "24 reallocated sectors (+16)") {
		t.Fatalf("body %q", wh.bodies[1])
	}

	// A restarted monitor remembers what it already said.
	again := testContext(&wh.fakeSender)
	again.Webhook, again.Store, again.Drives = wh, store, smart.NewReader()
	again.Drives.Run = reader.Run
	again.Cfg.SmartMonitorEnabled, again.Cfg.SmartCheckInterval = true, 60
	monitor.LoadDrives(again)
	if drives, _ := again.Drives.Drives(); len(drives) != 1 || drives[0].Model != "WDC WD80EFAX" {
		t.Fatalf("readings survive a restart: %+v", drives)
	}
	evaluator(again)(monitor.Snapshot{Now: 5000})
	expectTitles(t, &wh.fakeSender, "Drive health warning", "Drive health warning")
}

func TestFailedServiceAlertCarriesItsLogs(t *testing.T) {
	var mu sync.Mutex
	state := "active"
	m := units.New()
	m.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case name == "journalctl":
			return []byte("sonarr[1]: Unhandled exception: database is locked\n"), nil
		case args[0] == "list-units":
			return []byte(`[{"unit":"sonarr.service","load":"loaded","active":"` + state + `"}]`), nil
		}
		return []byte("Id=sonarr.service\nDescription=Sonarr Daemon\nActiveState=" + state + "\nSubState=x\nResult=exit-code\nExecMainStatus=1\nNRestarts=0\n"), nil
	}
	wh := &bodySender{}
	ctx := testContext(&wh.fakeSender)
	ctx.Webhook, ctx.Units = wh, m
	ctx.Cfg.ServiceWatchEnabled, ctx.Cfg.ServiceWatchInterval, ctx.Cfg.ServiceRestartAlert = true, 30, 3
	eval := evaluator(ctx)
	eval(monitor.Snapshot{Now: 100})
	mu.Lock()
	state = "failed"
	mu.Unlock()
	eval(monitor.Snapshot{Now: 110}) // not due yet
	expectTitles(t, &wh.fakeSender)
	eval(monitor.Snapshot{Now: 130})
	expectTitles(t, &wh.fakeSender, "sonarr failed")
	if !strings.Contains(wh.bodies[0], "database is locked") || !strings.Contains(wh.bodies[0], "exit status 1") {
		t.Fatalf("body %q", wh.bodies[0])
	}
	mu.Lock()
	state = "active"
	mu.Unlock()
	eval(monitor.Snapshot{Now: 160})
	expectTitles(t, &wh.fakeSender, "sonarr failed", "sonarr is running again")
}
