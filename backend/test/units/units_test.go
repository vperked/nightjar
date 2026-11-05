package units_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/units"
)

// fakeSystemd serves list-units, show and journalctl from a table of services the test edits.
type fakeSystemd struct {
	mu    sync.Mutex
	units map[string]map[string]string // name → properties
	calls []string
}

func newFake() *fakeSystemd {
	f := &fakeSystemd{units: map[string]map[string]string{}}
	f.set("sonarr.service", "active", "running", 0)
	f.set("nginx.service", "active", "running", 0)
	return f
}

func (f *fakeSystemd) set(name, active, sub string, restarts int, extra ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := map[string]string{"Id": name, "Description": strings.TrimSuffix(name, ".service") + " daemon", "ActiveState": active, "SubState": sub,
		"UnitFileState": "enabled", "Result": "success", "ExecMainStatus": "0", "NRestarts": fmt.Sprint(restarts), "Transient": "no"}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	f.units[name] = p
}

func (f *fakeSystemd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch {
	case name == "systemctl" && args[0] == "list-units":
		var rows []map[string]string
		for n, p := range f.units {
			rows = append(rows, map[string]string{"unit": n, "load": "loaded", "active": p["ActiveState"], "sub": p["SubState"]})
		}
		return json.Marshal(rows)
	case name == "systemctl" && args[0] == "show":
		var blocks []string
		for _, n := range args[3:] {
			p := f.units[n]
			var lines []string
			for k, v := range p {
				lines = append(lines, k+"="+v)
			}
			blocks = append(blocks, strings.Join(lines, "\n"))
		}
		return []byte(strings.Join(blocks, "\n\n") + "\n"), nil
	case name == "journalctl":
		return []byte("2026-11-02T01:00:00 host sonarr[1]: crashed: out of memory\n"), nil
	case name == "systemctl":
		if _, ok := f.units[args[2]]; !ok {
			return nil, errors.New("unit not found")
		}
		return nil, nil
	}
	return nil, errors.New("unexpected " + name)
}

