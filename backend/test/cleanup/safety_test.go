package cleanup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"nightjar/internal/cleanup"
)

func TestOnlyLogNamesCountAsRotated(t *testing.T) {
	for _, n := range []string{"dmesg.0", "log.smbd.old", "auth.log.2.gz", "Xorg.0.log.old"} {
		if !cleanup.IsRotatedLog(n) {
			t.Errorf("%s should be rotated", n)
		}
	}
	// A library, a kernel module and Samba's live per-client log end the way an archive does.
	for _, n := range []string{"libc.so.6", "nvme.ko.zst", "log.192.168.1.10"} {
		if cleanup.IsRotatedLog(n) {
			t.Errorf("%s should not be rotated", n)
		}
	}
}

func TestSecurityArchivesAreNeverDeleted(t *testing.T) {
	m, dir := newMonitor(t, 1000)
	os.MkdirAll(filepath.Join(dir, "audit"), 0o755)
	// auth.log-20240115.gz is logrotate dateext plus compress: two suffixes stacked.
	kept := []string{"auth.log.1", "auth.log.2.gz", "auth.log-20240115.gz", "btmp.1", "wtmp.1", "secure-20240115", filepath.Join("audit", "audit.log.1")}
	gone := filepath.Join(dir, "syslog.1")
	past := time.Now().Add(-30 * 24 * time.Hour)
	for _, n := range append(kept, "syslog.1") {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, past, past)
	}
	m.CleanupRotated(true)
	for _, n := range kept {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must be kept", n)
		}
		info, _ := m.Size(p)
		m.Clean(info, true)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive a forced clean", n)
		}
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("an ordinary archive is still removed")
	}
}

func TestADirectorySwappedForASymlinkCannotRedirectCleanup(t *testing.T) {
	m, dir := newMonitor(t, 10)
	outside := t.TempDir()
	victim := filepath.Join(outside, "libc.log.6")
	big := filepath.Join(outside, "important.log")
	os.WriteFile(victim, []byte("x"), 0o644)
	os.WriteFile(big, bytes.Repeat([]byte("y"), 100), 0o644)
	past := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(victim, past, past)
	// What was a directory under the log dir when it was listed is now a link out of it.
	os.Symlink(outside, filepath.Join(dir, "x"))

	m.Clean(cleanup.LogInfo{Path: filepath.Join(dir, "x", "libc.log.6"), Size: 1, ModTime: past}, true)
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a file outside the log dir was deleted")
	}
	m.Clean(cleanup.LogInfo{Path: filepath.Join(dir, "x", "important.log"), Size: 100}, true)
	if st, _ := os.Stat(big); st.Size() != 100 {
		t.Fatal("a file outside the log dir was truncated")
	}
}

func TestCleanupLeavesOtherFilesystemsAlone(t *testing.T) {
	logDir, tmp := t.TempDir(), t.TempDir()
	shm, err1 := os.Stat("/dev/shm")
	here, err2 := os.Stat(logDir)
	if err1 != nil || err2 != nil || shm.Sys().(*syscall.Stat_t).Dev == here.Sys().(*syscall.Stat_t).Dev {
		t.Skip("needs /dev/shm on its own filesystem")
	}
	logs := cleanup.NewLogMonitor(filepath.Join(logDir, "app.log"), 1, 7)
	logs.LogDir, logs.JournalPath, logs.SystemLogs = logDir, "", nil
	c := &cleanup.Cleaner{Logs: logs, Opts: cleanup.Options{AllowUnsafePaths: true, AggressiveThreshold: 0,
		TempDirs: []string{tmp}, CacheDirs: []string{tmp}, EmergencyCacheDirs: []string{tmp}}}
	app := filepath.Join(logDir, "app.log")
	stale := filepath.Join(tmp, "stale")
	write := func() {
		os.WriteFile(app, bytes.Repeat([]byte("x"), 100), 0o644)
		os.WriteFile(stale, []byte("x"), 0o644)
		past := time.Now().Add(-48 * time.Hour)
		os.Chtimes(stale, past, past)
	}
	write()

	// A full disk elsewhere: emptying these logs would not free a byte there.
	if st := c.EmergencyClean("/dev/shm"); st.BytesCleared != 0 || st.LogsCleaned != 0 {
		t.Fatalf("cleaned for another filesystem: %+v", st)
	}
	if st, _ := os.Stat(app); st.Size() != 100 {
		t.Fatal("a log was emptied for another filesystem")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a temp file was removed for another filesystem")
	}

	// The filesystem they are on is cleaned as before.
	c.EmergencyClean(logDir)
	if st, _ := os.Stat(app); st.Size() != 0 {
		t.Fatal("the log on the full filesystem should be emptied")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the stale temp file on the full filesystem should be removed")
	}
}

func TestDirSizeAndLargestSubdirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "big", "deep"), 0o755)
	os.MkdirAll(filepath.Join(root, "small"), 0o755)
	os.WriteFile(filepath.Join(root, "big", "deep", "a"), make([]byte, 3000), 0o644)
	os.WriteFile(filepath.Join(root, "small", "b"), make([]byte, 10), 0o644)
	if size, partial := cleanup.DirSize(root, 5*time.Second); size != 3010 || partial {
		t.Fatalf("size %d partial %v", size, partial)
	}
	got := cleanup.LargestSubdirs(root, 5, 5*time.Second)
	if len(got) != 2 || got[0].Path != filepath.Join(root, "big") || got[0].Size != 3000 || got[1].Size != 10 {
		t.Fatalf("got %+v", got)
	}
}

// An *arr import hardlinks the seeding download into the library: both names are one file on
// disk, so the folder is as big as du says, not twice that.
func TestDirSizeCountsHardlinksOnce(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "downloads"), 0o755)
	os.MkdirAll(filepath.Join(root, "movies"), 0o755)
	src := filepath.Join(root, "downloads", "film.mkv")
	os.WriteFile(src, make([]byte, 5000), 0o644)
	if err := os.Link(src, filepath.Join(root, "movies", "film.mkv")); err != nil {
		t.Skip("no hardlinks here:", err)
	}
	os.WriteFile(filepath.Join(root, "movies", "other.mkv"), make([]byte, 100), 0o644)
	if size, partial := cleanup.DirSize(root, 5*time.Second); size != 5100 || partial {
		t.Fatalf("size %d partial %v, want 5100", size, partial)
	}
}

// openSUSE's btrfs layout: / and /var are subvolumes of one pool, each with its own device number,
// so a full / is helped by cleaning /var/log. A tmpfs and a second disk are apart.
func TestBtrfsSubvolumesCountAsOneFilesystem(t *testing.T) {
	mountinfo := `22 1 0:31 /@/.snapshots/1/snapshot / rw,relatime shared:1 - btrfs /dev/sda2 rw,subvol=/@/.snapshots/1/snapshot
40 22 0:42 /@/var /var rw,relatime shared:20 - btrfs /dev/sda2 rw,subvol=/@/var
41 22 0:43 /@/home /home rw,relatime shared:21 - btrfs /dev/sda2 rw,subvol=/@/home
42 22 0:44 / /tmp rw,nosuid,nodev shared:22 - tmpfs tmpfs rw
43 22 0:45 / /run rw,nosuid,nodev shared:23 - tmpfs tmpfs rw
44 22 8:17 / /srv/data rw,relatime shared:24 - ext4 /dev/sdb1 rw
45 44 0:46 / /srv/data/scratch rw shared:25 - tmpfs tmpfs rw`
	cases := []struct {
		a, b string
		same bool
	}{
		{"/", "/var/log", true},
		{"/", "/var/cache/zypp", true},
		{"/home", "/var/tmp", true},
		{"/var", "/var/log", true},
		{"/", "/tmp", false},           // tmpfs: left to the device number
		{"/tmp", "/run", false},        // two tmpfs are never one block device
		{"/", "/srv/data/logs", false}, // another disk
		{"/srv/data", "/srv/data/x", true},
		{"/srv/data", "/srv/data/scratch/x", false},
		{"/", "/variable", true}, // on /, not /var, but /'s pool all the same
	}
	for _, c := range cases {
		if got := cleanup.SameBlockDevice(mountinfo, c.a, c.b); got != c.same {
			t.Errorf("SameBlockDevice(%s, %s) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}
