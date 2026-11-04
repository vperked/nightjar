// Package kernel watches for kernel crashes, hardware errors, instability and unexpected reboots.
package kernel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"nightjar/internal/logx"
)

var log = logx.Component("Kernel")

var earlyWarningKeywords = []string{
	"mce:", "machine check", "edac error", "corrected error", "uncorrected error", "memory error", "ecc error",
	"hardware error", "mcelog", "oom killer", "out of memory", "memory allocation failure", "slab allocation",
	"page allocation failure", "swap", "memory pressure", "i/o error", "disk error", "sector", "bad block",
	"smart error", "filesystem error", "ext4 error", "xfs error", "btrfs error", "driver error", "device error",
	"hardware failure", "pci error", "usb error", "sata error", "nvme error", "ahci error", "soft lockup",
	"hard lockup", "watchdog", "hung task", "stuck", "rcu stall", "rcu self-detected stall", "rcu detected stall",
	"thermal", "overheating", "temperature", "power", "voltage", "throttling", "thermal shutdown",
	"filesystem corruption", "superblock", "journal", "metadata", "network error", "ethernet error", "link down",
	"phy error", "call trace", "stack trace", "backtrace", "warn:", "error:", "failed", "failure", "timeout", "deadlock",
}

var journalCrashKeywords = []string{
	"kernel panic", "kernel oops", "kernel crash", "BUG:", "WARNING:", "general protection fault",
	"segmentation fault", "double fault", "unhandled page fault", "kernel BUG", "Unable to handle",
}

var kernelLogCriticalKeywords = []string{
	"kernel panic", "kernel oops", "kernel crash", "bug:", "panic:", "oops:", "crash:",
	"general protection fault", "segmentation fault", "double fault", "unhandled page fault",
}

var dmesgCriticalKeywords = []string{"panic", "oops", "bug:", "crash", "segmentation fault", "general protection fault", "double fault", "unhandled page fault"}
var dmesgIgnoreFragments = []string{"failed:", "failed (", "error -", "warn:", "warning:"}

func grepPattern(keywords []string) string {
	quoted := make([]string, len(keywords))
	for i, k := range keywords {
		quoted[i] = regexp.QuoteMeta(k)
	}
	return strings.Join(quoted, "|")
}

func matching(lower string, keywords []string) []string {
	var out []string
	for _, k := range keywords {
		if strings.Contains(lower, strings.ToLower(k)) {
			out = append(out, k)
		}
	}
	return out
}

// Event is one crash, warning or hardware error.
type Event struct {
	Type          string `json:"type"`
	Severity      string `json:"severity,omitempty"`
	Keyword       string `json:"keyword,omitempty"`
	Message       string `json:"message"`
	Source        string `json:"source"`
	ErrorType     string `json:"error_type,omitempty"`
	Device        string `json:"device,omitempty"`
	Count         int64  `json:"count,omitempty"`
	CountIncrease int64  `json:"count_increase,omitempty"`
}

// Reboot is a detected boot-time change.
type Reboot struct {
	PreviousBootTime float64
	CurrentBootTime  float64
}

// CrashReport is one crash-check pass.
type CrashReport struct {
	Crashes        []Event
	EarlyWarnings  []Event
	HardwareErrors []Event
	Reboot         *Reboot
}

// Stability is one stability-check pass.
type Stability struct {
	RiskLevel       string
	RiskScore       int
	Indicators      []string
	Recommendations []string
}

type tracking struct {
	BootTime       float64            `json:"boot_time"`
	LastIOErrors   map[string]int64   `json:"last_io_errors"`
	LastAlertTime  map[string]float64 `json:"last_alert_time"`
	LastEdacCounts map[string]int64   `json:"last_edac_counts"`
	SavedAt        float64            `json:"saved_at"`
}

const maxDetected = 10000

// Monitor keeps per-boot state in a JSON file so device error counts and reboots survive restarts.
type Monitor struct {
	linux          bool
	persistence    string
	ignoredDevices map[string]bool
	bootTime       float64
	detected       map[string]bool
	state          tracking
	pendingReboot  *Reboot
}

func bootTime() float64 {
	if t, err := host.BootTime(); err == nil {
		return float64(t)
	}
	return 0
}

// New loads saved tracking data; a boot-time change since the last run queues a reboot alert.
func New(persistence string, ignoredDevices []string) *Monitor {
	if persistence == "" {
		home, _ := os.UserHomeDir()
		persistence = filepath.Join(home, ".nightjar_kernel_tracking.json")
	}
	m := &Monitor{
		linux:          runtime.GOOS == "linux",
		persistence:    persistence,
		ignoredDevices: map[string]bool{},
		detected:       map[string]bool{},
		state:          tracking{LastIOErrors: map[string]int64{}, LastAlertTime: map[string]float64{}, LastEdacCounts: map[string]int64{}},
	}
	for _, d := range ignoredDevices {
		m.ignoredDevices[d] = true
	}
	if len(ignoredDevices) > 0 {
		log.Info("Kernel monitor: ignoring I/O errors on devices: " + strings.Join(ignoredDevices, ", "))
	}
	if !m.linux {
		return m
	}
	m.bootTime = bootTime()
	m.load()
	return m
}

