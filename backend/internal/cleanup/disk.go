package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"nightjar/internal/collect"
	"nightjar/internal/format"
	"nightjar/internal/logx"
)

var diskLog = logx.Component("Disk")

var (
	allowedTempBases  = []string{"/tmp", "/var/tmp"}
	allowedCacheBases = []string{"/var/cache"}
	defaultCacheDirs  = []string{"/var/cache/apt/archives", "/var/cache/apt/pkgcache.bin", "/var/cache/apt/srcpkgcache.bin"}
	// Emergency mode targets regenerable caches only, not all of /var/cache.
	emergencyCacheDirs = []string{"/var/cache/apt/archives", "/var/cache/pacman/pkg", "/var/cache/dnf", "/tmp", "/var/tmp"}
	protectedNames     = map[string]bool{
		".X11-unix": true, ".ICE-unix": true, ".font-unix": true, ".XIM-unix": true, ".Test-unix": true,
		"debconf": true, "ldconfig": true, "private": true, "snap-private-tmp": true,
	}
	protectedPrefixes = []string{"systemd-private-", "tmux-", "ssh-", "snap."}
	serviceNameRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]*$`)
	userGroupRE       = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?:[a-z_][a-z0-9_-]*[$]?$`)
)

func isCleanupProtected(name string) bool {
	if protectedNames[name] {
		return true
	}
	for _, p := range protectedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Options configure the Cleaner.
type Options struct {
	AllowUnsafePaths     bool
	AggressiveThreshold  float64
	LogServicesToRestart []string
	ManageNginxLogs      bool
	NginxLogDir          string
	NginxUserGroup       string
	NginxServiceName     string
	// Directories to clean; empty means the built-in lists. They must sit inside the allowed bases.
	TempDirs, CacheDirs, EmergencyCacheDirs []string
}

// Cleaner runs routine and emergency disk cleanup.
type Cleaner struct {
	Opts Options
	Logs *LogMonitor
}

func withinBase(path string, bases []string) bool {
	for _, b := range bases {
		if path == b || strings.HasPrefix(path, b+"/") {
			return true
		}
	}
	return false
}

// ResolveDirs keeps only candidates that resolve (without symlinks) inside the allowlist,
// unless ALLOW_UNSAFE_CLEANUP_PATHS is on.
func (c *Cleaner) ResolveDirs(candidates, allowed []string) []string {
	var out []string
	for _, raw := range candidates {
		candidate := strings.TrimSpace(raw)
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		if resolved != filepath.Clean(abs) {
			diskLog.Warn("[CLEANUP] refusing to clean symlinked path", "path", candidate)
			continue
		}
		if !c.Opts.AllowUnsafePaths && !withinBase(resolved, allowed) {
			diskLog.Warn("[CLEANUP] refusing to clean path outside the allowlist", "path", candidate)
			continue
		}
		if c.Opts.AllowUnsafePaths {
			diskLog.Warn("[CLEANUP] ALLOW_UNSAFE_CLEANUP_PATHS is enabled - cleaning unrestricted path", "path", resolved)
		}
		out = append(out, resolved)
	}
	return out
}

// CleanTree removes stale regular files and symlinks under root without following links.
// Every operation goes through os.Root, so a directory swapped for a symlink mid-walk cannot
// redirect deletion elsewhere. Sockets, FIFOs and devices are left alone, other filesystems
// are not crossed, and a directory is only removed once empty and older than maxAge.
func CleanTree(root string, maxAge time.Duration, st *Stats, now time.Time) {
	r, err := os.OpenRoot(root)
	if err != nil {
		st.Errors = append(st.Errors, fmt.Sprintf("could not open %s: %v", root, err))
		return
	}
	defer r.Close()
	rootInfo, err := r.Stat(".")
	if err != nil {
		return
	}
	dev := deviceOf(rootInfo)
	var walk func(r *os.Root, dir string)
	walk = func(r *os.Root, dir string) {
		d, err := r.Open(".")
		if err != nil {
			st.Errors = append(st.Errors, fmt.Sprintf("could not list %s: %v", dir, err))
			return
		}
		names, err := d.Readdirnames(-1)
		d.Close()
		if err != nil {
			return
		}
		for _, name := range names {
			if isCleanupProtected(name) {
				continue
			}
			full := filepath.Join(dir, name)
			info, err := r.Lstat(name)
			if err != nil {
				continue
			}
			old := now.Sub(info.ModTime()) > maxAge
			switch {
			case info.IsDir():
				if deviceOf(info) != dev {
					continue
				}
				child, err := r.OpenRoot(name)
				if err != nil {
					continue
				}
				walk(child, full)
				child.Close()
				if old && r.Remove(name) == nil {
					st.DirsRemoved++
				}
			case info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0:
				if !old {
					continue
				}
				if err := r.Remove(name); err != nil {
					if errors.Is(err, syscall.ENOSPC) {
						st.Errors = append(st.Errors, fmt.Sprintf("disk full while removing %s: %v", full, err))
					}
					continue
				}
				st.FilesRemoved++
				st.BytesCleared += info.Size()
			}
		}
	}
	walk(r, root)
}

func deviceOf(info fs.FileInfo) uint64 {
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Dev)
	}
	return 0
}

