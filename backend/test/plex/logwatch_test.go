package plex_test

import (
	"os"
	"path/filepath"
	"testing"

	"nightjar/internal/plex"
)

const missingLine = "Nov 25, 2026 01:57:55.109 [137527309515576] ERROR - Couldn't find the file to stream: /home/media/tv/Carrie/Season 01/Carrie - S01E07.mkv\n"

func plexDir(t *testing.T) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "Logs", "Plex Media Server.log")
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// What was logged before Nightjar started is old news: only what follows is reported.
func TestLogWatcherReportsOnlyWhatIsLoggedAfterItStarts(t *testing.T) {
	dir, log := plexDir(t)
	appendTo(t, log, missingLine)
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	if f := w.Poll(); !f.Empty() {
		t.Fatalf("the first poll reported old lines: %+v", f)
	}
	if f := w.Poll(); !f.Empty() {
		t.Fatalf("nothing new was logged: %+v", f)
	}
	appendTo(t, log, missingLine+missingLine+
		"Nov 25, 2026 01:58:00.000 [1] ERROR - Couldn't find the file to stream: /home/media/tv/Carrie/Season 01/Carrie - S01E08.mkv\n")
	f := w.Poll()
	if f.MissingCount != 3 || len(f.MissingFiles) != 2 {
		t.Fatalf("want 3 attempts at 2 files, got %+v", f)
	}
	if f.MissingFiles[0] != "/home/media/tv/Carrie/Season 01/Carrie - S01E07.mkv" {
		t.Fatalf("path = %q", f.MissingFiles[0])
	}
}

// Plex's routine ERROR lines (client profiles, network chatter) are not failures.
func TestLogWatcherIgnoresPlexsRoutineErrors(t *testing.T) {
	dir, log := plexDir(t)
	appendTo(t, log, "")
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	w.Poll()
	appendTo(t, log, "Nov 25, 2026 01:57:54.870 [1] ERROR - [Req#13a87f/Transcode] ClientProfileExtra: missing or invalid type parameter\n"+
		"Nov 25, 2026 01:57:58.930 [1] ERROR - [Req#1/Transcode/a/b] Device creation failed: -1313558101.\n"+
		"Nov 25, 2026 01:57:58.930 [1] DEBUG - Couldn't find the file to stream: /not/an/error.mkv\n")
	if f := w.Poll(); !f.Empty() {
		t.Fatalf("routine lines reported: %+v", f)
	}
}

// A line Plex is halfway through writing is read once it is finished.
func TestLogWatcherWaitsForAWholeLine(t *testing.T) {
	dir, log := plexDir(t)
	appendTo(t, log, "")
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	w.Poll()
	appendTo(t, log, missingLine[:40])
	if f := w.Poll(); !f.Empty() {
		t.Fatalf("half a line reported: %+v", f)
	}
	appendTo(t, log, missingLine[40:])
	if f := w.Poll(); f.MissingCount != 1 {
		t.Fatalf("the finished line was missed: %+v", f)
	}
}

// Plex rotates by renaming the log to .1.log: what was added to the old file before the rename is
// still read, then the new file from its start.
func TestLogWatcherFollowsRotation(t *testing.T) {
	dir, log := plexDir(t)
	appendTo(t, log, "start\n")
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	w.Poll()
	appendTo(t, log, missingLine)
	if err := os.Rename(log, filepath.Join(dir, "Logs", "Plex Media Server.1.log")); err != nil {
		t.Fatal(err)
	}
	appendTo(t, log, missingLine)
	if f := w.Poll(); f.MissingCount != 2 {
		t.Fatalf("want the old file's last line and the new file's, got %+v", f)
	}
	appendTo(t, log, missingLine)
	if f := w.Poll(); f.MissingCount != 1 {
		t.Fatalf("the new file is not followed: %+v", f)
	}
}

func TestLogWatcherReportsADamagedDatabase(t *testing.T) {
	dir, log := plexDir(t)
	appendTo(t, log, "")
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	w.Poll()
	appendTo(t, log, "Nov 25, 2026 03:00:00.000 [1] ERROR - SQLITE3:0x80000001, 11, database corruption at line 1 of [abc]: database disk image is malformed\n")
	f := w.Poll()
	if len(f.Database) != 1 || f.Database[0] != "SQLITE3:0x80000001, 11, database corruption at line 1 of [abc]: database disk image is malformed" {
		t.Fatalf("database = %q", f.Database)
	}
}

// Dumps already there are old crashes; a new one is a crash.
func TestLogWatcherReportsNewCrashDumps(t *testing.T) {
	dir, _ := plexDir(t)
	server := filepath.Join(dir, "Crash Reports", "1.43.5.11079-b3b13e506", "PLEX MEDIA SERVER")
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(server, "old.dmp")
	os.WriteFile(old, []byte("x"), 0o644)
	w := plex.NewLogWatcher(dir)
	defer w.Close()
	if f := w.Poll(); !f.Empty() {
		t.Fatalf("an old dump was reported: %+v", f)
	}
	os.WriteFile(filepath.Join(server, "new.log"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(server, "new.dmp"), []byte("x"), 0o644)
	f := w.Poll()
	if len(f.Crashes) != 1 || f.Crashes[0].Process != "PLEX MEDIA SERVER" || f.Crashes[0].Version != "1.43.5.11079-b3b13e506" {
		t.Fatalf("crashes = %+v", f.Crashes)
	}
	if f := w.Poll(); len(f.Crashes) != 0 {
		t.Fatalf("a crash was reported twice: %+v", f.Crashes)
	}
}

// No Plex here (another machine's Plex, or a folder not yet created): nothing to report, no error.
func TestLogWatcherWithoutAPlexFolder(t *testing.T) {
	w := plex.NewLogWatcher(filepath.Join(t.TempDir(), "nope"))
	defer w.Close()
	for range 2 {
		if f := w.Poll(); !f.Empty() {
			t.Fatalf("reported %+v", f)
		}
	}
}

// A missing file in a folder that is also gone points at the drive, not the file.
func TestFolderState(t *testing.T) {
	dir := t.TempDir()
	if s := plex.FolderState(filepath.Join(dir, "gone.mkv")); s != "" {
		t.Fatalf("readable folder = %q", s)
	}
	if s := plex.FolderState(filepath.Join(dir, "nope", "gone.mkv")); s != "missing" {
		t.Fatalf("missing folder = %q", s)
	}
}
