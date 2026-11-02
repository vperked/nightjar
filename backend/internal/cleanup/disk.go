package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"servermonitor/internal/collect"
	"servermonitor/internal/format"
	"servermonitor/internal/logx"
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

// or returns dirs, or def when dirs is empty.
func or(dirs, def []string) []string {
	if len(dirs) == 0 {
		return def
	}
	return dirs
}

// CleanTemp removes files older than maxAgeDays (at least one hour) from the temp directories.
func (c *Cleaner) CleanTemp(maxAgeDays float64) Stats {
	var st Stats
	maxAge := time.Duration(max(3600, maxAgeDays*secondsPerDay) * float64(time.Second))
	for _, dir := range c.ResolveDirs(or(c.Opts.TempDirs, allowedTempBases), allowedTempBases) {
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
	diskLog.Info(fmt.Sprintf("[CLEANUP] Starting disk cleanup: %.1f%% used, %.1f%% free (target: %.1f%% free)", *initial, 100-*initial, targetFreePercent))
	aggressive := *initial >= c.Opts.AggressiveThreshold

	if c.Logs != nil {
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

	diskLog.Info("[CLEANUP] Step 2: Cleaning temporary files...")
	for _, days := range []float64{7, 3, 1, 0} {
		st.add(c.CleanTemp(days))
		if freeEnough(mount, targetFreePercent) {
			break
		}
	}
	if aggressive {
		diskLog.Info("[CLEANUP] Step 3: Cleaning package caches and other space consumers...")
		st.add(c.cleanPackageCaches())
	}
	st.FinalUsage = usagePercent(mount)
	if st.FinalUsage != nil {
		diskLog.Info(fmt.Sprintf("[CLEANUP] Disk cleanup complete: %.1f%% used, %.1f%% free. Cleared %s (%d files, %d dirs, %d logs)",
			*st.FinalUsage, 100-*st.FinalUsage, format.Bytes(float64(st.BytesCleared)), st.FilesRemoved, st.DirsRemoved, st.LogsCleaned))
	}
	return st
}

func (c *Cleaner) cleanPackageCaches() Stats {
	var st Stats
	candidates := or(c.Opts.CacheDirs, defaultCacheDirs)
	for _, path := range c.ResolveDirs(candidates, allowedCacheBases) {
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

// EmergencyClean runs every step at once: temp files older than an hour, every log, and regenerable caches.
func (c *Cleaner) EmergencyClean(mount string) Stats {
	var st Stats
	initial := usagePercent(mount)
	if initial == nil {
		st.Errors = append(st.Errors, "could not get disk usage for "+mount)
		return st
	}
	st.InitialUsage = initial
	diskLog.Warn(fmt.Sprintf("[EMERGENCY] Disk critically full: %.1f%% used, %.1f%% free", *initial, 100-*initial))
	diskLog.Info("[EMERGENCY] Step 1: Cleaning all temporary files...")
	st.add(c.CleanTemp(1.0 / 24))
	if c.Logs != nil {
		diskLog.Info("[EMERGENCY] Step 2: Cleaning log files (aggressive - ALL logs in /var/log)...")
		st.add(c.cleanLogs(true, true))
	}
	diskLog.Info("[EMERGENCY] Step 3: Cleaning additional cache directories...")
	allowed := append(append([]string{}, allowedTempBases...), allowedCacheBases...)
	for _, dir := range c.ResolveDirs(or(c.Opts.EmergencyCacheDirs, emergencyCacheDirs), allowed) {
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

// SetupNginxLogs recreates nginx's log files and restarts it, when MANAGE_NGINX_LOGS is on.
func (c *Cleaner) SetupNginxLogs() {
	o := c.Opts
	if runtime.GOOS != "linux" || !o.ManageNginxLogs {
		return
	}
	if !userGroupRE.MatchString(o.NginxUserGroup) {
		diskLog.Error("invalid NGINX_USER_GROUP format, expected user:group", "value", o.NginxUserGroup)
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
	user := strings.Split(o.NginxUserGroup, ":")[0]
	if run(5*time.Second, "id", user) == nil {
		run(10*time.Second, "chown", o.NginxUserGroup, o.NginxLogDir)
	}
	for _, name := range []string{"error.log", "access.log"} {
		p := filepath.Join(o.NginxLogDir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if f, err := os.Create(p); err == nil {
				f.Close()
				run(10*time.Second, "chown", o.NginxUserGroup, p)
			}
		}
	}
	for attempt := range 3 {
		if run(30*time.Second, "systemctl", "restart", o.NginxServiceName) == nil {
			diskLog.Info("    " + o.NginxServiceName + " restarted successfully")
			return
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	diskLog.Warn("    could not restart " + o.NginxServiceName)
}

// DirSize sums file sizes under path (without following symlinks), stopping at the deadline.
func DirSize(path string, timeout time.Duration) (size int64, partial bool) {
	deadline := time.Now().Add(timeout)
	errStop := errors.New("stop")
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if timeout > 0 && time.Now().After(deadline) {
			return errStop
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size, errors.Is(err, errStop)
}

// DirEntry is one subdirectory and its size.
type DirEntry struct {
	Path string
	Size int64
}

// LargestSubdirs sizes each immediate subdirectory of parent, largest first.
func LargestSubdirs(parent string, limit int, perDirTimeout time.Duration) []DirEntry {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(parent, e.Name())
		if parent == "/" && (e.Name() == "proc" || e.Name() == "sys" || e.Name() == "dev" || e.Name() == "run") {
			continue
		}
		size, _ := DirSize(p, perDirTimeout)
		out = append(out, DirEntry{p, size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