// sameFilesystem says whether path sits on the filesystem mounted at mount: only cleaning there
// can free space there. The device number alone is not enough: every btrfs subvolume has its own,
// though all share one pool (openSUSE mounts @/var at /var, so /var/log looks apart from /).
func sameFilesystem(mount, path string) bool {
	m, err := os.Stat(mount)
	if err != nil {
		return false
	}
	p, err := os.Stat(path)
	if err != nil {
		return false
	}
	if deviceOf(m) == deviceOf(p) {
		return true
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	return err == nil && SameBlockDevice(string(data), resolvePath(mount), resolvePath(path))
}

// resolvePath is path made absolute with its symlinks resolved, as far as that works.
func resolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// SameBlockDevice says whether the mounts holding a and b (absolute, symlink-free paths) are of
// one block device, per mountinfo (the text of /proc/self/mountinfo): two btrfs subvolumes, or a
// bind mount and its origin. Mounts not backed by a /dev device (tmpfs, network shares, ZFS
// datasets) never match here.
func SameBlockDevice(mountinfo, a, b string) bool {
	mounts := parseMountinfo(mountinfo)
	ma, ok1 := containingMount(mounts, a)
	mb, ok2 := containingMount(mounts, b)
	return ok1 && ok2 && strings.HasPrefix(ma.source, "/dev/") && ma.source == mb.source
}

// mountEntry is one line of mountinfo.
type mountEntry struct {
	point, source string
	dev           uint64
}

// parseMountinfo reads /proc/self/mountinfo's format: "id parent major:minor root mountpoint
// options [optional fields...] - fstype source superoptions".
func parseMountinfo(data string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		major, minor, ok := strings.Cut(f[2], ":")
		maj, err1 := strconv.ParseUint(major, 10, 32)
		mnr, err2 := strconv.ParseUint(minor, 10, 32)
		if !ok || err1 != nil || err2 != nil {
			continue
		}
		e := mountEntry{point: unescapeMount(f[4]), dev: unix.Mkdev(uint32(maj), uint32(mnr))}
		for i := 6; i+2 < len(f); i++ {
			if f[i] == "-" {
				e.source = unescapeMount(f[i+2])
				break
			}
		}
		out = append(out, e)
	}
	return out
}

// containingMount is the mount path sits on: the longest mount point it is under, the last one
// mounted when several share it.
func containingMount(mounts []mountEntry, path string) (mountEntry, bool) {
	var best mountEntry
	found := false
	for _, m := range mounts {
		under := path == m.point || m.point == "/" || strings.HasPrefix(path, m.point+"/")
		if under && (!found || len(m.point) >= len(best.point)) {
			best, found = m, true
		}
	}
	return best, found
}

