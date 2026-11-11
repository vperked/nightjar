package cleanup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// lchtimes backdates a path without following it when it is a symlink.
func lchtimes(t *testing.T, path string, when time.Time) {
	t.Helper()
	ts := []unix.Timespec{unix.NsecToTimespec(when.UnixNano()), unix.NsecToTimespec(when.UnixNano())}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatalf("backdating %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Rotation suffixes stack (dateext, then compress); every one must come off, but a name that is
// nothing but a suffix must not be stripped to nothing.
func TestRotatedBaseStripsStackedSuffixes(t *testing.T) {
	cases := map[string]string{
		"kern.log.2.gz":          "kern.log",
		"auth.log-20240115.gz":   "auth.log",
		"auth.log-20240115":      "auth.log",
		"syslog.1":               "syslog",
		"daemon.log.old":         "daemon.log",
		"messages.3.zst":         "messages",
		"nginx/access.log.14.xz": "nginx/access.log",
		"wtmp.1":                 "wtmp",
		"syslog":                 "syslog",
		".gz":                    ".gz", // stripping would leave "", so the name is kept
		"1":                      "1",
	}
	for in, want := range cases {
		if got := rotatedBase(in); got != want {
			t.Errorf("rotatedBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// Deciding what counts as a rotated log decides what gets deleted under /var/log: libraries, kernel
// modules, database binlogs and Samba's live per-client logs must never match.
func TestRotatedLogClassification(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"syslog.1", true},
		{"kern.log.2.gz", true},
		{"daemon.log.old", true},
		{"syslog-20240115", true},
		{"auth.log-20240115.gz", true}, // rotated, though protected (checked separately)
		{"dmesg.0", true},
		{"mail.err.1", true},
		{"nginx-access.log.10.bz2", true},
		{"log.smbd.old", true},
		{"log.192.168.1.10.old", true}, // a Samba client log that was rotated
		// Never rotated logs:
		{"syslog", false},
		{"kern.log", false},
		{"log.192.168.1.10", false}, // Samba's live per-client log: .10 is an octet, not a rotation
		{"10.0.0.1", false},
		{"libc.so.6", false},
		{"libssl.so.3", false},
		{"liblog.so.1", false}, // has "log" in it, but is a library
		{"ext4.ko.zst", false},
		{"mysql-bin.000123", false},
		{"mariadb-relay-bin.000002", false},
		{"binlog.000042", false},
		{"photo.jpg.1", false},
		{"archive.tar.gz", false},
	}
	for _, c := range cases {
		if got := IsRotatedLog("/var/log/" + c.name); got != c.want {
			t.Errorf("IsRotatedLog(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	if !ipSuffixRE.MatchString("log.192.168.1.10") || !ipSuffixRE.MatchString("192.168.1.10") {
		t.Error("ipSuffixRE should match a trailing IPv4 address")
	}
	if ipSuffixRE.MatchString("syslog.1") || ipSuffixRE.MatchString("log.1.2.3") || ipSuffixRE.MatchString("x1.2.3.4") {
		t.Error("ipSuffixRE matched something that does not end in a whole IPv4 address")
	}
	for name, want := range map[string]bool{"syslog": true, "kern.log": true, "wtmp": true, "btmp": true, "dmesg": true,
		"libc.so": false, "foo.ko": false, "liblog.so.6": false, "random.bin": false} {
		if got := looksLikeLog(name); got != want {
			t.Errorf("looksLikeLog(%q) = %v, want %v", name, got, want)
		}
	}
}

// Security and audit logs (live or rotated, or anything under an audit directory) are never emptied
// or deleted.
func TestProtectedLogsLiveOrRotated(t *testing.T) {
	protected := []string{
		"/var/log/auth.log", "/var/log/auth.log.1", "/var/log/auth.log.2.gz", "/var/log/auth.log-20240115.gz",
		"/var/log/secure", "/var/log/secure-20240115", "/var/log/btmp.1", "/var/log/wtmp", "/var/log/lastlog",
		"/var/log/faillog", "/var/log/audit/audit.log", "/var/log/audit/audit.log.3", "/var/log/audit/anything.txt",
	}
	for _, p := range protected {
		if !isProtectedLog(p) {
			t.Errorf("%s should be protected", p)
		}
	}
	for _, p := range []string{"/var/log/syslog.1", "/var/log/kern.log", "/var/log/auditd-helper.log", "/var/log/myaudit/x.log", "/var/log/mail.log.2.gz"} {
		if isProtectedLog(p) {
			t.Errorf("%s should not be protected", p)
		}
	}
}

// The allowlist check must be by path component: /tmpfoo is not inside /tmp.
func TestWithinBaseIsByComponent(t *testing.T) {
	bases := []string{"/tmp", "/var/tmp"}
	for p, want := range map[string]bool{
		"/tmp": true, "/tmp/x": true, "/var/tmp/a/b": true,
		"/tmpfoo": false, "/tmp-evil/x": false, "/var/tmpx": false, "/": false, "/var": false, "": false,
	} {
		if got := withinBase(p, bases); got != want {
			t.Errorf("withinBase(%q) = %v, want %v", p, got, want)
		}
	}
	for name, want := range map[string]bool{
		".X11-unix": true, "systemd-private-abc-chronyd.service-xyz": true, "tmux-1000": true, "ssh-XXXX": true, "snap.lxd": true,
		"debconf": true, "foo": false, "my-systemd-private": false,
	} {
		if got := isCleanupProtected(name); got != want {
			t.Errorf("isCleanupProtected(%q) = %v, want %v", name, got, want)
		}
	}
}

// ResolveDirs refuses a candidate that is a symlink (even one pointing inside the allowlist) or
// that lies outside the allowed bases, and accepts a real directory inside them.
func TestResolveDirsRefusesSymlinksAndOutsiders(t *testing.T) {
	base := t.TempDir()
	// t.TempDir may itself sit under a symlinked path (macOS /var); resolve so comparisons hold.
	base, _ = filepath.EvalSymlinks(base)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	realDir := filepath.Join(base, "cache")
	os.Mkdir(realDir, 0o755)
	link := filepath.Join(base, "link")
	os.Symlink(outside, link)
	inLink := filepath.Join(base, "inlink")
	os.Symlink(realDir, inLink)

	c := &Cleaner{}
	got := c.ResolveDirs([]string{realDir, link, inLink, outside, "  ", filepath.Join(base, "missing")}, []string{base})
	if len(got) != 1 || got[0] != realDir {
		t.Fatalf("ResolveDirs = %v, want only %s", got, realDir)
	}
	// With unsafe paths allowed the allowlist is lifted, but symlinks are still refused.
	c.Opts.AllowUnsafePaths = true
	got = c.ResolveDirs([]string{link, outside}, []string{base})
	if len(got) != 1 || got[0] != outside {
		t.Fatalf("unsafe ResolveDirs = %v, want only %s", got, outside)
	}
}

// removeLog and openRegular go through os.Root: a directory under the log dir that is a symlink out
// of it must not be followed, nor a symlink in a log's place.
func TestRemoveLogDoesNotFollowSymlinks(t *testing.T) {
	logDir, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim.log.1")
	writeFile(t, victim, []byte("precious"))
	secret := filepath.Join(outside, "secret")
	writeFile(t, secret, []byte("keep me"))
	os.Symlink(outside, filepath.Join(logDir, "sub"))
	os.Symlink(secret, filepath.Join(logDir, "syslog.1"))
	realLog := filepath.Join(logDir, "nested", "kern.log.1")
	writeFile(t, realLog, []byte("old"))

	m := &LogMonitor{LogDir: logDir, ThresholdBytes: 1}

	// rootFor: inside the log dir, the root is the log dir and the name is relative.
	r, name, err := m.rootFor(realLog)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if name != filepath.Join("nested", "kern.log.1") {
		t.Errorf("rootFor relative name = %q", name)
	}
	// The log dir itself, or a path escaping it, falls back to the file's own directory.
	if r, name, err := m.rootFor(filepath.Join(logDir, "..", filepath.Base(outside), "victim.log.1")); err == nil {
		r.Close()
		if name != "victim.log.1" {
			t.Errorf("fallback name = %q", name)
		}
	}

	if err := m.removeLog(filepath.Join(logDir, "sub", "victim.log.1")); err == nil {
		t.Error("removeLog followed a symlinked directory out of the log dir")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a file outside the log dir was deleted through a symlinked directory")
	}
	if err := m.removeLog(filepath.Join(logDir, "syslog.1")); err == nil {
		t.Error("removeLog removed a symlink in a log's place (it should refuse non-regular files)")
	}
	if b, _ := os.ReadFile(secret); string(b) != "keep me" {
		t.Fatal("the symlink target was touched")
	}
	if f, err := m.openRegular(filepath.Join(logDir, "syslog.1")); err == nil {
		f.Close()
		t.Error("openRegular opened a log through a symlink")
	}
	if f, err := m.openRegular(filepath.Join(logDir, "sub", "victim.log.1")); err == nil {
		f.Close()
		t.Error("openRegular opened a file through a symlinked directory")
	}
	if m.cleanRegular(filepath.Join(logDir, "sub", "victim.log.1"), true) {
		t.Error("cleanRegular reported success through a symlinked directory")
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatal("a file outside the log dir was truncated")
	}
	// The real one does go.
	if err := m.removeLog(realLog); err != nil {
		t.Fatalf("removeLog on a real rotated log: %v", err)
	}
	if _, err := os.Stat(realLog); !os.IsNotExist(err) {
		t.Fatal("the real rotated log is still there")
	}
}

// cleanRegular keeps the newest ThresholdBytes in place (same inode), empties a log when forced, and
// only trims a protected log even when forced.
func TestCleanRegularTrimsAndProtects(t *testing.T) {
	dir := t.TempDir()
	m := &LogMonitor{LogDir: dir, ThresholdBytes: 1000}
	data := bytes.Repeat([]byte("0123456789"), 300) // 3000 bytes
	data = append(data[:2990], []byte("NEWEST-END")...)

	app := filepath.Join(dir, "app.log")
	writeFile(t, app, data)
	before, _ := os.Stat(app)
	if !m.cleanRegular(app, false) {
		t.Fatal("trim failed")
	}
	got, _ := os.ReadFile(app)
	if len(got) != 1000 || !bytes.Equal(got, data[2000:]) {
		t.Fatalf("trim kept %d bytes ending %q", len(got), got[max(0, len(got)-10):])
	}
	after, _ := os.Stat(app)
	if !os.SameFile(before, after) {
		t.Error("trim replaced the file instead of rewriting it in place")
	}

	writeFile(t, app, data)
	if !m.cleanRegular(app, true) {
		t.Fatal("forced clean failed")
	}
	if st, _ := os.Stat(app); st.Size() != 0 {
		t.Errorf("forced clean left %d bytes", st.Size())
	}

	auth := filepath.Join(dir, "auth.log")
	writeFile(t, auth, data)
	if !m.cleanRegular(auth, true) {
		t.Fatal("forced clean of auth.log failed")
	}
	if st, _ := os.Stat(auth); st.Size() != 1000 {
		t.Errorf("forced clean of a protected log left %d bytes, want it trimmed to 1000", st.Size())
	}

	small := filepath.Join(dir, "small.log")
	writeFile(t, small, []byte("tiny"))
	if !m.cleanRegular(small, false) {
		t.Fatal("clean of a small log failed")
	}
	if b, _ := os.ReadFile(small); string(b) != "tiny" {
		t.Error("a log under the threshold was changed")
	}

	// A rotated protected archive is kept whole by Clean even when forced.
	arch := filepath.Join(dir, "auth.log.2.gz")
	writeFile(t, arch, data)
	old := time.Now().Add(-400 * 24 * time.Hour)
	os.Chtimes(arch, old, old)
	if !m.Clean(LogInfo{Path: arch, Size: int64(len(data)), ModTime: old}, true) {
		t.Fatal("Clean of a protected archive reported failure")
	}
	if _, err := os.Stat(arch); err != nil {
		t.Fatal("a protected rotated archive was deleted")
	}
}

// RotatedLogs only lists regular, rotated, unprotected files, and skips journal, private and audit.
func TestRotatedLogsListing(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "evil.log.1"), []byte("x"))
	for _, name := range []string{
		"syslog.1", "kern.log.2.gz", "nginx/access.log.3.gz", // listed
		"syslog", "auth.log.1", "btmp.1", "audit/audit.log.1", "journal/x/system.log.1", "private/foo.log.1",
		"mysql/mysql-bin.000001", "samba/log.192.168.1.10", "libfoo.so.1",
	} {
		writeFile(t, filepath.Join(dir, name), []byte("data"))
	}
	os.Symlink(filepath.Join(outside, "evil.log.1"), filepath.Join(dir, "linked.log.1"))
	os.Symlink(outside, filepath.Join(dir, "linkdir"))

	m := &LogMonitor{LogDir: dir, ThresholdBytes: 1 << 20, RetentionDays: 7}
	var got []string
	for _, l := range m.RotatedLogs() {
		rel, _ := filepath.Rel(dir, l.Path)
		got = append(got, rel)
	}
	want := "kern.log.2.gz,nginx/access.log.3.gz,syslog.1"
	if strings.Join(got, ",") != want {
		t.Fatalf("RotatedLogs = %v, want %s", got, want)
	}
}

// CleanTree removes stale files and symlinks (the link, never its target), empty stale directories,
// and leaves protected names, fresh files and special files alone.
func TestCleanTreeNeverFollowsLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "important")
	writeFile(t, target, []byte("keep"))
	writeFile(t, filepath.Join(outside, "dir", "inner"), []byte("keep"))

	stale := time.Now().Add(-48 * time.Hour)
	writeFile(t, filepath.Join(root, "old.tmp"), []byte("12345"))
	writeFile(t, filepath.Join(root, "fresh.tmp"), []byte("new"))
	writeFile(t, filepath.Join(root, "olddir", "deep.tmp"), []byte("x"))
	writeFile(t, filepath.Join(root, ".X11-unix", "X0"), []byte("x"))
	writeFile(t, filepath.Join(root, "systemd-private-abc", "tmp", "x"), []byte("x"))
	os.Symlink(target, filepath.Join(root, "filelink"))
	os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "dirlink"))
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"old.tmp", "olddir/deep.tmp", "olddir", ".X11-unix/X0", ".X11-unix",
		"systemd-private-abc/tmp/x", "systemd-private-abc/tmp", "systemd-private-abc", "filelink", "dirlink", "fifo"} {
		lchtimes(t, filepath.Join(root, p), stale)
	}

	var st Stats
	CleanTree(root, 24*time.Hour, &st, time.Now())

	gone := []string{"old.tmp", "olddir", "filelink", "dirlink"}
	kept := []string{"fresh.tmp", ".X11-unix/X0", "systemd-private-abc/tmp/x", "fifo"}
	for _, p := range gone {
		if _, err := os.Lstat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", p)
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s should have been kept: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("a symlink's target was removed or changed")
	}
	if _, err := os.Stat(filepath.Join(outside, "dir", "inner")); err != nil {
		t.Fatal("CleanTree followed a symlinked directory and deleted inside it")
	}
	if st.FilesRemoved != 4 || st.DirsRemoved != 1 {
		t.Errorf("stats = %d files, %d dirs; want 4 files (old.tmp, deep.tmp, two links), 1 dir", st.FilesRemoved, st.DirsRemoved)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("CleanTree removed its own root")
	}
}

