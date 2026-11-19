package updates_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/updates"
)

// The agent keeps its targets in a file: it holds what the manager stores, across restarts, readable
// by root alone, and is only ever replaced whole.
func TestTheFileStoreKeepsTargetsLikeTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "updates.json")
	store, err := updates.OpenFileStore(path)
	if err != nil || len(store.UpdateTargets()) != 0 || !store.Available() {
		t.Fatalf("a missing file is an empty store: %v", err)
	}
	host := &fakeHost{version: "1.0.0", next: "1.1.0", active: true}
	gh := github(t, "v1.1.0")
	detected := []updates.Spec{app, {ID: "os", Name: "OS packages", Kind: updates.KindSystem}}
	m := updates.New(updates.Options{Store: store, Run: host.run, GitHubAPI: gh.URL, Poll: time.Millisecond}, func() []updates.Spec { return detected }, false)
	if st := m.Statuses(); len(st) != 2 || st[0].ID != "app" || st[1].ID != "os" {
		t.Fatalf("the first start keeps what was found, in order: %+v", st)
	}
	m.Check("app")
	st := settle(t, m, "app", func(s updates.Status) bool { return s.Available })
	if st.Installed != "1.0.0" || st.Latest != "v1.1.0" {
		t.Fatalf("check: %+v", st)
	}
	paused := app
	paused.Paused = true
	if _, err := m.Save("app", paused); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("os"); err != nil {
		t.Fatal(err)
	}
	m.Wait(time.Second)

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file is root's alone: %v %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("no temporary files are left behind: %v", entries)
	}

	// After a restart: the same targets and state, and no second detection.
	again, err := updates.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	m2 := updates.New(updates.Options{Store: again, Run: host.run, GitHubAPI: gh.URL}, func() []updates.Spec {
		t.Error("detection runs on the first start only")
		return nil
	}, false)
	got := m2.Statuses()
	if len(got) != 1 || got[0].ID != "app" || !got[0].Paused || got[0].Latest != "v1.1.0" || !got[0].Available {
		t.Fatalf("after a restart: %+v", got)
	}
	m2.Wait(time.Second)
}

func TestTheFileStoreKeepsSettingsAndSurvivesABadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "updates.json")
	store, _ := updates.OpenFileStore(path)
	if store.Settings() != nil {
		t.Fatal("no settings saved yet")
	}
	if err := store.SaveSettings(json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	store.SetMeta("k", "v")
	again, _ := updates.OpenFileStore(path)
	var settings map[string]bool
	if json.Unmarshal(again.Settings(), &settings) != nil || len(settings) != 1 || settings["enabled"] || again.Meta("k") != "v" {
		t.Fatalf("settings and values kept: %s %q", again.Settings(), again.Meta("k"))
	}

	// A write that fails changes nothing, here or on disk.
	gone := filepath.Join(dir, "gone", "updates.json")
	broken, _ := updates.OpenFileStore(gone)
	if err := broken.SaveUpdateTarget("x", []byte(`{}`)); err == nil || len(broken.UpdateTargets()) != 0 {
		t.Fatalf("a failed write: %v %v", err, broken.UpdateTargets())
	}

	// A file that cannot be read is moved aside, and the store starts empty rather than not at all.
	os.WriteFile(path, []byte("{not json"), 0o600)
	fresh, err := updates.OpenFileStore(path)
	if err == nil || !strings.Contains(err.Error(), ".bad") || len(fresh.UpdateTargets()) != 0 {
		t.Fatalf("a bad file: %v", err)
	}
	if b, _ := os.ReadFile(path + ".bad"); string(b) != "{not json" {
		t.Fatalf("the bad file is kept aside: %q", b)
	}
	if err := fresh.SaveUpdateTarget("x", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

// An agent's window is set to the minute.
func TestMinuteWindows(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 3, 10, h, m, 0, 0, time.Local) }
	w := updates.MinuteWindow(4*60+30, 6*60)
	if w.Contains(at(4, 29)) || !w.Contains(at(4, 30)) || !w.Contains(at(5, 59)) || w.Contains(at(6, 0)) {
		t.Fatal("04:30–06:00 contains 04:30 to 05:59")
	}
	if w.String() != "04:30–06:00" {
		t.Fatalf("label: %s", w)
	}
	over := updates.MinuteWindow(23*60+15, 45)
	if !over.Contains(at(23, 20)) || !over.Contains(at(0, 30)) || over.Contains(at(0, 45)) || over.Contains(at(12, 0)) {
		t.Fatal("23:15–00:45 runs over midnight")
	}
	if got := over.Opened(at(0, 30)); got.Day() != 9 || got.Hour() != 23 || got.Minute() != 15 {
		t.Fatalf("opened the evening before at 23:15, got %v", got)
	}
	if !updates.MinuteWindow(90, 90).Contains(at(15, 0)) || updates.MinuteWindow(90, 90).String() != "any time" {
		t.Fatal("equal start and end means any time")
	}
	// The dashboard's own, in hours, reads as before.
	if (updates.Window{Start: 4, End: 6}).String() != "04:00–06:00" {
		t.Fatal("hour windows unchanged")
	}
}