// onMount keeps the paths on mount's filesystem.
func onMount(mount string, paths []string) []string {
	var out []string
	for _, p := range paths {
		if sameFilesystem(mount, p) {
			out = append(out, p)
		}
	}
	return out
}

// or returns dirs, or def when dirs is empty.
func or(dirs, def []string) []string {
	if len(dirs) == 0 {
		return def
	}
	return dirs
}

// CleanTemp removes files older than maxAgeDays (at least one hour) from the temp directories.
func (c *Cleaner) CleanTemp(maxAgeDays float64) Stats {
	return cleanTemp(c.tempDirs(), maxAgeDays)
}

func (c *Cleaner) tempDirs() []string {
	return c.ResolveDirs(or(c.Opts.TempDirs, allowedTempBases), allowedTempBases)
}

func cleanTemp(dirs []string, maxAgeDays float64) Stats {
	var st Stats
	maxAge := time.Duration(max(3600, maxAgeDays*secondsPerDay) * float64(time.Second))
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			CleanTree(dir, maxAge, &st, time.Now())
		}
	}
	return st
}

// cleanLogs trims every log over threshold, or empties all of them when aggressive.
func (c *Cleaner) cleanLogs(aggressive, includeMonitored bool) Stats {
	if c.Logs == nil {
		return Stats{}
	}
	st := Stats{}
	rotated := c.Logs.CleanupRotated(aggressive)
	st.LogsCleaned += rotated.FilesRemoved
	st.BytesCleared += rotated.BytesCleared
	st.Errors = append(st.Errors, rotated.Errors...)

	candidates := c.Logs.AllLogFiles()
	sizes := c.Logs.Survey()
	if includeMonitored {
		if sizes.App != nil {
			candidates = append(candidates, *sizes.App)
		}
		candidates = append(candidates, sizes.System...)
	}
	if sizes.Journal != nil {
		candidates = append(candidates, *sizes.Journal)
	}
	logs := LargestPerPath(candidates)
	if !aggressive {
		var over []LogInfo
		for _, l := range logs {
			if l.Size > c.Logs.ThresholdBytes {
				over = append(over, l)
			}
		}
		logs = over
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].Size > logs[j].Size })
	for _, l := range logs {
		current, ok := c.Logs.Size(l.Path)
		if l.Path == c.Logs.JournalPath {
			current, ok = l, true
		}
		if !ok || current.Size == 0 {
			continue
		}
		if !c.Logs.Clean(current, aggressive) {
			continue
		}
		st.LogsCleaned++
		after, _ := c.Logs.Size(l.Path)
		st.BytesCleared += max(0, current.Size-after.Size)
		diskLog.Info(fmt.Sprintf("    Cleaned log: %s (%s -> %s)", l.Path, format.Bytes(float64(current.Size)), format.Bytes(float64(after.Size))))
	}
	if st.LogsCleaned > 0 {
		c.RestartLogServices()
		c.SetupNginxLogs()
	}
	return st
}

func usagePercent(mount string) *float64 {
	u, err := collect.DiskUsage(mount)
	if err != nil {
		return nil
	}
	return &u.Percent
}

func freeEnough(mount string, target float64) bool {
	p := usagePercent(mount)
	return p != nil && 100-*p >= target
}

