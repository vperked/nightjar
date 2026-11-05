package services_test

import (
	"strings"
	"testing"
	"time"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

func TestSchedules(t *testing.T) {
	for _, ok := range []string{"0 3 * * *", "*/15 * * * *", "0 9-17 * * 1-5", "@daily", "every 15m", "every 1d", "30 2 1,15 * *", "0 0 * * 7"} {
		if _, err := services.ParseSchedule(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0 3 * *", "60 * * * *", "every 10s", "every soon", "* * 0 * *", "*/0 * * * *"} {
		if _, err := services.ParseSchedule(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func heartbeat(t *testing.T, schedule string, over map[string]any) (*services.Monitor, services.Spec) {
	t.Helper()
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	raw := `{"name":"backup","type":"heartbeat","schedule":"` + schedule + `","grace":600`
	if v, ok := over["max_runtime"]; ok {
		raw += `,"max_runtime":` + v.(string)
	}
	spec, err := services.ParseSpec([]byte(raw + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Target) < 16 || spec.Interval != 30 || spec.Retries != 0 {
		t.Fatalf("a new heartbeat gets a token and is looked at every half minute: %+v", spec)
	}
	m := services.NewMonitor([]services.Spec{spec}, store)
	return m, spec
}

func at(day, hour, minute int) float64 {
	return float64(time.Date(2026, 11, day, hour, minute, 0, 0, time.Local).Unix())
}

func TestNightlyHeartbeat(t *testing.T) {
	m, spec := heartbeat(t, "0 3 * * *", map[string]any{"max_runtime": "3600"})
	// Added in the afternoon: nothing is due until tomorrow's run.
	if r := m.Heartbeat(spec, at(2, 15, 0)); !r.Up || !strings.Contains(r.Detail, "no ping yet") {
		t.Fatalf("waiting: %+v", r)
	}
	// The run starts at 03:00 and finishes at 03:20, inside the hour it may take.
	m.Ping(spec.Target, "start", "", at(3, 3, 0))
	if r := m.Heartbeat(spec, at(3, 3, 15)); !r.Up || !strings.Contains(r.Detail, "running since") {
		t.Fatalf("running: %+v", r)
	}
	if _, ok := m.Ping(spec.Target, "", "done: 812 files", at(3, 3, 20)); !ok {
		t.Fatal("ping not taken")
	}
	r := m.Heartbeat(spec, at(3, 9, 0))
	if !r.Up || r.LatencyMS == nil || *r.LatencyMS != 20*60*1000 {
		t.Fatalf("ran well, taking 20 minutes: %+v", r)
	}
	// The next night it never runs: down once the grace has passed.
	if r := m.Heartbeat(spec, at(4, 3, 5)); !r.Up {
		t.Fatalf("still within the grace: %+v", r)
	}
	if r := m.Heartbeat(spec, at(4, 3, 11)); r.Up || !strings.Contains(r.Detail, "missed the run") {
		t.Fatalf("missed: %+v", r)
	}
	// It runs late and fails with exit code 3.
	m.Ping(spec.Target, "start", "", at(4, 4, 0))
	m.Ping(spec.Target, "3", "rsync: connection unexpectedly closed", at(4, 4, 2))
	if r := m.Heartbeat(spec, at(4, 4, 3)); r.Up || !strings.Contains(r.Detail, "code 3") || !strings.Contains(r.Detail, "connection unexpectedly closed") {
		t.Fatalf("failed: %+v", r)
	}
	// The night after, it hangs.
	m.Ping(spec.Target, "start", "", at(5, 3, 0))
	if r := m.Heartbeat(spec, at(5, 4, 1)); r.Up || !strings.Contains(r.Detail, "still running") {
		t.Fatalf("too long: %+v", r)
	}
	if _, ok := m.Ping("no-such-token-at-all", "", "", at(5, 5, 0)); ok {
		t.Fatal("an unknown token was taken")
	}
}

func TestEveryHeartbeat(t *testing.T) {
	m, spec := heartbeat(t, "every 1h", nil)
	start := at(2, 12, 0)
	m.Heartbeat(spec, start)
	m.Ping(spec.Target, "", "", start+60)
	if r := m.Heartbeat(spec, start+3600+600); !r.Up {
		t.Fatalf("on time: %+v", r)
	}
	if r := m.Heartbeat(spec, start+60+3600+601); r.Up || !strings.Contains(r.Detail, "late") {
		t.Fatalf("late: %+v", r)
	}
}

func TestRepairSpec(t *testing.T) {
	spec, err := services.ParseSpec([]byte(`{"name":"plex","type":"http","target":"http://127.0.0.1:32400","repair":{"action":"restart","unit":"plexmediaserver"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := spec.Repair; r.Unit != "plexmediaserver.service" || r.Max != 2 || r.Wait != 120 {
		t.Fatalf("defaults: %+v", r)
	}
	for _, bad := range []string{
		`{"action":"restart","unit":"plex; rm -rf /"}`,
		`{"action":"script","script":"../../bin/sh"}`,
		`{"action":"reboot"}`,
	} {
		if _, err := services.ParseSpec([]byte(`{"name":"x","type":"tcp","target":"a:1","repair":` + bad + `}`)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// A heartbeat that never pings goes late though Nightjar restarts more often than its schedule:
// when it began listening is kept, not started over on each restart.
func TestHeartbeatNeverPingedAcrossRestarts(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	spec, err := services.ParseSpec([]byte(`{"name":"backup","type":"heartbeat","schedule":"every 1d","grace":600}`))
	if err != nil {
		t.Fatal(err)
	}
	start := at(2, 12, 0)
	services.NewMonitor([]services.Spec{spec}, store).Heartbeat(spec, start)
	services.NewMonitor([]services.Spec{spec}, store).Heartbeat(spec, start+12*3600)
	if r := services.NewMonitor([]services.Spec{spec}, store).Heartbeat(spec, start+86400+601); r.Up || !strings.Contains(r.Detail, "late") {
		t.Fatalf("late a day after it was added, restarts or not: %+v", r)
	}
}