func (m *Monitor) resetTracking() {
	m.state.LastIOErrors = map[string]int64{}
	m.state.LastAlertTime = map[string]float64{}
	m.state.LastEdacCounts = map[string]int64{}
}

func (m *Monitor) load() {
	data, err := os.ReadFile(m.persistence)
	if err != nil {
		return
	}
	var saved tracking
	if err := json.Unmarshal(data, &saved); err != nil {
		log.Warn("Error loading kernel tracking data, starting fresh", "file", m.persistence, "err", err)
		return
	}
	if saved.BootTime != 0 && m.bootTime != 0 && abs(saved.BootTime-m.bootTime) >= 10 {
		log.Info("System rebooted since last run; queueing reboot alert")
		m.pendingReboot = &Reboot{PreviousBootTime: saved.BootTime, CurrentBootTime: m.bootTime}
		m.save()
		return
	}
	if saved.LastIOErrors != nil {
		m.state.LastIOErrors = saved.LastIOErrors
	}
	if saved.LastAlertTime != nil {
		m.state.LastAlertTime = saved.LastAlertTime
	}
	if saved.LastEdacCounts != nil {
		m.state.LastEdacCounts = saved.LastEdacCounts
	}
}

func (m *Monitor) save() {
	m.state.BootTime = m.bootTime
	m.state.SavedAt = float64(time.Now().Unix())
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(m.persistence); dir != "" {
		os.MkdirAll(dir, 0o700)
	}
	tmp := m.persistence + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Warn("Error saving kernel tracking data", "err", err)
		return
	}
	os.Rename(tmp, m.persistence)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func (m *Monitor) seen(id string) bool {
	if m.detected[id] {
		return true
	}
	if len(m.detected) >= maxDetected {
		m.detected = map[string]bool{}
	}
	m.detected[id] = true
	return false
}

func tailLines(path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(0, st.Size()-maxBytes)
	buf := make([]byte, st.Size()-start)
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return strings.Split(strings.ToValidUTF8(string(buf), ""), "\n"), nil
}

type journalEntry struct {
	Message   any    `json:"MESSAGE"`
	Timestamp string `json:"__REALTIME_TIMESTAMP"`
	Priority  string `json:"PRIORITY"`
}

func journalEntries(sinceMinutes int, priority, pattern string) []journalEntry {
	since := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute).Format("2006-01-02 15:04:05")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "--since", since, "--priority", priority, "--grep", pattern, "--no-pager", "--output", "json").Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// A flooded journal makes this slow; say so rather than report "nothing found".
		log.Warn("journalctl timed out after 15s; these kernel checks were skipped (is something flooding the journal?)", "priority", priority)
		return nil
	}
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	var entries []journalEntry
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

func (e journalEntry) message() string {
	if s, ok := e.Message.(string); ok {
		return s
	}
	return fmt.Sprint(e.Message)
}