// CleanDiskSpace works through logs, temp files and package caches until targetFreePercent is free.
// Only what sits on mount's filesystem is touched: emptying /var/log cannot free a byte on a full
// data disk.
func (c *Cleaner) CleanDiskSpace(mount string, targetFreePercent float64) Stats {
	var st Stats
	initial := usagePercent(mount)
	if initial == nil {
		st.Errors = append(st.Errors, "could not get disk usage for "+mount)
		return st
	}
	st.InitialUsage = initial
	if 100-*initial >= targetFreePercent {
		diskLog.Info(fmt.Sprintf("[CLEANUP] Disk already has %.1f%% free (target: %.1f%%)", 100-*initial, targetFreePercent))
		st.FinalUsage = initial
		return st
	}
	logsHere := c.Logs != nil && sameFilesystem(mount, c.Logs.LogDir)
	temp := onMount(mount, c.tempDirs())
	caches := onMount(mount, c.ResolveDirs(or(c.Opts.CacheDirs, defaultCacheDirs), allowedCacheBases))
	if !logsHere && len(temp) == 0 && len(caches) == 0 {
		diskLog.Info(fmt.Sprintf("[CLEANUP] %s is %.1f%% used, but nothing Nightjar cleans is on it", mount, *initial))
		st.FinalUsage = initial
		return st
	}
	diskLog.Info(fmt.Sprintf("[CLEANUP] Starting disk cleanup: %.1f%% used, %.1f%% free (target: %.1f%% free)", *initial, 100-*initial, targetFreePercent))
	aggressive := *initial >= c.Opts.AggressiveThreshold

	if logsHere {
		if aggressive {
			diskLog.Warn(fmt.Sprintf("[CLEANUP] Step 1: Aggressive log cleanup (disk >= %.1f%%, emptying logs in /var/log)...", c.Opts.AggressiveThreshold))
		} else {
			diskLog.Info("[CLEANUP] Step 1: Cleaning log files (all logs in /var/log exceeding threshold)...")
		}
		st.add(c.cleanLogs(aggressive, true))
		if freeEnough(mount, targetFreePercent) {
			st.FinalUsage = usagePercent(mount)
			return st
		}
	}

	if len(temp) > 0 {
		diskLog.Info("[CLEANUP] Step 2: Cleaning temporary files...")
		for _, days := range []float64{7, 3, 1, 0} {
			st.add(cleanTemp(temp, days))
			if freeEnough(mount, targetFreePercent) {
				break
			}
		}
	}
	if aggressive && len(caches) > 0 {
		diskLog.Info("[CLEANUP] Step 3: Cleaning package caches and other space consumers...")
		st.add(cleanPackageCaches(caches))
	}
	st.FinalUsage = usagePercent(mount)
	if st.FinalUsage != nil {
		diskLog.Info(fmt.Sprintf("[CLEANUP] Disk cleanup complete: %.1f%% used, %.1f%% free. Cleared %s (%d files, %d dirs, %d logs)",
			*st.FinalUsage, 100-*st.FinalUsage, format.Bytes(float64(st.BytesCleared)), st.FilesRemoved, st.DirsRemoved, st.LogsCleaned))
	}
	return st
}

// cleanPackageCaches empties the given (already resolved) package caches.
func cleanPackageCaches(paths []string) Stats {
	var st Stats
	for _, path := range paths {
		if strings.Contains(path, "apt/archives") {
			entries, err := os.ReadDir(path)
			if err != nil {
				st.Errors = append(st.Errors, fmt.Sprintf("error cleaning %s: %v", path, err))
				continue
			}
			for _, e := range entries {
				if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".deb") {
					continue
				}
				info, err := e.Info()
				if err == nil && os.Remove(filepath.Join(path, e.Name())) == nil {
					st.FilesRemoved++
					st.BytesCleared += info.Size()
				}
			}
		} else if strings.HasSuffix(path, ".bin") {
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && os.Remove(path) == nil {
				st.FilesRemoved++
				st.BytesCleared += info.Size()
			}
		}
	}
	if st.BytesCleared > 0 {
		diskLog.Info(fmt.Sprintf("[CLEANUP] Cleaned package caches: %s (%d files)", format.Bytes(float64(st.BytesCleared)), st.FilesRemoved))
	}
	return st
}