func poll(t *testing.T, m *units.Monitor, at time.Time, ignore ...string) []units.Event {
	t.Helper()
	ev, err := m.Poll(context.Background(), at, ignore, 3)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func kinds(ev []units.Event) string {
	var out []string
	for _, e := range ev {
		out = append(out, fmt.Sprintf("%d:%s", e.Kind, e.Unit.Name))
	}
	return strings.Join(out, " ")
}

func TestFailureAndRecovery(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	t0 := time.Unix(1_800_000_000, 0)
	if ev := poll(t, m, t0); len(ev) != 0 {
		t.Fatalf("the first poll learns: %s", kinds(ev))
	}
	f.set("sonarr.service", "failed", "failed", 0, "Result", "exit-code", "ExecMainStatus", "137")
	ev := poll(t, m, t0.Add(30*time.Second))
	if kinds(ev) != "0:sonarr.service" || ev[0].Unit.ExitStatus != 137 || ev[0].Unit.Result != "exit-code" {
		t.Fatalf("failure: %+v", ev)
	}
	if ev := poll(t, m, t0.Add(60*time.Second)); len(ev) != 0 {
		t.Fatalf("a failure is announced once: %s", kinds(ev))
	}
	if list := m.Units(); list[0].Name != "sonarr.service" {
		t.Fatalf("failed services list first: %v", list[0].Name)
	}
	f.set("sonarr.service", "active", "running", 0)
	ev = poll(t, m, t0.Add(330*time.Second))
	if kinds(ev) != "1:sonarr.service" || ev[0].Downtime != 300*time.Second {
		t.Fatalf("recovery: %+v", ev)
	}
}

func TestAlreadyFailedAtStart(t *testing.T) {
	f := newFake()
	f.set("nginx.service", "failed", "failed", 0)
	quiet := units.New()
	quiet.Run = f.run
	if ev := poll(t, quiet, time.Now()); len(ev) != 0 {
		t.Fatal("a monitor restarting to apply settings does not repeat itself")
	}
	fresh := units.New()
	fresh.Run, fresh.AnnounceExisting = f.run, true
	if ev := poll(t, fresh, time.Now()); kinds(ev) != "0:nginx.service" {
		t.Fatalf("a fresh start (after a reboot, say) reports it: %s", kinds(ev))
	}
}

func TestRestartLoop(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	t0 := time.Unix(1_800_000_000, 0)
	poll(t, m, t0)
	f.set("sonarr.service", "activating", "auto-restart", 2)
	ev := poll(t, m, t0.Add(time.Minute))
	if kinds(ev) != "0:sonarr.service" {
		t.Fatalf("stuck restarting counts as down: %s", kinds(ev))
	}
	f.set("sonarr.service", "active", "running", 3)
	ev = poll(t, m, t0.Add(2*time.Minute))
	if kinds(ev) != "2:sonarr.service 1:sonarr.service" || ev[0].Restarts != 3 {
		t.Fatalf("three restarts in ten minutes: %+v", ev)
	}
	f.set("sonarr.service", "active", "running", 4)
	if ev := poll(t, m, t0.Add(3*time.Minute)); len(ev) != 0 {
		t.Fatalf("a restart loop is announced once: %s", kinds(ev))
	}
	// Quiet for longer than the window: a new loop is new news.
	poll(t, m, t0.Add(20*time.Minute))
	poll(t, m, t0.Add(21*time.Minute))
	f.set("sonarr.service", "active", "running", 7)
	if ev := poll(t, m, t0.Add(22*time.Minute)); kinds(ev) != "2:sonarr.service" {
		t.Fatalf("a later loop: %s", kinds(ev))
	}
}

func TestIgnoredAndTransientServices(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	poll(t, m, time.Now())
	f.set("apt-daily.service", "failed", "failed", 0)
	f.set("run-u42.service", "failed", "failed", 0, "Transient", "yes")
	if ev := poll(t, m, time.Now(), "apt-*"); len(ev) != 0 {
		t.Fatalf("ignored and transient services stay quiet: %s", kinds(ev))
	}
	for _, u := range m.Units() {
		if u.Name == "run-u42.service" {
			t.Fatal("transient units are someone's one-off command, not a service")
		}
		if u.Name == "apt-daily.service" && !u.Ignored {
			t.Fatal("ignored services are marked")
		}
	}
}

func TestLogsAndControlOnlyForKnownServices(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	poll(t, m, time.Now())
	if _, err := m.Logs(context.Background(), "../../etc/passwd", 10); !errors.Is(err, units.ErrUnknown) {
		t.Fatalf("logs of an unknown unit: %v", err)
	}
	if err := m.Control(context.Background(), "restart", "evil.service"); !errors.Is(err, units.ErrUnknown) {
		t.Fatalf("restart of an unknown unit: %v", err)
	}
	if err := m.Control(context.Background(), "mask", "sonarr.service"); err == nil {
		t.Fatal("only start, stop and restart")
	}
	lines, err := m.Logs(context.Background(), "sonarr.service", 5)
	if err != nil || len(lines) != 1 {
		t.Fatalf("logs: %v %v", lines, err)
	}
	if err := m.Control(context.Background(), "restart", "sonarr.service"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, "|"), "systemctl restart -- sonarr.service") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestCauseShowsUntilRecovery(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	t0 := time.Unix(1_800_000_000, 0)
	poll(t, m, t0)
	f.set("sonarr.service", "failed", "failed", 0, "Result", "exit-code", "ExecMainStatus", "1")
	poll(t, m, t0.Add(30*time.Second))
	lines, err := m.Logs(context.Background(), "sonarr.service", 80)
	if err != nil {
		t.Fatal(err)
	}
	found := m.Diagnose("sonarr.service", lines)
	if len(found) == 0 || found[0].ID != "oom" {
		t.Fatalf("the journal says out of memory: %+v", found)
	}
	m.SetCause("sonarr.service", found[0])
	if u, _ := m.Get("sonarr.service"); u.Cause == nil || u.Cause.ID != "oom" {
		t.Fatalf("a failed service carries its cause: %+v", u.Cause)
	}
	if u := m.Units()[0]; u.Cause == nil {
		t.Fatal("the list shows the cause too")
	}
	f.set("sonarr.service", "active", "running", 0)
	poll(t, m, t0.Add(90*time.Second))
	if u, _ := m.Get("sonarr.service"); u.Cause != nil {
		t.Fatalf("a running service has no cause: %+v", u.Cause)
	}
}

// A service restarted from the dashboard while a poll reads systemd is not announced as failed on
// the poll's older reading, then as running again on the next.
func TestARestartDuringAPollRaisesNoAlerts(t *testing.T) {
	f := newFake()
	m := units.New()
	m.Run = f.run
	t0 := time.Unix(1_800_000_000, 0)
	f.set("sonarr.service", "failed", "failed", 0, "Result", "exit-code")
	poll(t, m, t0)

	restartMidPoll := true
	m.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := f.run(ctx, name, args...)
		// The poll's reading is taken; the dashboard restarts the service before the poll compares.
		if restartMidPoll && name == "systemctl" && args[0] == "show" && len(args) > 4 {
			restartMidPoll = false
			f.set("sonarr.service", "active", "running", 0)
			if _, err := m.Refresh(ctx, "sonarr.service"); err != nil {
				t.Fatal(err)
			}
		}
		return out, err
	}
	if ev := poll(t, m, t0.Add(30*time.Second)); len(ev) != 0 {
		t.Fatalf("the poll's older reading raised: %s", kinds(ev))
	}
	if u, _ := m.Get("sonarr.service"); u.Failed() {
		t.Fatalf("the older reading replaced the newer: %+v", u)
	}
	if ev := poll(t, m, t0.Add(60*time.Second)); len(ev) != 0 {
		t.Fatalf("the next poll: %s", kinds(ev))
	}
}
