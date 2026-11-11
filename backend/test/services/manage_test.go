package services_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

func parseSpec(t *testing.T, json string) services.Spec {
	t.Helper()
	s, err := services.ParseSpec([]byte(json))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChecksAreManagedLive(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)

	if err := m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1"}`)); err != nil {
		t.Fatal(err)
	}
	if due := m.Claim(1000); len(due) != 1 || due[0].Retries != 2 || due[0].Interval != 60 {
		t.Fatalf("a new check runs on the next cycle with defaults filled in: %+v", due)
	}
	m.Record("api", services.Result{Detail: "refused"}, 1000)
	if err := m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"x:1"}`)); !errors.Is(err, services.ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}

	// Rename and pause: history follows the new name, and a paused check neither runs nor records.
	if err := m.Save("api", parseSpec(t, `{"name":"db","type":"tcp","target":"127.0.0.1:1","paused":true}`)); err != nil {
		t.Fatal(err)
	}
	if m.Has("api") || len(store.Buckets("db", 0, 2000, 10)) != 1 {
		t.Fatal("rename should move the check and its stored history")
	}
	if len(m.Claim(5000)) != 0 || m.Record("db", services.Result{}, 5000) != "" || m.Summaries(5000)[0].Status != services.StatusPaused {
		t.Fatal("a paused check should not run, record or show a status other than paused")
	}

	// What was saved survives a restart.
	again := services.NewMonitor(services.Stored(store, "", func(msg string) { t.Log(msg) }), store)
	if got := again.Specs(); len(got) != 1 || got[0].Name != "db" || !got[0].Paused {
		t.Fatalf("stored checks %+v", got)
	}

	if err := m.Delete("db"); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("db"); !errors.Is(err, services.ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	if len(store.Monitors()) != 0 || len(store.Buckets("db", 0, 2000, 10)) != 0 {
		t.Fatal("delete removes the check and its history")
	}
}

func TestServicesFileIsImportedOnce(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	file := filepath.Join(t.TempDir(), "services.json")
	os.WriteFile(file, []byte(`[{"name":"web","target":"https://example.com"},{"name":"bad"}]`), 0o644)

	if got := services.Stored(store, file, func(string) {}); len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("import: %+v", got)
	}
	os.WriteFile(file, []byte(`[{"name":"other","target":"https://example.com"}]`), 0o644)
	if got := services.Stored(store, file, func(string) {}); len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("once checks are stored the file is ignored: %+v", got)
	}
	if err := services.NewMonitor(nil, nil).Save("", services.Spec{Name: "x"}); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("without a database, changes are refused rather than lost: %v", err)
	}
}

func TestEditingRunsTheCheckAgainButKeepsLastCheck(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Record("api", services.Result{Up: true}, 1000)
	if len(m.Claim(1010)) != 0 {
		t.Fatal("not due inside its interval")
	}
	m.Save("api", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","interval":60,"retries":5}`))
	if len(m.Claim(1010)) != 1 || m.State("api").LastCheck != 1000 {
		t.Fatal("an edited check runs on the next cycle and still shows when it last ran")
	}
}

// A check renamed while it runs reports under its old name: the result lands on the renamed
// check, which then runs again (it stayed marked running for good, never checked again).
func TestRenamingARunningCheck(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	if len(m.Claim(1000)) != 1 {
		t.Fatal("a new check runs")
	}
	if err := m.Save("api", parseSpec(t, `{"name":"api-prod","type":"tcp","target":"127.0.0.1:1","interval":60}`)); err != nil {
		t.Fatal(err)
	}
	// Meanwhile a new check takes the old name; it has not run, so the result is not its.
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:2","interval":60}`))
	// It says where the result went, for its alert to use the new name.
	if name, _, recorded := m.RecordAs("api", services.Result{Up: true}, 1005); name != "api-prod" || !recorded {
		t.Fatalf("recorded as %q", name)
	}
	if got := m.State("api-prod"); got.LastCheck != 1005 || got.Status != services.StatusUp {
		t.Fatalf("the result goes to the renamed check: %+v", got)
	}
	if got := m.State("api"); got.LastCheck != 0 {
		t.Fatalf("not to the new one: %+v", got)
	}
	due := m.Claim(1100)
	if len(due) != 2 {
		t.Fatalf("both run again: %+v", due)
	}
}

// Changing a running check's target drops the old target's result and does not start the new
// one beside it; it runs as soon as the old run is done.
func TestRetargetingARunningCheck(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Claim(1000)
	m.Save("api", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:2","interval":60}`))
	if len(m.Claim(1001)) != 0 {
		t.Fatal("the old run is still under way")
	}
	if got := m.Record("api", services.Result{Detail: "refused"}, 1002); got != "" || m.State("api").LastResult != nil || m.State("api").Status != services.StatusUnknown {
		t.Fatalf("the old target's result is dropped: %q %+v", got, m.State("api"))
	}
	if due := m.Claim(1003); len(due) != 1 || due[0].Target != "127.0.0.1:2" {
		t.Fatalf("the new target runs at once: %+v", due)
	}
}

// A check renamed twice while it runs still reports under its first name, and leaves nothing
// behind that would send a later check's result to it.
func TestRenamingARunningCheckTwice(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"a","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Claim(1000)
	m.Save("a", parseSpec(t, `{"name":"b","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Save("b", parseSpec(t, `{"name":"c","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Record("a", services.Result{Up: true}, 1005)
	if got := m.State("c"); got.LastCheck != 1005 {
		t.Fatalf("the result goes to the check's latest name: %+v", got)
	}
	// A new check named b, deleted while it runs: its result is dropped, not given to c.
	m.Save("", parseSpec(t, `{"name":"b","type":"tcp","target":"127.0.0.1:2","interval":60}`))
	if due := m.Claim(1010); len(due) != 1 || due[0].Name != "b" {
		t.Fatalf("only the new check is due: %+v", due)
	}
	m.Delete("b")
	m.Record("b", services.Result{Detail: "refused"}, 1012)
	if got := m.State("c"); got.LastCheck != 1005 || got.Status != services.StatusUp {
		t.Fatalf("c keeps its own result: %+v", got)
	}
}

// A check renamed and renamed back while it runs reports under its name as usual.
func TestRenamingARunningCheckBack(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"a","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Claim(1000)
	m.Save("a", parseSpec(t, `{"name":"b","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Save("b", parseSpec(t, `{"name":"a","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Record("a", services.Result{Up: true}, 1005)
	if got := m.State("a"); got.LastCheck != 1005 {
		t.Fatalf("the result lands on the check: %+v", got)
	}
	// A new check named b, deleted while it runs, does not pass its result to a.
	m.Save("", parseSpec(t, `{"name":"b","type":"tcp","target":"127.0.0.1:2","interval":60}`))
	m.Claim(1010)
	m.Delete("b")
	m.Record("b", services.Result{Detail: "refused"}, 1012)
	if got := m.State("a"); got.LastCheck != 1005 || got.Status != services.StatusUp {
		t.Fatalf("a keeps its own result: %+v", got)
	}
}