// EmergencyClean runs every step at once: temp files older than an hour, every log, and regenerable
// caches, of those on mount's filesystem.
func (c *Cleaner) EmergencyClean(mount string) Stats {
	var st Stats
	initial := usagePercent(mount)
	if initial == nil {
		st.Errors = append(st.Errors, "could not get disk usage for "+mount)
		return st
	}
	st.InitialUsage = initial
	logsHere := c.Logs != nil && sameFilesystem(mount, c.Logs.LogDir)
	temp := onMount(mount, c.tempDirs())
	allowed := append(append([]string{}, allowedTempBases...), allowedCacheBases...)
	caches := onMount(mount, c.ResolveDirs(or(c.Opts.EmergencyCacheDirs, emergencyCacheDirs), allowed))
	if !logsHere && len(temp) == 0 && len(caches) == 0 {
		diskLog.Warn(fmt.Sprintf("[EMERGENCY] %s is %.1f%% used, but nothing Nightjar cleans is on it", mount, *initial))
		st.FinalUsage = initial
		return st
	}
	diskLog.Warn(fmt.Sprintf("[EMERGENCY] Disk critically full: %.1f%% used, %.1f%% free", *initial, 100-*initial))
	if len(temp) > 0 {
		diskLog.Info("[EMERGENCY] Step 1: Cleaning all temporary files...")
		st.add(cleanTemp(temp, 1.0/24))
	}
	if logsHere {
		diskLog.Info("[EMERGENCY] Step 2: Cleaning log files (aggressive - ALL logs in /var/log)...")
		st.add(c.cleanLogs(true, true))
	}
	if len(caches) > 0 {
		diskLog.Info("[EMERGENCY] Step 3: Cleaning additional cache directories...")
	}
	for _, dir := range caches {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			CleanTree(dir, 24*time.Hour, &st, time.Now())
		}
	}
	st.FinalUsage = usagePercent(mount)
	if st.FinalUsage != nil {
		diskLog.Warn(fmt.Sprintf("[EMERGENCY] Complete: %.1f%% used, %.1f%% free. Cleared %s (%d files, %d dirs, %d logs)",
			*st.FinalUsage, 100-*st.FinalUsage, format.Bytes(float64(st.BytesCleared)), st.FilesRemoved, st.DirsRemoved, st.LogsCleaned))
	}
	return st
}

// CleanLogsAndRestart empties every log (except protected ones) and restarts the log daemons.
func (c *Cleaner) CleanLogsAndRestart() Stats {
	if c.Logs == nil {
		return Stats{Errors: []string{"no log monitor"}}
	}
	return c.cleanLogs(true, false)
}

// RestartLogServices try-restarts LOG_SERVICES_TO_RESTART so daemons reopen truncated files.
func (c *Cleaner) RestartLogServices() {
	if runtime.GOOS != "linux" {
		return
	}
	for _, raw := range c.Opts.LogServicesToRestart {
		name := strings.TrimSuffix(strings.TrimSpace(raw), ".service")
		if !serviceNameRE.MatchString(name) {
			diskLog.Warn("ignoring invalid service name in LOG_SERVICES_TO_RESTART", "name", name)
			continue
		}
		diskLog.Info("    Restarting " + name + "...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, "systemctl", "try-restart", name+".service").CombinedOutput()
		cancel()
		if err != nil {
			diskLog.Warn("    Failed to restart "+name, "err", strings.TrimSpace(string(out)))
		} else {
			diskLog.Info("    " + name + " restarted successfully")
		}
	}
}

// nginxOwner looks up NGINX_USER_GROUP's user and group ids.
func nginxOwner(userGroup string) (uid, gid int, ok bool) {
	name, group, _ := strings.Cut(userGroup, ":")
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, false
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(g.Gid)
	return uid, gid, err1 == nil && err2 == nil
}

// reclaimDir gives the directory r is open on back to root, without group or other write, when
// it is not root's already. Earlier versions chowned the nginx log dir to the web user.
func reclaimDir(r *os.Root, dir string) error {
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	info, err := d.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid == 0 && info.Mode().Perm()&0o022 == 0) {
		return nil
	}
	if err := d.Chown(0, -1); err != nil {
		return err
	}
	if err := d.Chmod(info.Mode() & (fs.ModePerm | fs.ModeSetgid | fs.ModeSticky) &^ 0o022); err != nil {
		return err
	}
	diskLog.Warn("[CLEANUP] gave the nginx log dir back to root; it was writable by another user", "dir", dir)
	return nil
}

