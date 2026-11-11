// Package kernel watches for kernel crashes, hardware errors, instability and unexpected reboots.
package kernel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	"kernel panic", "kernel oops", "kernel crash", "kernel bug at", "bug:", "panic:", "oops:", "crash:",
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
		if HasKeyword(lower, strings.ToLower(k)) {
			out = append(out, k)
		}
	}
	return out
}

// HasKeyword says whether a lowercase line has a keyword where a word starts: "bug:" in "kernel
// bug: ..." but not in "ollama_debug:info", and "panic" but not "nopanic".
func HasKeyword(lower, kw string) bool {
	for from := 0; ; {
		i := strings.Index(lower[from:], kw)
		if i < 0 {
			return false
		}
		i += from
		if i == 0 || !isWordByte(lower[i-1]) {
			return true
		}
		from = i + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// KernelLogCrash finds a crash keyword in a line of a syslog file. Only the kernel's own lines
// count: the same files hold every program's log, and an app's words are not a kernel crash.
// (A program can still tag itself "kernel" in syslog and messages, so only kern.log is read.)
func KernelLogCrash(line string) (string, bool) {
	if !strings.Contains(line, " kernel: ") {
		return "", false
	}
	lower := strings.ToLower(line)
	if isSelf(lower) {
		return "", false
	}
	for _, kw := range kernelLogCriticalKeywords {
		if HasKeyword(lower, kw) {
			return kw, true
		}
	}
	return "", false
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
	// Clean is a reboot the previous boot's journal shows it shut down for: a planned one. Known
	// is false when that journal could not be read.
	Clean, Known bool
}

// shutdownMarkers are what systemd logs as it shuts a machine down on purpose.
var shutdownMarkers = []string{
	"reached target system reboot", "reached target system power off", "reached target system shutdown",
	"reached target reboot", "reached target power-off", "reached target shutdown",
	"system is rebooting", "system is powering down", "system is powering off", "shutting down.",
}

// CleanShutdown says whether the end of a boot's journal shows it shutting down on purpose.
func CleanShutdown(tail string) bool {
	lower := strings.ToLower(tail)
	for _, m := range shutdownMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// previousShutdown reads the end of the previous boot's journal for a clean shutdown.
func previousShutdown() (clean, known bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "-b", "-1", "-n", "300", "--no-pager", "-o", "cat").Output()
	if err != nil || len(out) == 0 {
		return false, false
	}
	return CleanShutdown(string(out)), true
}

// CrashReport is one crash-check pass.
type CrashReport struct {
	Crashes        []Event
	EarlyWarnings  []Event
	HardwareErrors []Event
	Reboot         *Reboot
	// Flood is the kernel writing far more than it should; while it lasts the journal checks are
	// skipped, since they would only time out.
	Flood *Flood
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
	// LastOOMKills is the kernel's OOM-kill count at the last stability check, so only new kills
	// score; nil until the first check, which takes it as the baseline.
	LastOOMKills *int64  `json:"last_oom_kills,omitempty"`
	SavedAt      float64 `json:"saved_at"`
}

const maxDetected = 10000

// maxJournalEntries caps what one journal read takes in.
const maxJournalEntries = 2000

// Monitor keeps per-boot state in a JSON file so device error counts and reboots survive restarts.
type Monitor struct {
	linux          bool
	persistence    string
	ignoredDevices map[string]bool
	bootTime       float64
	detected       map[string]bool
	state          tracking
	pendingReboot  *Reboot
	// lastJournal is when the journal was last read in full, so the next read starts there.
	lastJournal time.Time
	// reported is each crash message already alerted on and when, so one crash seen in the journal,
	// dmesg and kern.log is told once.
	reported map[string]time.Time
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
		reported:       map[string]time.Time{},
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

// resetTracking starts a new boot's counters: every kernel counter restarts at zero, so every OOM
// kill it counts from here is new.
func (m *Monitor) resetTracking() {
	m.state.LastIOErrors = map[string]int64{}
	m.state.LastAlertTime = map[string]float64{}
	m.state.LastEdacCounts = map[string]int64{}
	m.state.LastOOMKills = new(int64(0))
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
		m.resetTracking()
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
	m.state.LastOOMKills = saved.LastOOMKills
	// Older versions kept one EDAC counter per file name ("edac_ce_count") for every controller;
	// those are meaningless now that each controller has its own.
	for key := range m.state.LastEdacCounts {
		if !strings.Contains(key, "/") {
			delete(m.state.LastEdacCounts, key)
		}
	}
	for key := range m.state.LastAlertTime {
		if strings.HasPrefix(key, "edac_") && !strings.Contains(key, "/") {
			delete(m.state.LastAlertTime, key)
		}
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

// journalEntries reads the kernel's journal lines since sinceMinutes ago, newest first. complete is
// false when journalctl could not run, failed or timed out, so the span has to be read again.
func journalEntries(sinceMinutes int, priority, pattern string) (entries []journalEntry, complete bool) {
	since := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute).Format("2006-01-02 15:04:05")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Only what the kernel itself logged, across boots, so a crash before a reboot still shows.
	// Newest first, so when there is more than maxJournalEntries it is the oldest that is left out.
	cmd := exec.CommandContext(ctx, "journalctl", "_TRANSPORT=kernel", "--since", since, "--priority", priority, "--grep", pattern, "--reverse", "--no-pager", "--output", "json")
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		if !errors.Is(err, exec.ErrNotFound) { // no journal at all is no news: dmesg and the logs are read
			journalProblem(err.Error())
		}
		return nil, false
	}
	// Read as it comes and stop at maxJournalEntries, so a busy journal cannot fill memory.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && len(entries) < maxJournalEntries {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	stopped := len(entries) >= maxJournalEntries || sc.Err() != nil
	cancel() // stops journalctl if it has more to say
	waitErr := cmd.Wait()
	if timedOut && len(entries) == 0 {
		// A flooded journal makes this slow; say so rather than report "nothing found".
		log.Warn("journalctl timed out after 15s; these kernel checks were skipped (is something flooding the journal?)", "priority", priority)
	}
	if timedOut {
		return entries, false
	}
	complete, problem := journalOutcome(waitErr, stderr.String(), stopped, sc.Err() != nil)
	journalProblem(problem)
	return entries, complete
}

// journalOutcome judges how a journalctl read ended. With --grep, journalctl exits 1 when nothing
// matches, saying nothing: a read that is complete. Exiting with something on stderr is a failure
// (a journalctl built without --grep, a pattern it cannot read, a journal it cannot open), as is being
// stopped any other way than by this side having read enough. complete is false then, so the span is
// read again, and problem says why.
func journalOutcome(waitErr error, stderr string, stoppedEarly, readFailed bool) (complete bool, problem string) {
	stderr = strings.TrimSpace(stderr)
	var exit *exec.ExitError
	switch {
	case readFailed:
		return false, "journalctl's output could not be read"
	case waitErr == nil:
		return true, ""
	case stoppedEarly:
		return true, "" // stopped here, having read all that is kept
	case errors.As(waitErr, &exit) && exit.ExitCode() == 1 && stderr == "":
		return true, ""
	case stderr != "":
		lines := strings.Split(stderr, "\n")
		return false, strings.TrimSpace(lines[len(lines)-1])
	}
	return false, waitErr.Error()
}

// journalFailure is the last journalctl failure said in the log, so each is said once rather than at
// every check, and a new one, or the same after it worked again, is said again.
var journalFailure struct {
	sync.Mutex
	last string
}

func journalProblem(problem string) {
	journalFailure.Lock()
	defer journalFailure.Unlock()
	if problem != "" && problem != journalFailure.last {
		log.Warn("journalctl could not read the kernel's journal; crashes and early warnings there are not seen until it can", "err", problem)
	}
	journalFailure.last = problem
}

// limitedWriter keeps the first n bytes written to it and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// message is the entry's text. journalctl writes a message that is not valid UTF-8, or holds control
// bytes, as an array of byte values; those are put back together, invalid bytes replaced, so crash
// keywords in them are still found.
func (e journalEntry) message() string {
	switch m := e.Message.(type) {
	case string:
		return m
	case []any:
		b := make([]byte, 0, len(m))
		for _, v := range m {
			n, ok := v.(float64)
			if !ok || n < 0 || n > 255 {
				return fmt.Sprint(e.Message)
			}
			b = append(b, byte(n))
		}
		return strings.ToValidUTF8(string(b), "\uFFFD")
	case nil:
		return ""
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

func (m *Monitor) earlyWarnings(sinceMinutes int) ([]Event, bool) {
	var out []Event
	entries, complete := journalEntries(sinceMinutes, "0..4", grepPattern(earlyWarningKeywords))
	for _, e := range entries {
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
	return out, complete
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

// edacRoot is where the kernel lists its memory controllers.
const edacRoot = "/sys/devices/system/edac/mc"

// EdacCounts reads each memory controller's total correctable and uncorrectable error counts under
// root, keyed "mc0/ce_count". Only the controller totals: the per-csrow and per-DIMM files under
// them count the same errors again. A missing root (no EDAC driver) has none.
func EdacCounts(root string) map[string]int64 {
	out := map[string]int64{}
	controllers, _ := os.ReadDir(root)
	for _, mc := range controllers {
		if !strings.HasPrefix(mc.Name(), "mc") {
			continue
		}
		for _, name := range []string{"ce_count", "ue_count"} {
			raw, err := os.ReadFile(filepath.Join(root, mc.Name(), name))
			if err != nil {
				continue
			}
			if count, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
				out[mc.Name()+"/"+name] = count
			}
		}
	}
	return out
}

// edacErrors reports growth in the memory controllers' correctable and uncorrectable error counts:
// any uncorrectable error, or 1000+ (or 10%+) new correctable ones, at most once a day per counter.
func (m *Monitor) edacErrors() []Event {
	var out []Event
	now := float64(time.Now().Unix())
	const cooldown = 24 * 3600
	counts := EdacCounts(edacRoot)
	for _, counter := range slices.Sorted(maps.Keys(counts)) {
		count := counts[counter]
		mc, name, _ := strings.Cut(counter, "/")
		key := "edac_" + counter
		last := m.state.LastEdacCounts[key]
		increase := count - last
		m.state.LastEdacCounts[key] = count
		kind := "uncorrectable"
		if name == "ce_count" {
			kind = "correctable"
		}
		significant := kind == "uncorrectable" || increase >= 1000 || (last > 0 && float64(increase) >= float64(last)*0.1)
		if increase <= 0 || !significant || now-m.state.LastAlertTime[key] < cooldown {
			continue
		}
		out = append(out, Event{
			Type: "hardware_error", ErrorType: kind, Source: "Memory Controller " + mc, Count: count, CountIncrease: increase,
			Message: fmt.Sprintf("%s%s memory error detected: %d new errors (total: %d)", strings.ToUpper(kind[:1]), kind[1:], increase, count),
		})
		m.state.LastAlertTime[key] = now
	}
	return out
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
	// Only kills since the last check: the counter runs from boot, and one OOM kill weeks ago
	// says nothing about now.
	if n, ok := oomKills(); ok {
		if last := m.state.LastOOMKills; last != nil && n > *last {
			s.add(25, fmt.Sprintf("Critical: OOM killer has killed %d process(es)", n-*last), "OOM killer active - system memory critically low")
		}
		m.state.LastOOMKills = &n
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
func oomKills() (int64, bool) {
	data, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
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

// journalCrashes is one event per crash line: a line with two keywords ("BUG: Unable to handle
// ...") is still one crash.
func (m *Monitor) journalCrashes(sinceMinutes int) ([]Event, bool) {
	var out []Event
	entries, complete := journalEntries(sinceMinutes, "err", grepPattern(journalCrashKeywords))
	for _, e := range entries {
		msg := e.message()
		lower := strings.ToLower(msg)
		if isSelf(lower) {
			continue
		}
		if kws := matching(lower, journalCrashKeywords); len(kws) > 0 && !m.seen(e.Timestamp+"_"+kws[0]) {
			out = append(out, Event{Type: "kernel_error", Keyword: kws[0], Message: msg, Source: "journalctl"})
		}
	}
	return out, complete
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
	// Panics, oopses and BUGs are logged at emerg, alert and crit, above err.
	out, err := exec.CommandContext(ctx, "dmesg", "-T", "--level=emerg,alert,crit,err,warn").Output()
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
		if isSelf(lower) || (containsAny(lower, dmesgIgnoreFragments) && len(matching(lower, dmesgCriticalKeywords)) == 0) {
			continue
		}
		for _, kw := range dmesgCriticalKeywords {
			if HasKeyword(lower, kw) {
				if !m.seen("dmesg_" + line) {
					events = append(events, Event{Type: "kernel_error", Keyword: kw, Message: line, Source: "dmesg"})
				}
				break
			}
		}
	}
	return events
}

// SyslogTime reads the timestamp a syslog line starts with: RFC 3339 ("2026-11-05T01:40:00.1+00:00")
// or the traditional "Nov  5 01:40:00", which has no year and is taken as the latest one not ahead
// of now.
func SyslogTime(line string, now time.Time) (time.Time, bool) {
	if f, _, ok := strings.Cut(line, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, f); err == nil {
			return t, true
		}
	}
	if len(line) < 15 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Jan _2 15:04:05", line[:15], now.Location())
	if err != nil {
		return time.Time{}, false
	}
	t = t.AddDate(now.Year(), 0, 0)
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, true
}

// kernelLogCrashes reads kern.log's crash lines from the last sinceMinutes. Only kern.log: syslog and
// messages take any program's lines, and `logger -t kernel` can put a fake crash there.
func (m *Monitor) kernelLogCrashes(sinceMinutes int) []Event {
	now := time.Now()
	cutoff := now.Add(-time.Duration(sinceMinutes) * time.Minute)
	var out []Event
	const path = "/var/log/kern.log"
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Before(cutoff) {
		return nil
	}
	lines, err := tailLines(path, 512*1024)
	if err != nil {
		return nil
	}
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		// The file's tail can hold crashes from days ago; those were told when they happened.
		if t, ok := SyslogTime(line, now); ok && t.Before(cutoff) {
			break
		}
		if kw, ok := KernelLogCrash(line); ok && !m.seen(path+"_"+line) {
			out = append(out, Event{Type: "kernel_error", Keyword: kw, Message: strings.TrimSpace(line), Source: path})
		}
	}
	return out
}

// crashText is a crash line without its source's framing (a syslog prefix, dmesg's or printk's
// [timestamp]), so the same line from the journal, dmesg and kern.log compares equal.
func crashText(msg string) string {
	if _, rest, ok := strings.Cut(msg, " kernel: "); ok {
		msg = rest
	}
	msg = strings.TrimSpace(msg)
	if strings.HasPrefix(msg, "[") {
		if i := strings.Index(msg, "]"); i > 0 {
			msg = msg[i+1:]
		}
	}
	return strings.ToLower(strings.TrimSpace(msg))
}

// newCrashes drops crashes already told from another source (or twice in one pass). A message is
// remembered for twice the look-back, past which no source reads it again.
func (m *Monitor) newCrashes(crashes []Event, sinceMinutes int, now time.Time) []Event {
	keep := 2 * time.Duration(sinceMinutes) * time.Minute
	for text, at := range m.reported {
		if now.Sub(at) > keep {
			delete(m.reported, text)
		}
	}
	var out []Event
	for _, c := range crashes {
		text := crashText(c.Message)
		if _, told := m.reported[text]; told {
			continue
		}
		m.reported[text] = now
		out = append(out, c)
	}
	return out
}

// CheckReboot reports a boot-time change since the last call or the last run, once.
func (m *Monitor) CheckReboot() *Reboot {
	if m.pendingReboot != nil {
		r := m.pendingReboot
		m.pendingReboot = nil
		r.Clean, r.Known = previousShutdown()
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
		r.Clean, r.Known = previousShutdown()
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
	r.Flood = flooding()
	// Each check reads the journal only since the last one (the window is the most it reads); what
	// overlaps is already seen.
	window := sinceMinutes
	if !m.lastJournal.IsZero() {
		window = min(sinceMinutes, int(time.Since(m.lastJournal).Minutes())+2)
	}
	if r.Flood == nil {
		started := time.Now()
		warnings, warningsRead := m.earlyWarnings(window)
		crashes, crashesRead := m.journalCrashes(window)
		r.EarlyWarnings = warnings
		r.Crashes = append(r.Crashes, crashes...)
		// A read that timed out is tried again from the same place next time; what it did get is
		// already marked seen.
		if warningsRead && crashesRead {
			m.lastJournal = started
		}
	}
	r.HardwareErrors = m.hardwareErrors()
	r.Crashes = append(r.Crashes, m.dmesgCrashes(sinceMinutes)...)
	r.Crashes = append(r.Crashes, m.kernelLogCrashes(sinceMinutes)...)
	r.Crashes = m.newCrashes(r.Crashes, sinceMinutes, time.Now())
	return r
}