// isSelf spots the monitor's own lines, under its old names too, so it never reports itself.
func isSelf(lower string) bool {
	for _, name := range []string{"nightjar", "servermonitor", "server-monitor", "server monitor"} {
		if strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

func (m *Monitor) earlyWarnings(sinceMinutes int) []Event {
	var out []Event
	for _, e := range journalEntries(sinceMinutes, "0..4", grepPattern(earlyWarningKeywords)) {
		msg := e.message()
		severity := "warning"
		if p := e.Priority; p == "0" || p == "1" || p == "2" || p == "3" {
			severity = "critical"
		}
		for _, kw := range matching(strings.ToLower(msg), earlyWarningKeywords) {
			if m.seen("warning_" + e.Timestamp + "_" + kw) {
				continue
			}
			out = append(out, Event{Type: "early_warning", Severity: severity, Keyword: kw, Message: msg, Source: "journalctl"})
		}
	}
	return out
}

func (m *Monitor) hardwareErrors() []Event {
	out := m.edacErrors()
	if kb, ok := memAvailableKB(); ok && kb < 100*1024 {
		out = append(out, Event{Type: "memory_pressure", Severity: "critical", Source: "/proc/meminfo", Message: "Very low memory available - system may become unstable"})
	}
	if e := m.recentOOMKill(); e != nil {
		out = append(out, *e)
	}
	return out
}

// edacErrors reports growth in the memory controllers' correctable and uncorrectable error counts:
// any uncorrectable error, or 1000+ (or 10%+) new correctable ones, at most once a day per counter.
func (m *Monitor) edacErrors() []Event {
	var out []Event
	now := float64(time.Now().Unix())
	const cooldown = 24 * 3600
	for _, root := range []string{"/sys/devices/system/edac/mc", "/sys/devices/system/edac"} {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			name := d.Name()
			if err != nil || d.IsDir() || (!strings.Contains(name, "ce_count") && !strings.Contains(name, "ue_count")) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			count, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil {
				return nil
			}
			mc := memoryController(path)
			key := mc + "_" + name
			last := m.state.LastEdacCounts[key]
			increase := count - last
			m.state.LastEdacCounts[key] = count
			kind := "uncorrectable"
			if strings.Contains(name, "ce_count") {
				kind = "correctable"
			}
			significant := kind == "uncorrectable" || increase >= 1000 || (last > 0 && float64(increase) >= float64(last)*0.1)
			if increase <= 0 || !significant || now-m.state.LastAlertTime[key] < cooldown {
				return nil
			}
			source := "EDAC-" + mc
			if strings.Contains(mc, "mc") {
				source = "Memory Controller " + mc
			}
			out = append(out, Event{
				Type: "hardware_error", ErrorType: kind, Source: source, Count: count, CountIncrease: increase,
				Message: fmt.Sprintf("%s%s memory error detected: %d new errors (total: %d)", strings.ToUpper(kind[:1]), kind[1:], increase, count),
			})
			m.state.LastAlertTime[key] = now
			return nil
		})
	}
	return out
}

func memoryController(path string) string {
	for _, part := range strings.Split(filepath.Dir(path), "/") {
		if strings.HasPrefix(part, "mc") || strings.HasPrefix(part, "edac") {
			return part
		}
	}
	return "unknown"
}

// recentOOMKill is the latest unseen OOM-killer line in the last 500 lines of kern.log.
func (m *Monitor) recentOOMKill() *Event {
	lines, err := tailLines("/var/log/kern.log", 256*1024)
	if err != nil {
		return nil
	}
	for i := len(lines) - 1; i >= max(0, len(lines)-500); i-- {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "oom") && strings.Contains(lower, "killer") {
			if m.seen("oom_" + lines[i]) {
				return nil
			}
			return &Event{Type: "oom_killer", Severity: "critical", Message: strings.TrimSpace(lines[i]), Source: "/var/log/kern.log"}
		}
	}
	return nil
}

func memAvailableKB() (int64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				v, err := strconv.ParseInt(parts[1], 10, 64)
				return v, err == nil
			}
		}
	}
	return 0, false
}

func (s *Stability) add(score int, indicator, recommendation string) {
	s.RiskScore += score
	s.Indicators = append(s.Indicators, indicator)
	if recommendation != "" {
		s.Recommendations = append(s.Recommendations, recommendation)
	}
}

// CheckStability scores memory pressure, OOM kills and disk I/O errors into a risk level.
func (m *Monitor) CheckStability() Stability {
	s := Stability{RiskLevel: "low"}
	if !m.linux {
		return s
	}
	if current := bootTime(); current != 0 && m.bootTime != 0 && abs(current-m.bootTime) >= 10 {
		m.resetTracking()
		m.bootTime = current
		m.save()
	}
	if kb, ok := memAvailableKB(); ok {
		switch mb := float64(kb) / 1024; {
		case mb < 50:
			s.add(30, "Critical: Less than 50MB memory available", "Free memory immediately - system at risk of OOM kill")
		case mb < 200:
			s.add(15, "Warning: Less than 200MB memory available", "")
		}
	}
	if n := oomKills(); n > 0 {
		s.add(25, fmt.Sprintf("Critical: OOM killer has killed %d process(es)", n), "OOM killer active - system memory critically low")
	}
	now := float64(time.Now().Unix())
	errs := diskIOErrors()
	for _, device := range slices.Sorted(maps.Keys(errs)) {
		count := errs[device]
		if count > 0 && !m.ignoredDevices[device] && m.shouldAlertDevice(device, count, now) {
			s.add(15, fmt.Sprintf("Warning: %d I/O error(s) on %s", count, device), "Check disk health on "+device)
		}
	}
	m.save()
	switch {
	case s.RiskScore >= 50:
		s.RiskLevel = "critical"
	case s.RiskScore >= 30:
		s.RiskLevel = "high"
	case s.RiskScore >= 15:
		s.RiskLevel = "medium"
	}
	return s
}