// SetupNginxLogs recreates nginx's log files and restarts it, when MANAGE_NGINX_LOGS is on.
// The directory is kept root's (nginx opens its logs as root), and taken back from the web user
// where an earlier version handed it over: were it the web user's, that user could plant a symlink
// at a log's name and have root nginx open any file through it. The files are created without
// following links and chowned through the open descriptor.
func (c *Cleaner) SetupNginxLogs() {
	o := c.Opts
	if runtime.GOOS != "linux" || !o.ManageNginxLogs {
		return
	}
	if !userGroupRE.MatchString(o.NginxUserGroup) {
		diskLog.Error("invalid NGINX_USER_GROUP format, expected user:group", "value", o.NginxUserGroup)
		return
	}
	if !serviceNameRE.MatchString(o.NginxServiceName) {
		diskLog.Error("invalid NGINX_SERVICE_NAME", "value", o.NginxServiceName)
		return
	}
	run := func(timeout time.Duration, name string, args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Run()
	}
	if err := os.MkdirAll(o.NginxLogDir, 0o755); err != nil {
		diskLog.Error("could not create nginx log dir", "err", err)
		return
	}
	r, err := os.OpenRoot(o.NginxLogDir)
	if err != nil {
		diskLog.Error("could not open nginx log dir", "err", err)
		return
	}
	if err := reclaimDir(r, o.NginxLogDir); err != nil {
		// Restarting nginx now would have root open logs where the web user can plant links.
		diskLog.Error("could not give the nginx log dir back to root; not restarting "+o.NginxServiceName, "dir", o.NginxLogDir, "err", err)
		r.Close()
		return
	}
	uid, gid, owned := nginxOwner(o.NginxUserGroup)
	for _, name := range []string{"error.log", "access.log"} {
		// Anything but a plain file at a log's name (a symlink the web user planted while the folder
		// was theirs) goes, so root nginx does not open some other file through it on the restart.
		if info, err := r.Lstat(name); err == nil && !info.Mode().IsRegular() {
			if err := r.Remove(name); err != nil {
				diskLog.Error("could not remove a non-file at an nginx log's name; not restarting "+o.NginxServiceName, "file", name, "err", err)
				r.Close()
				return
			}
			diskLog.Warn("[CLEANUP] removed a link or other non-file at an nginx log's name", "file", name)
		}
		// O_EXCL never follows a link: an existing file, or a symlink in its place, is left alone.
		f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o640)
		if err != nil {
			continue
		}
		if owned {
			if err := f.Chown(uid, gid); err != nil {
				diskLog.Warn("could not hand nginx log to "+o.NginxUserGroup, "file", name, "err", err)
			}
		}
		f.Close()
	}
	r.Close()
	for attempt := range 3 {
		if run(30*time.Second, "systemctl", "restart", o.NginxServiceName) == nil {
			diskLog.Info("    " + o.NginxServiceName + " restarted successfully")
			return
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	diskLog.Warn("    could not restart " + o.NginxServiceName)
}

// DirSize sums file sizes under path (without following symlinks), stopping at the deadline. The
// deadline holds even when a stat hangs (a dead network mount): the walk is then left behind and
// what it had counted is returned as partial. While a walk left behind has not returned, path is
// not walked again (each would hang a thread as well): that is 0, partial. A timeout of 0 waits for
// the whole walk.
func DirSize(path string, timeout time.Duration) (size int64, partial bool) {
	return dirSize(path, timeout, nil, 0)
}

// fileID names a file by device and inode, so hardlinks to it can be told apart from other files.
type fileID struct{ dev, ino uint64 }

// abandoned counts, per path, the walks dirSize gave up waiting for that are still running.
var abandoned = struct {
	sync.Mutex
	walks map[string]int
}{walks: map[string]int{}}

// dirSize is DirSize that, given the mount table, does not descend into a filesystem other than
// dev mounted below path.
func dirSize(path string, timeout time.Duration, mounts map[string]uint64, dev uint64) (int64, bool) {
	abandoned.Lock()
	stuck := abandoned.walks[path] > 0
	abandoned.Unlock()
	if stuck {
		diskLog.Warn("not sizing " + path + ": an earlier walk of it is still hung; is the mount responding?")
		return 0, true
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var total atomic.Int64
	linked := map[fileID]struct{}{} // only the walk goroutine touches it
	done := make(chan struct{})
	// finished and left are guarded by abandoned's lock: whichever of the walk ending and the
	// caller giving up comes second settles the count.
	var finished, left bool
	go func() {
		defer close(done)
		defer func() {
			abandoned.Lock()
			finished = true
			if left {
				if abandoned.walks[path]--; abandoned.walks[path] <= 0 {
					delete(abandoned.walks, path)
				}
			}
			abandoned.Unlock()
		}()
		filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if md, ok := mounts[p]; ok && p != path && md != dev {
					return filepath.SkipDir
				}
				return nil
			}
			if info, err := d.Info(); err == nil {
				// A hardlinked file (an *arr import of a seeding torrent) is one set of blocks:
				// count it once, as du does, or a media folder reads far bigger than its disk.
				if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
					id := fileID{uint64(st.Dev), st.Ino}
					if _, dup := linked[id]; dup {
						return nil
					}
					linked[id] = struct{}{}
				}
				total.Add(info.Size())
			}
			return nil
		})
	}()
	select {
	case <-done:
		return total.Load(), ctx.Err() != nil
	case <-ctx.Done():
		abandoned.Lock()
		if !finished {
			left = true
			abandoned.walks[path]++
		}
		abandoned.Unlock()
		return total.Load(), true
	}
}