// mountinfo parsing decides which filesystem cleanup may touch: escapes, optional fields, the
// longest mount point, and only /dev-backed sources matching.
func TestMountinfoParsingAndContainment(t *testing.T) {
	const mountinfo = `22 1 0:21 / / rw,relatime shared:1 - btrfs /dev/sda2 rw,subvol=/@
23 22 0:22 /@var /var rw,relatime shared:2 - btrfs /dev/sda2 rw,subvol=/@var
24 22 0:23 / /tmp rw,nosuid,nodev shared:3 - tmpfs tmpfs rw
25 22 8:17 / /mnt/my\040disk rw,relatime shared:4 master:1 - ext4 /dev/sdb1 rw
26 22 0:24 / /var/lib/docker rw - xfs /dev/sdc1 rw
27 22 0:25 / /srv/nas rw - nfs4 nas:/export rw
28 22 0:26 / /srv/nas2 rw - nfs4 nas:/export rw
bad line
29 22 x:y / /broken rw - ext4 /dev/sdz rw
30 24 0:27 / /tmp rw - ext4 /dev/sdd1 rw
`
	mounts := parseMountinfo(mountinfo)
	if len(mounts) != 8 {
		t.Fatalf("parsed %d mounts, want 8: %+v", len(mounts), mounts)
	}
	if mounts[3].point != "/mnt/my disk" || mounts[3].source != "/dev/sdb1" || mounts[3].dev != unix.Mkdev(8, 17) {
		t.Errorf("escaped mount = %+v", mounts[3])
	}
	for path, want := range map[string]string{
		"/":                        "/",
		"/var/log":                 "/var",
		"/variable":                "/", // not under /var
		"/var/lib/docker/overlay2": "/var/lib/docker",
		"/mnt/my disk/films":       "/mnt/my disk",
		"/tmp/x":                   "/tmp",
	} {
		m, ok := containingMount(mounts, path)
		if !ok || m.point != want {
			t.Errorf("containingMount(%q) = %q, want %q", path, m.point, want)
		}
	}
	// Two mounts on /tmp: the last one mounted is what a path there is on.
	if m, _ := containingMount(mounts, "/tmp/x"); m.source != "/dev/sdd1" {
		t.Errorf("stacked /tmp resolved to %q, want the later /dev/sdd1", m.source)
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"/var/log", "/", true},            // two btrfs subvolumes of one pool
		{"/var/lib/docker", "/var", false}, // a different disk
		{"/mnt/my disk", "/", false},
		{"/srv/nas", "/srv/nas2", false}, // network shares never match
		{"/home", "/", true},
	}
	for _, c := range cases {
		if got := SameBlockDevice(mountinfo, c.a, c.b); got != c.want {
			t.Errorf("SameBlockDevice(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for in, want := range map[string]string{`a\040b`: "a b", `tab\011x`: "tab\tx", `back\134slash`: `back\slash`, `trail\04`: `trail\04`, `plain`: "plain"} {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

// sameFilesystem: a directory and one inside it are on one filesystem; a missing path is on none.
// deviceOf of a FileInfo without a Stat_t is 0, not a panic.
func TestSameFilesystemAndDeviceOf(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	if !sameFilesystem(dir, sub) {
		t.Error("a directory and its subdirectory should be on one filesystem")
	}
	if sameFilesystem(dir, filepath.Join(dir, "missing")) || sameFilesystem(filepath.Join(dir, "missing"), dir) {
		t.Error("a missing path is on no filesystem")
	}
	if got := deviceOf(fakeInfo{}); got != 0 {
		t.Errorf("deviceOf without Stat_t = %d", got)
	}
	st, _ := os.Stat(dir)
	if deviceOf(st) == 0 {
		t.Error("deviceOf a real directory should be its device")
	}
	if got := or(nil, []string{"x"}); len(got) != 1 || got[0] != "x" {
		t.Errorf("or(nil, def) = %v", got)
	}
}

// dirSize counts a hardlinked file once, does not follow symlinks, and skips another filesystem's
// mount point given the mount table.
func TestDirSizeHardlinksSymlinksAndMounts(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "huge"), make([]byte, 1<<20))
	writeFile(t, filepath.Join(dir, "a"), make([]byte, 1000))
	if err := os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "a-link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "c"), make([]byte, 500))
	os.Symlink(outside, filepath.Join(dir, "out"))
	writeFile(t, filepath.Join(dir, "mnt", "big"), make([]byte, 4000))

	size, partial := dirSize(dir, 0, nil, 0)
	if partial {
		t.Error("a complete walk reported partial")
	}
	// 1000 + 500 + 4000, plus the symlink's own (tiny) size: never the 1 MiB behind it.
	if size < 5500 || size > 5500+4096 {
		t.Errorf("dirSize = %d, want ~5500", size)
	}
	st, _ := os.Stat(dir)
	mounts := map[string]uint64{filepath.Join(dir, "mnt"): deviceOf(st) + 1}
	size, _ = dirSize(dir, 0, mounts, deviceOf(st))
	if size < 1500 || size > 1500+4096 {
		t.Errorf("dirSize skipping a mount = %d, want ~1500", size)
	}
}

type fakeInfo struct{ os.FileInfo }

func (fakeInfo) Sys() any { return nil }

// A lock apt or dpkg holds (an fcntl record lock) is seen; a free or missing lock file is not.
func TestLockHeldSeesDpkgStyleLocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock-frontend")
	writeFile(t, path, nil)
	if lockHeld(path) || lockHeld(filepath.Join(dir, "missing")) {
		t.Fatal("a free lock counts as held")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lk := unix.Flock_t{Type: unix.F_WRLCK}
	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
		t.Fatal(err)
	}
	if !lockHeld(path) {
		t.Fatal("dpkg's kind of lock was not seen")
	}
	lk.Type = unix.F_UNLCK
	unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
	if lockHeld(path) {
		t.Fatal("a released lock counts as held")
	}
}

// While the package manager works, its downloaded packages stay, in routine and emergency cleanup.
func TestPackageCachesWaitForThePackageManager(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "apt", "archives")
	deb := filepath.Join(cache, "thing_1.0_amd64.deb")
	writeFile(t, deb, []byte("package"))
	lchtimes(t, deb, time.Now().Add(-48*time.Hour))
	defer func(old func(string) bool) { packagesBusy = old }(packagesBusy)
	packagesBusy = func(string) bool { return true }
	if st := cleanPackageCaches([]string{cache}); st.FilesRemoved != 0 {
		t.Fatalf("removed %d while apt was busy", st.FilesRemoved)
	}
	if _, err := os.Stat(deb); err != nil {
		t.Fatal("the package went while apt was busy")
	}
	packagesBusy = func(string) bool { return false }
	if st := cleanPackageCaches([]string{cache}); st.FilesRemoved != 1 {
		t.Fatalf("removed %d once apt was done", st.FilesRemoved)
	}
}
