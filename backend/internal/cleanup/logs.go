// Package cleanup frees disk space: log trimming and rotation cleanup, stale temp files and package caches.
package cleanup

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"nightjar/internal/format"
	"nightjar/internal/logx"
)

var logsLog = logx.Component("Logs")

const secondsPerDay = 86400.0

var (
	rotatedLogRE  = regexp.MustCompile(`(?i)(?:\.\d+(?:\.(?:gz|bz2|xz|zst|zip))?$)|(?:\.(?:gz|bz2|xz|zst)$)|(?:\.old$)|(?:-\d{8}$)`)
	databaseLogRE = regexp.MustCompile(`(?i)(?:-bin|-relay-bin|binlog)\.\d+$`)
	// ipSuffixRE is a name ending in an IPv4 address, as Samba names its live per-client logs
	// (log.192.168.1.10); the trailing ".10" is no rotation number.
	ipSuffixRE    = regexp.MustCompile(`(?:^|\.)\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)
	protectedLogs = map[string]bool{"auth.log": true, "secure": true, "audit.log": true, "btmp": true, "wtmp": true, "lastlog": true, "faillog": true}
)

// LogInfo is one log file (or the journal directory) and its size.
type LogInfo struct {
	Path         string
	Size         int64
	ModTime      time.Time
	AgeDays      float64
	OriginalSize int64
}

// rotatedBase is a rotated log's name without its rotation suffix: kern.log for kern.log.2.gz.
// Suffixes stack (dateext then compress gives auth.log-20240115.gz), so they come off until none is left.
func rotatedBase(name string) string {
	for {
		base := rotatedLogRE.ReplaceAllString(name, "")
		if base == name || base == "" {
			return name
		}
		name = base
	}
}

// looksLikeLog says whether a file name reads as a log rather than, say, a library (libc.so.6) or
// a kernel module (*.ko.zst) that happens to end the way a rotated log does.
func looksLikeLog(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, ".so.") || strings.HasSuffix(lower, ".so") || strings.HasSuffix(lower, ".ko") {
		return false
	}
	for _, kw := range append([]string{"dmesg", "debug"}, logKeywords...) {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return protectedLogs[lower]
}

// IsRotatedLog matches syslog.1, kern.log.2.gz, daemon.log.old, syslog-20240115 and bare archives,
// but never database binlogs (mysql-bin.000123), nor a file whose name, rotation suffix aside, is
// not a log's.
func IsRotatedLog(path string) bool {
	name := filepath.Base(path)
	if databaseLogRE.MatchString(name) || ipSuffixRE.MatchString(name) || !rotatedLogRE.MatchString(name) {
		return false
	}
	return looksLikeLog(rotatedBase(name))
}

// isProtectedLog is a security or audit log, live or rotated (auth.log, auth.log.2.gz, wtmp.1,
// anything under an audit directory): those are only ever trimmed, never emptied or deleted.
func isProtectedLog(path string) bool {
	name := filepath.Base(path)
	if protectedLogs[name] || protectedLogs[rotatedBase(name)] {
		return true
	}
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "audit")
}

// LogMonitor finds, trims and removes log files.
type LogMonitor struct {
	AppLog         string
	ThresholdBytes int64
	SystemLogs     []string
	JournalPath    string
	RetentionDays  float64
	LogDir         string
}

func NewLogMonitor(appLog string, thresholdMB, retentionDays float64) *LogMonitor {
	m := &LogMonitor{
		AppLog:         appLog,
		ThresholdBytes: int64(thresholdMB * 1024 * 1024),
		SystemLogs:     []string{"/var/log/syslog", "/var/log/messages", "/var/log/kern.log", "/var/log/auth.log", "/var/log/daemon.log"},
		RetentionDays:  min(365, max(0, retentionDays)),
		LogDir:         "/var/log",
	}
	if runtime.GOOS == "linux" {
		m.JournalPath = "/var/log/journal"
	}
	return m
}

func (m *LogMonitor) ThresholdFormatted() string { return format.Bytes(float64(m.ThresholdBytes)) }

// Size stats one file.
func (m *LogMonitor) Size(path string) (LogInfo, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return LogInfo{}, false
	}
	return LogInfo{Path: path, Size: st.Size(), ModTime: st.ModTime()}, true
}

// JournalSize sums the journal directory.
func (m *LogMonitor) JournalSize() (LogInfo, bool) {
	if m.JournalPath == "" {
		return LogInfo{}, false
	}
	var total int64
	found := false
	filepath.WalkDir(m.JournalPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		found = true
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return LogInfo{Path: m.JournalPath, Size: total}, found
}

// RotatedLogs lists rotated archives under the log directory. Security and audit archives are
// left out: they are the trail an intrusion is traced by.
func (m *LogMonitor) RotatedLogs() []LogInfo {
	var out []LogInfo
	now := time.Now()
	filepath.WalkDir(m.LogDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != m.LogDir && (d.Name() == "journal" || d.Name() == "private" || d.Name() == "audit") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !IsRotatedLog(d.Name()) || isProtectedLog(path) {
			return nil
		}
		if info, ok := m.Size(path); ok {
			info.AgeDays = now.Sub(info.ModTime).Seconds() / secondsPerDay
			out = append(out, info)
		}
		return nil
	})
	return out
}

func (m *LogMonitor) shouldDeleteRotated(info LogInfo, force bool) bool {
	return force || info.Size > m.ThresholdBytes || info.AgeDays >= m.RetentionDays
}

// Stats summarises one cleanup pass.
type Stats struct {
	FilesRemoved int
	DirsRemoved  int
	LogsCleaned  int
	BytesCleared int64
	Errors       []string
	InitialUsage *float64
	FinalUsage   *float64
}

func (s *Stats) add(o Stats) {
	s.FilesRemoved += o.FilesRemoved
	s.DirsRemoved += o.DirsRemoved
	s.LogsCleaned += o.LogsCleaned
	s.BytesCleared += o.BytesCleared
	s.Errors = append(s.Errors, o.Errors...)
}

// CleanupRotated removes rotated archives that are too old, too large, or all of them when forced.
func (m *LogMonitor) CleanupRotated(force bool) Stats {
	var st Stats
	for _, info := range m.RotatedLogs() {
		if !m.shouldDeleteRotated(info, force) {
			continue
		}
		if err := m.removeLog(info.Path); err != nil && !os.IsNotExist(err) {
			logsLog.Error("could not remove rotated log", "path", info.Path, "err", err)
			st.Errors = append(st.Errors, "could not remove rotated log "+info.Path)
			continue
		}
		logsLog.Info("removed rotated log", "path", info.Path, "size", format.Bytes(float64(info.Size)))
		st.FilesRemoved++
		st.BytesCleared += info.Size
	}
	if st.FilesRemoved > 0 {
		logsLog.Info(fmt.Sprintf("[CLEANUP] Removed %d rotated log archive(s), freed %s", st.FilesRemoved, format.Bytes(float64(st.BytesCleared))))
	}
	return st
}

var logKeywords = []string{"log", "syslog", "messages", "kern", "auth", "daemon", "mail", "apache", "nginx", "mysql", "postgresql"}

// AllLogFiles lists everything under the log directory that looks like a log.
func (m *LogMonitor) AllLogFiles() []LogInfo {
	var out []LogInfo
	skip := map[string]bool{"journal": true, "private": true, "apt": true, "dist-upgrade": true}
	filepath.WalkDir(m.LogDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != m.LogDir && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(d.Name())
		if !d.Type().IsRegular() || databaseLogRE.MatchString(d.Name()) {
			return nil
		}
		for _, ext := range []string{".lock", ".pid", ".tmp", ".swp"} {
			if strings.HasSuffix(name, ext) {
				return nil
			}
		}
		isLog := strings.Contains(name, ".log")
		for _, kw := range logKeywords {
			isLog = isLog || strings.Contains(name, kw)
		}
		if isLog {
			if info, ok := m.Size(path); ok {
				out = append(out, info)
			}
		}
		return nil
	})
	return out
}

// Sizes is the periodic log survey.
type Sizes struct {
	App      *LogInfo
	System   []LogInfo
	Journal  *LogInfo
	Rotated  []LogInfo
	Exceeded []LogInfo
}

func (m *LogMonitor) Survey() Sizes {
	var s Sizes
	if info, ok := m.Size(m.AppLog); ok {
		s.App = &info
		if info.Size > m.ThresholdBytes {
			s.Exceeded = append(s.Exceeded, info)
		}
	}
	for _, p := range m.SystemLogs {
		if info, ok := m.Size(p); ok {
			s.System = append(s.System, info)
			if info.Size > m.ThresholdBytes {
				s.Exceeded = append(s.Exceeded, info)
			}
		}
	}
	if info, ok := m.JournalSize(); ok {
		s.Journal = &info
		if info.Size > m.ThresholdBytes {
			s.Exceeded = append(s.Exceeded, info)
		}
	}
	s.Rotated = m.RotatedLogs()
	for _, info := range s.Rotated {
		if m.shouldDeleteRotated(info, false) {
			s.Exceeded = append(s.Exceeded, info)
		}
	}
	return s
}

// LargestPerPath keeps one entry per path, the largest.
func LargestPerPath(logs []LogInfo) []LogInfo {
	byPath := map[string]LogInfo{}
	var order []string
	for _, l := range logs {
		prev, ok := byPath[l.Path]
		if !ok {
			order = append(order, l.Path)
		}
		if !ok || l.Size > prev.Size {
			byPath[l.Path] = l
		}
	}
	out := make([]LogInfo, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

// Clean trims one log to its most recent ThresholdBytes, or empties it when forced
// (protected security logs are only ever trimmed). Rotated archives are deleted, except
// protected ones, which are kept whole, and the journal is vacuumed with journalctl.
func (m *LogMonitor) Clean(info LogInfo, force bool) bool {
	if IsRotatedLog(info.Path) {
		info.AgeDays = time.Since(info.ModTime).Seconds() / secondsPerDay
		if isProtectedLog(info.Path) || !m.shouldDeleteRotated(info, force) {
			return true
		}
		err := m.removeLog(info.Path)
		return err == nil || os.IsNotExist(err)
	}
	if !force && info.Size <= m.ThresholdBytes {
		return true
	}
	if m.JournalPath != "" && (info.Path == m.JournalPath || strings.HasPrefix(info.Path, m.JournalPath+"/")) {
		return m.vacuumJournal(force)
	}
	return m.cleanRegular(info.Path, force)
}

// rootFor opens the directory a log is acted on through: the log directory when the log is inside
// it, else the log's own directory. Every delete and open then goes through os.Root, as CleanTree's
// do, so a directory swapped for a symlink after the walk listed it cannot send a delete or a
// truncate outside (to /usr/lib, say).
func (m *LogMonitor) rootFor(path string) (*os.Root, string, error) {
	if m.LogDir != "" {
		if rel, err := filepath.Rel(m.LogDir, path); err == nil && rel != "." && filepath.IsLocal(rel) {
			r, err := os.OpenRoot(m.LogDir)
			return r, rel, err
		}
	}
	r, err := os.OpenRoot(filepath.Dir(path))
	return r, filepath.Base(path), err
}

// removeLog deletes one regular log file without following links on the way to it.
func (m *LogMonitor) removeLog(path string) error {
	r, name, err := m.rootFor(path)
	if err != nil {
		return err
	}
	defer r.Close()
	st, err := r.Lstat(name)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return r.Remove(name)
}

func (m *LogMonitor) openRegular(path string) (*os.File, error) {
	r, name, err := m.rootFor(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

func (m *LogMonitor) cleanRegular(path string, force bool) bool {
	f, err := m.openRegular(path)
	if err != nil {
		logsLog.Error("could not open log", "path", path, "err", err)
		return false
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	if size == 0 {
		return true
	}
	if force && !isProtectedLog(path) {
		if err := f.Truncate(0); err != nil {
			logsLog.Error("could not clear log", "path", path, "err", err)
			return false
		}
		logsLog.Info(fmt.Sprintf("Emergency: Cleared log %s (%s)", path, format.Bytes(float64(size))))
		return true
	}
	if size <= m.ThresholdBytes {
		return true
	}
	// Shift the newest ThresholdBytes to the front in place, then truncate.
	readPos, writePos := size-m.ThresholdBytes, int64(0)
	buf := make([]byte, 1<<20)
	for {
		n, err := f.ReadAt(buf, readPos)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], writePos); werr != nil {
				logsLog.Error("could not trim log", "path", path, "err", werr)
				return false
			}
			readPos += int64(n)
			writePos += int64(n)
		}
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			logsLog.Error("could not trim log", "path", path, "err", err)
			return false
		}
	}
	if err := f.Truncate(writePos); err != nil {
		return false
	}
	logsLog.Info(fmt.Sprintf("Log trimmed: %s from %s to %s (kept most recent %s)", path,
		format.Bytes(float64(size)), format.Bytes(float64(writePos)), format.Bytes(float64(m.ThresholdBytes))))
	return true
}

// vacuumJournal shrinks the journal to the log threshold, or, when forced, to at most 100M: never
// less aggressive than a routine pass, and never below 1M.
func (m *LogMonitor) vacuumJournal(force bool) bool {
	mb := m.ThresholdBytes / (1024 * 1024)
	if force {
		mb = min(mb, 100)
	}
	size := fmt.Sprintf("%dM", max(1, mb))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "journalctl", "--vacuum-size", size).Run(); err != nil {
		return false
	}
	if force {
		logsLog.Info("Emergency: aggressively cleaned journal logs", "size", size)
	}
	return true
}