// oomKills is the kernel's count of OOM kills since boot.
func oomKills() int {
	data, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// diskIOErrors is each SCSI/SATA disk's lifetime I/O error count (/sys/block/*/device/ioerr_cnt, in hex).
// NVMe drives expose no such counter; their failures reach the kernel log checks instead.
func diskIOErrors() map[string]int64 {
	out := map[string]int64{}
	paths, _ := filepath.Glob("/sys/block/*/device/ioerr_cnt")
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 0, 64); err == nil {
			out[filepath.Base(filepath.Dir(filepath.Dir(p)))] = n
		}
	}
	return out
}

// shouldAlertDevice alerts on first sight, a counter reset, a 50% jump, or (after a day)
// growth of max(100, 10%).
func (m *Monitor) shouldAlertDevice(device string, count int64, now float64) bool {
	last, known := m.state.LastIOErrors[device]
	m.state.LastIOErrors[device] = count
	increase := count - last
	var alert bool
	switch {
	case !known, increase < 0:
		alert = true
	case increase > 0 && increase >= last/2:
		alert = true
	case increase > 0:
		alert = increase >= max(100, last/10) && now-m.state.LastAlertTime[device] >= 24*3600
	}
	if alert {
		m.state.LastAlertTime[device] = now
	}
	return alert
}

func (m *Monitor) journalCrashes(sinceMinutes int) []Event {
	var out []Event
	for _, e := range journalEntries(sinceMinutes, "err", grepPattern(journalCrashKeywords)) {
		msg := e.message()
		lower := strings.ToLower(msg)
		if isSelf(lower) {
			continue
		}
		for _, kw := range matching(lower, journalCrashKeywords) {
			if !m.seen(e.Timestamp + "_" + kw) {
				out = append(out, Event{Type: "kernel_error", Keyword: kw, Message: msg, Source: "journalctl"})
			}
		}
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (m *Monitor) dmesgCrashes(sinceMinutes int) []Event {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dmesg", "-T", "--level=err,warn").Output()
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if i := strings.Index(line, "]"); i > 0 {
			if t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.Trim(line[:i], "[ "), time.Local); err == nil && t.Before(cutoff) {
				continue
			}
		}
		lower := strings.ToLower(line)
		if isSelf(lower) || (containsAny(lower, dmesgIgnoreFragments) && !containsAny(lower, dmesgCriticalKeywords)) {
			continue
		}
		for _, kw := range dmesgCriticalKeywords {
			if strings.Contains(lower, kw) {
				if !m.seen("dmesg_" + line) {
					events = append(events, Event{Type: "kernel_error", Keyword: kw, Message: line, Source: "dmesg"})
				}
				break
			}
		}
	}
	return events
}

func (m *Monitor) kernelLogCrashes(sinceMinutes int) []Event {
	cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	var out []Event
	for _, path := range []string{"/var/log/kern.log", "/var/log/messages", "/var/log/syslog"} {
		st, err := os.Stat(path)
		if err != nil || st.ModTime().Before(cutoff) {
			continue
		}
		lines, err := tailLines(path, 512*1024)
		if err != nil {
			continue
		}
		if len(lines) > 1000 {
			lines = lines[len(lines)-1000:]
		}
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			lower := strings.ToLower(line)
			if strings.TrimSpace(line) == "" || isSelf(lower) {
				continue
			}
			for _, kw := range kernelLogCriticalKeywords {
				if strings.Contains(lower, kw) {
					if !m.seen(path + "_" + line) {
						out = append(out, Event{Type: "kernel_error", Keyword: kw, Message: strings.TrimSpace(line), Source: path})
					}
					break
				}
			}
		}
	}
	return out
}

// CheckReboot reports a boot-time change since the last call or the last run, once.
func (m *Monitor) CheckReboot() *Reboot {
	if m.pendingReboot != nil {
		r := m.pendingReboot
		m.pendingReboot = nil
		return r
	}
	current := bootTime()
	if current == 0 {
		return nil
	}
	if m.bootTime == 0 {
		m.bootTime = current
		return nil
	}
	if abs(current-m.bootTime) >= 10 {
		r := &Reboot{PreviousBootTime: m.bootTime, CurrentBootTime: current}
		m.bootTime = current
		m.save()
		return r
	}
	return nil
}

// CheckCrashes gathers reboots, early warnings, hardware errors and crashes from journalctl, dmesg and kernel logs.
func (m *Monitor) CheckCrashes(sinceMinutes int) CrashReport {
	var r CrashReport
	if !m.linux {
		return r
	}
	r.Reboot = m.CheckReboot()
	r.EarlyWarnings = m.earlyWarnings(sinceMinutes)
	r.HardwareErrors = m.hardwareErrors()
	r.Crashes = append(r.Crashes, m.journalCrashes(sinceMinutes)...)
	r.Crashes = append(r.Crashes, m.dmesgCrashes(sinceMinutes)...)
	r.Crashes = append(r.Crashes, m.kernelLogCrashes(sinceMinutes)...)
	return r
}
