package services_test

import (
	"strings"
	"testing"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// A check failing while Nightjar's machine boots or shuts down is not held against it: no
// downtime, no alert, and it is looked at again soon. Once the machine has settled it counts.
func TestFailuresWhileSettlingAreNotCounted(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	s := spec("tcp", "h:1")
	s.Interval, s.Retries, s.RetryInterval = 300, 0, 30
	m := services.NewMonitor([]services.Spec{s}, store)
	settling := true
	m.Settling = func(float64) bool { return settling }
	fail := services.Result{Detail: "connection refused"}

	m.Claim(1000)
	if _, change, recorded := m.RecordAs("svc", fail, 1000); recorded || change != "" {
		t.Fatalf("a failure while settling was counted: %q %v", change, recorded)
	}
	if st := m.State("svc"); st.Status == services.StatusDown || st.ConsecutiveFailures != 0 {
		t.Fatalf("settling failure changed the status: %+v", st)
	}
	if u := m.Uptime("svc", 0); u != nil {
		t.Fatalf("settling failure counted against uptime: %v", *u)
	}
	if store.LastCheckTime() != 0 {
		t.Fatal("settling failure was stored")
	}
	if len(m.Claim(1029)) != 0 || len(m.Claim(1030)) != 1 {
		t.Fatal("a held failure is looked at again at the retry interval")
	}
	// A pass while settling counts as usual.
	if _, _, recorded := m.RecordAs("svc", services.Result{Up: true}, 1030); !recorded {
		t.Fatal("a pass while settling was dropped")
	}
	if store.LastCheckTime() != 1030 {
		t.Fatalf("pass not stored: %v", store.LastCheckTime())
	}
	// Settled: a failure counts again.
	settling = false
	m.Claim(1330)
	if change := m.Record("svc", fail, 1330); change != services.StatusDown {
		t.Fatalf("after settling a failure should count: %q", change)
	}
}

// A heartbeat ping sent while Nightjar was off was lost, and a job on its machine could not run:
// the run due then is not held against the job, but the next one is judged as usual.
func TestHeartbeatRunDueWhileNightjarWasOffline(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	spec, err := services.ParseSpec([]byte(`{"name":"backup","type":"heartbeat","schedule":"0 3 * * *","grace":600}`))
	if err != nil {
		t.Fatal(err)
	}
	m := services.NewMonitor([]services.Spec{spec}, store)
	m.Heartbeat(spec, at(2, 12, 0))
	m.Ping(spec.Target, "", "", at(3, 3, 1))
	// Off from 02:30 to 03:30 the next night.
	m = services.NewMonitor([]services.Spec{spec}, store)
	if got := m.Offline(at(4, 2, 30), at(4, 3, 30)); len(got) != 1 || got[0] != "backup" {
		t.Fatalf("excused %v", got)
	}
	if r := m.Heartbeat(spec, at(4, 3, 31)); !r.Up {
		t.Fatalf("the run due while Nightjar was off was held against the job: %+v", r)
	}
	// Restarted again: the excuse was kept.
	m = services.NewMonitor([]services.Spec{spec}, store)
	if r := m.Heartbeat(spec, at(4, 12, 0)); !r.Up {
		t.Fatalf("the excuse was lost on restart: %+v", r)
	}
	// The night after, it is missed for real.
	if r := m.Heartbeat(spec, at(5, 3, 11)); r.Up || !strings.Contains(r.Detail, "missed the run") {
		t.Fatalf("missed: %+v", r)
	}
	// A short stop with no run due excuses nothing.
	if got := m.Offline(at(5, 12, 0), at(5, 12, 1)); len(got) != 0 {
		t.Fatalf("excused %v with no run due", got)
	}
}

func TestEveryHeartbeatOffline(t *testing.T) {
	m, spec := heartbeat(t, "every 1h", nil)
	start := at(2, 12, 0)
	m.Heartbeat(spec, start)
	m.Ping(spec.Target, "", "", start)
	// A restart half way to the next ping excuses nothing.
	if got := m.Offline(start+1800, start+1810); len(got) != 0 {
		t.Fatalf("excused %v", got)
	}
	if r := m.Heartbeat(spec, start+3600+601); r.Up {
		t.Fatalf("late: %+v", r)
	}
	// Off across when it was due: the hour starts again from when Nightjar came back.
	if got := m.Offline(start+3500, start+4000); len(got) != 1 {
		t.Fatalf("excused %v", got)
	}
	if r := m.Heartbeat(spec, start+4000+3600); !r.Up {
		t.Fatalf("held against the job: %+v", r)
	}
	if r := m.Heartbeat(spec, start+4000+3600+601); r.Up {
		t.Fatalf("late after the excuse: %+v", r)
	}
}
