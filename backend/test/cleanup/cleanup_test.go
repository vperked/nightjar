package cleanup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"servermonitor/internal/cleanup"
)

func TestRotatedLogNames(t *testing.T) {
	for _, n := range []string{"syslog.1", "kern.log.2.gz", "daemon.log.old", "syslog-20240115", "messages.gz", "x.log.3.zst"} {
		if !cleanup.IsRotatedLog(n) {
			t.Errorf("%s should be rotated", n)
		}
	}
	for _, n := range []string{"syslog", "kern.log", "mysql-bin.000123", "auth.log"} {
		if cleanup.IsRotatedLog(n) {
			t.Errorf("%s should not be rotated", n)
		}
	}
}

func newMonitor(t *testing.T, thresholdBytes int64) (*cleanup.LogMonitor, string) {
	dir := t.TempDir()
	m := cleanup.NewLogMonitor(filepath.Join(dir, "app.log"), 1, 7)
	m.ThresholdBytes, m.LogDir, m.JournalPath = thresholdBytes, dir, ""
	return m, dir
}

func TestTrimKeepsMostRecentDataAndInode(t *testing.T) {
	m, dir := newMonitor(t, 1000)
	path := filepath.Join(dir, "big.log")
	data := append(bytes.Repeat([]byte("old\n"), 1000), bytes.Repeat([]byte("new\n"), 250)...)
	os.WriteFile(path, data, 0o644)
	before, _ := os.Stat(path)
	info, _ := m.Size(path)
	if !m.Clean(info, false) {
		t.Fatal("clean failed")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data[len(data)-1000:]) {
		t.Fatalf("kept %d bytes, wrong tail", len(got))
	}
	after, _ := os.Stat(path)
	if before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("trim must preserve the inode")
	}
}

func TestForceEmptiesButProtectedLogsAreOnlyTrimmed(t *testing.T) {
	m, dir := newMonitor(t, 10)
	plain := filepath.Join(dir, "app.log")
	auth := filepath.Join(dir, "auth.log")
	for _, p := range []string{plain, auth} {
		os.WriteFile(p, bytes.Repeat([]byte("x"), 100), 0o644)
		info, _ := m.Size(p)
		m.Clean(info, true)
	}
	if st, _ := os.Stat(plain); st.Size() != 0 {
		t.Fatalf("forced clean should empty, size %d", st.Size())
	}
	if st, _ := os.Stat(auth); st.Size() != 10 {
		t.Fatalf("auth.log should be trimmed to threshold, size %d", st.Size())
	}
}

func TestBelowThresholdIsUntouched(t *testing.T) {
	m, dir := newMonitor(t, 1000)
	p := filepath.Join(dir, "small.log")
	os.WriteFile(p, []byte("hi"), 0o644)
	info, _ := m.Size(p)
	if !m.Clean(info, false) {
		t.Fatal("should report success")
	}
	if got, _ := os.ReadFile(p); string(got) != "hi" {
		t.Fatal("small log changed")
	}
}

func TestRotatedCleanupHonoursAgeSizeAndForce(t *testing.T) {
	m, dir := newMonitor(t, 1000)
	old := filepath.Join(dir, "syslog.1")
	recent := filepath.Join(dir, "syslog.2.gz")
	live := filepath.Join(dir, "syslog")
	for _, p := range []string{old, recent, live} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	past := time.Now().Add(-10 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	st := m.CleanupRotated(false)
	if st.FilesRemoved != 1 {
		t.Fatalf("removed %d", st.FilesRemoved)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent small archive should be kept")
	}
	m.CleanupRotated(true)
	if _, err := os.Stat(recent); !os.IsNotExist(err) {
		t.Fatal("force should remove every archive")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("live log must not be touched")
	}
}

func TestCleanTreeRemovesOnlyStaleAndSkipsProtected(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "stale.tmp")
	fresh := filepath.Join(root, "fresh.tmp")
	protected := filepath.Join(root, "systemd-private-abc", "file")
	os.WriteFile(stale, []byte("x"), 0o644)
	os.WriteFile(fresh, []byte("x"), 0o644)
	os.MkdirAll(filepath.Dir(protected), 0o755)
	os.WriteFile(protected, []byte("x"), 0o644)
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{stale, protected} {
		os.Chtimes(p, past, past)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "keep")
	os.WriteFile(target, []byte("x"), 0o644)
	link := filepath.Join(root, "link")
	os.Symlink(target, link)
	unix.Lutimes(link, []unix.Timeval{unix.NsecToTimeval(past.UnixNano()), unix.NsecToTimeval(past.UnixNano())})

	var st cleanup.Stats
	cleanup.CleanTree(root, 24*time.Hour, &st, time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale file should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh file should be kept")
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatal("protected directory must not be cleaned")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("stale symlink should be removed")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("symlink target outside the root must survive")
	}
}

func TestResolveDirsEnforcesAllowlist(t *testing.T) {
	c := &cleanup.Cleaner{}
	got := c.ResolveDirs([]string{"/tmp", "/home", "/tmp/../etc", "  "}, tempBases)
	if len(got) != 1 || got[0] != "/tmp" {
		t.Fatalf("got %v", got)
	}
	c.Opts.AllowUnsafePaths = true
	if got := c.ResolveDirs([]string{"/etc"}, tempBases); len(got) != 1 {
		t.Fatalf("unsafe override should allow, got %v", got)
	}
}

var tempBases = []string{"/tmp", "/var/tmp"}
