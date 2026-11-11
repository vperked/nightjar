package plugins_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/plugins"
)

// A half-made plugin is listed with its problem, and provides an empty list rather than null.
func TestBrokenPluginsProvideNothingRatherThanNull(t *testing.T) {
	m, root, _ := setup(t)
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	plugin(t, root, "notjson", `{"id":`, "")
	plugin(t, root, "nullprov", `{"id":"nullprov","provides":null,"run":"run.sh"}`, "true\n")
	m.Scan()
	for _, id := range []string{"empty", "notjson", "nullprov"} {
		v := find(m, id)
		raw, _ := json.Marshal(v)
		if v.Problem == "" || v.Provides == nil || strings.Contains(string(raw), `"provides":null`) {
			t.Errorf("%s: %s", id, raw)
		}
	}
}

// Every folder on the way to the program counts, and it is looked at again before every run.
func TestAPluginOthersCouldChangeDoesNotRun(t *testing.T) {
	m, root, _ := setup(t)
	dir := plugin(t, root, "deep", `{"id":"deep","provides":["check"],"run":"bin/run.sh"}`, "")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "run.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	os.Chmod(bin, 0o777)
	m.Scan()
	if p := find(m, "deep"); !strings.Contains(p.Problem, "bin can be changed by other users") {
		t.Fatalf("a folder others can write: %q", p.Problem)
	}

	os.Chmod(bin, 0o755)
	m.Scan()
	if p := find(m, "deep"); p.Problem != "" {
		t.Fatalf("once it is fixed: %q", p.Problem)
	}
	on(t, m, "deep", nil)
	if r := m.RunCheck(context.Background(), "deep", ""); !r.Up {
		t.Fatalf("runs: %+v", r)
	}
	// Opened up after the folder was read: refused, without waiting for another scan.
	os.Chmod(bin, 0o777)
	if r := m.RunCheck(context.Background(), "deep", ""); r.Up || !strings.Contains(r.Detail, "can be changed by other users") {
		t.Fatalf("ran a program others could have swapped: %+v", r)
	}
	os.Chmod(bin, 0o755)
	os.Chmod(root, 0o777)
	if r := m.RunCheck(context.Background(), "deep", ""); r.Up {
		t.Fatalf("ran from a plugins folder others could swap plugins in: %+v", r)
	}
	os.Chmod(root, 0o755)
}

func TestFindingANewAlertPluginIsReported(t *testing.T) {
	m, root, _ := setup(t)
	plugin(t, root, "card", `{"id":"card","provides":["card"],"run":"run.sh"}`, "true\n")
	m.Scan()
	changes := 0
	m.NotifyChanged = func() { changes++ }
	m.Scan()
	plugin(t, root, "other-card", `{"id":"other-card","provides":["card"],"run":"run.sh"}`, "true\n")
	m.Scan()
	if changes != 0 {
		t.Fatalf("no plugin that delivers alerts changed, yet %d changes", changes)
	}
	plugin(t, root, "log", `{"id":"log","provides":["notify"],"run":"run.sh"}`, "true\n")
	m.Scan()
	m.Scan()
	if changes != 1 {
		t.Fatalf("one new alert plugin, %d changes", changes)
	}
	plugin(t, root, "log", `{"id":"log","name":"Log file","provides":["notify"],"run":"run.sh"}`, "true\n")
	m.Scan()
	os.RemoveAll(filepath.Join(root, "log"))
	m.Scan()
	if changes != 3 {
		t.Fatalf("renamed, then removed: %d changes", changes)
	}
}

// failingStore cannot read what it keeps until fixed.
type failingStore struct {
	memStore
	broken bool
}

func (f *failingStore) MetaValue(k string) (string, error) {
	if f.broken {
		return "", errors.New("database is locked")
	}
	return f.memStore[k], nil
}

func TestChoicesThatCannotBeReadAreNotSavedOver(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0o755)
	plugin(t, root, "a", `{"id":"a","provides":["check"],"run":"run.sh"}`, "true\n")
	plugin(t, root, "b", `{"id":"b","provides":["check"],"run":"run.sh"}`, "true\n")
	store := &failingStore{memStore: memStore{"plugins.state": `{"a":{"enabled":true}}`}, broken: true}
	m := plugins.New(root, store)
	m.Scan()
	yes := true
	if err := m.Configure("b", plugins.Change{Enabled: &yes}); err == nil {
		t.Fatal("saved over choices it could not read")
	}
	if store.memStore["plugins.state"] != `{"a":{"enabled":true}}` {
		t.Fatalf("saved: %s", store.memStore["plugins.state"])
	}
	store.broken = false
	if err := m.Configure("b", plugins.Change{Enabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled("a") || !m.Enabled("b") {
		t.Fatal("both are on once the choices can be read")
	}
}

// A plugins folder anywhere under a folder others can write to is refused, however far up: whoever can
// write there can swap the whole tree for their own (audit #95).
func TestPluginUnderWritableAncestorIsRefused(t *testing.T) {
	outer := t.TempDir()
	open := filepath.Join(outer, "shared")
	root := filepath.Join(open, "deep", "plugins")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(outer, 0o755)
	os.Chmod(open, 0o775) // group-writable, not sticky, two levels above the plugins folder
	plugin(t, root, "far", `{"name":"Far","provides":["check"],"run":"run.sh"}`, `echo '{"up":true}'`)
	m := plugins.New(root, memStore{})
	m.Scan()
	if p := find(m, "far"); !strings.Contains(p.Problem, "shared can be changed by other users") {
		t.Fatalf("problem %q, want the writable ancestor named", p.Problem)
	}
	// Made safe, it runs; the check is asked again rather than remembered.
	os.Chmod(open, 0o755)
	m.Scan()
	if p := find(m, "far"); p.Problem != "" {
		t.Fatalf("after chmod: problem %q", p.Problem)
	}
}

// Choices that could not be read when Nightjar started are read once they can be, without the
// owner having to change anything; until then every plugin is off.
func TestChoicesAreReadOnceTheyCanBe(t *testing.T) {
	defer func(old time.Duration) { plugins.StateRetry = old }(plugins.StateRetry)
	plugins.StateRetry = 0
	root := t.TempDir()
	os.Chmod(root, 0o755)
	plugin(t, root, "a", `{"id":"a","provides":["check"],"run":"run.sh"}`, "true\n")
	store := &failingStore{memStore: memStore{"plugins.state": `{"a":{"enabled":true}}`}, broken: true}
	m := plugins.New(root, store)
	m.Scan()
	if m.Enabled("a") {
		t.Fatal("on without its choices")
	}
	store.broken = false
	if !m.Enabled("a") {
		t.Fatal("still off once its choices can be read")
	}
}