// mountPoints maps each mount point to its device, from /proc/self/mountinfo, without touching the
// mounts themselves (a stat on a dead network mount never returns). Nil where there is no such file.
func mountPoints() map[string]uint64 {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	out := map[string]uint64{}
	for _, m := range parseMountinfo(string(data)) {
		out[m.point] = m.dev
	}
	return out
}

// unescapeMount undoes mountinfo's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// DirEntry is one subdirectory and its size.
type DirEntry struct {
	Path string
	Size int64
}

// LargestSubdirs sizes each immediate subdirectory of parent, largest first, in at most twice
// perDirTimeout all told. When parent is a mount point the question is what fills that filesystem,
// so other filesystems mounted below it (a NAS under /mnt, a second disk at /home) are left out.
func LargestSubdirs(parent string, limit int, perDirTimeout time.Duration) []DirEntry {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var mounts map[string]uint64
	var dev uint64
	if abs, err := filepath.Abs(parent); err == nil {
		all := mountPoints()
		if d, ok := all[abs]; ok {
			mounts, dev, parent = all, d, abs
		}
	}
	deadline := time.Now().Add(2 * perDirTimeout)
	var out []DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(parent, e.Name())
		if parent == "/" && (e.Name() == "proc" || e.Name() == "sys" || e.Name() == "dev" || e.Name() == "run") {
			continue
		}
		if md, ok := mounts[p]; ok && md != dev {
			continue
		}
		timeout := perDirTimeout
		if perDirTimeout > 0 {
			if timeout = min(perDirTimeout, time.Until(deadline)); timeout <= 0 {
				break
			}
		}
		size, _ := dirSize(p, timeout, mounts, dev)
		out = append(out, DirEntry{p, size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
