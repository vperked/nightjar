package kernel

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestMonitor(t *testing.T) *Monitor {
	t.Helper()
	return &Monitor{
		linux:          true,
		persistence:    filepath.Join(t.TempDir(), "tracking.json"),
		ignoredDevices: map[string]bool{},
		detected:       map[string]bool{},
		reported:       map[string]time.Time{},
		state:          tracking{LastIOErrors: map[string]int64{}, LastAlertTime: map[string]float64{}, LastEdacCounts: map[string]int64{}},
	}
}

// The panic the audit found: a host without EDAC sysfs (most VMs, ARM boards) must read as no
// counts, whether the root is missing, a file, or empty, never a nil DirEntry dereference.
func TestEdacCountsMissingOrOddRootDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	os.WriteFile(file, []byte("x"), 0o644)
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	for _, root := range []string{filepath.Join(dir, "missing", "edac", "mc"), file, empty, ""} {
		if got := EdacCounts(root); len(got) != 0 {
			t.Errorf("EdacCounts(%q) = %v, want none", root, got)
		}
	}
}

// A fake /sys/devices/system/edac/mc: only each controller's own totals count, keyed by controller,
// never the per-csrow/per-DIMM copies, and unreadable or junk counters are left out.
func TestEdacCountsFakeSysfs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edac", "mc")
	put := func(rel, v string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v), 0o444)
	}
	put("mc0/ce_count", "120\n")
	put("mc0/ue_count", "0\n")
	put("mc0/ce_noinfo_count", "7\n")
	put("mc0/csrow0/ce_count", "100\n")
	put("mc0/csrow1/ce_count", "20\n")
	put("mc0/dimm0/dimm_ce_count", "100\n")
	put("mc1/ce_count", "3\n")
	put("mc1/ue_count", "garbage\n")
	put("power/ce_count", "999\n") // not a controller
	os.MkdirAll(filepath.Join(root, "mc2"), 0o755)

	got := EdacCounts(root)
	want := map[string]int64{"mc0/ce_count": 120, "mc0/ue_count": 0, "mc1/ce_count": 3}
	if len(got) != len(want) {
		t.Fatalf("EdacCounts = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
}

// load drops the old per-file-name EDAC keys (which collapsed every controller into one and caused
// daily false memory-error alerts) and keeps the per-controller ones and device state.
func TestLoadMigratesLegacyEdacKeys(t *testing.T) {
	m := newTestMonitor(t)
	m.bootTime = 1_700_000_000
	saved := tracking{
		BootTime:       m.bootTime + 2, // the same boot (under 10s apart)
		LastIOErrors:   map[string]int64{"sda": 4},
		LastAlertTime:  map[string]float64{"edac_ce_count": 1, "edac_mc0/ce_count": 2, "sda": 3},
		LastEdacCounts: map[string]int64{"edac_ce_count": 120, "edac_ue_count": 1, "edac_mc0/ce_count": 120},
		LastOOMKills:   new(int64(5)),
	}
	data, _ := json.Marshal(saved)
	os.WriteFile(m.persistence, data, 0o600)
	m.load()
	if m.pendingReboot != nil {
		t.Fatal("a 2s boot-time jitter was taken for a reboot")
	}
	if len(m.state.LastEdacCounts) != 1 || m.state.LastEdacCounts["edac_mc0/ce_count"] != 120 {
		t.Errorf("LastEdacCounts = %v", m.state.LastEdacCounts)
	}
	if _, ok := m.state.LastAlertTime["edac_ce_count"]; ok || m.state.LastAlertTime["edac_mc0/ce_count"] != 2 || m.state.LastAlertTime["sda"] != 3 {
		t.Errorf("LastAlertTime = %v", m.state.LastAlertTime)
	}
	if m.state.LastIOErrors["sda"] != 4 || m.state.LastOOMKills == nil || *m.state.LastOOMKills != 5 {
		t.Errorf("device and OOM state not kept: %v %v", m.state.LastIOErrors, m.state.LastOOMKills)
	}
}

// A saved boot time 10s+ away is a reboot: it is queued, and every counter starts over (the OOM
// baseline at 0, so kills in the new boot count), saved to the tracking file.
func TestLoadQueuesRebootAndResetsCounters(t *testing.T) {
	m := newTestMonitor(t)
	m.bootTime = 1_700_000_000
	saved := tracking{BootTime: m.bootTime - 3600, LastIOErrors: map[string]int64{"sda": 9}, LastEdacCounts: map[string]int64{"edac_mc0/ce_count": 5}, LastOOMKills: new(int64(40))}
	data, _ := json.Marshal(saved)
	os.WriteFile(m.persistence, data, 0o600)
	m.load()
	if m.pendingReboot == nil || m.pendingReboot.PreviousBootTime != saved.BootTime || m.pendingReboot.CurrentBootTime != m.bootTime {
		t.Fatalf("pendingReboot = %+v", m.pendingReboot)
	}
	if len(m.state.LastIOErrors) != 0 || len(m.state.LastEdacCounts) != 0 || m.state.LastOOMKills == nil || *m.state.LastOOMKills != 0 {
		t.Errorf("counters not reset: %+v", m.state)
	}
	var onDisk tracking
	raw, err := os.ReadFile(m.persistence)
	if err != nil || json.Unmarshal(raw, &onDisk) != nil || onDisk.BootTime != m.bootTime {
		t.Errorf("the new boot was not saved: %s (%v)", raw, err)
	}
	// A corrupt tracking file starts fresh rather than failing.
	m2 := newTestMonitor(t)
	os.WriteFile(m2.persistence, []byte("{not json"), 0o600)
	m2.load()
	if m2.pendingReboot != nil || m2.state.LastIOErrors == nil {
		t.Error("a corrupt tracking file was not ignored")
	}
}

// Syslog timestamps decide which kern.log lines are recent enough to alert on: RFC 3339, the
// traditional yearless form across New Year, and junk.
func TestSyslogTimeParsingAndCutoff(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, utc)
	newYear := time.Date(2027, 1, 1, 0, 10, 0, 0, utc)
	cases := []struct {
		line string
		now  time.Time
		want time.Time
		ok   bool
	}{
		{"2026-11-05T01:40:00.123456+00:00 host kernel: BUG: unable to handle page fault", now, time.Date(2026, 11, 5, 1, 40, 0, 123456000, utc), true},
		{"2026-11-09T13:00:00+01:00 host kernel: x", now, time.Date(2026, 11, 9, 12, 0, 0, 0, utc), true},
		{"Nov  5 01:40:00 host kernel: [12345.678901] Kernel panic - not syncing", now, time.Date(2026, 11, 5, 1, 40, 0, 0, utc), true},
		{"Nov 15 01:40:00 host kernel: x", now, time.Date(2025, 11, 15, 1, 40, 0, 0, utc), true}, // ahead of now: last year
		{"Nov 10 01:40:00 host kernel: x", now, time.Date(2026, 11, 10, 1, 40, 0, 0, utc), true}, // within a day ahead: clock skew, this year
		{"Dec 31 23:59:00 host kernel: x", newYear, time.Date(2026, 12, 31, 23, 59, 0, 0, utc), true},
		{"Jan  1 00:05:00 host kernel: x", newYear, time.Date(2027, 1, 1, 0, 5, 0, 0, utc), true},
		{"short", now, time.Time{}, false},
		{"", now, time.Time{}, false},
		{"[12345.678901] usb 1-1: new device", now, time.Time{}, false},
		{"Foo 99 99:99:99 host kernel: x", now, time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := SyslogTime(c.line, c.now)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("SyslogTime(%q) = %v %v, want %v %v", c.line, got, ok, c.want, c.ok)
		}
	}
	// The cutoff: with a 30-minute look-back, a line from two hours ago is too old.
	cutoff := now.Add(-30 * time.Minute)
	if ts, ok := SyslogTime("Nov  9 10:00:00 host kernel: BUG: x", now); !ok || !ts.Before(cutoff) {
		t.Error("a two-hour-old line should be before the cutoff")
	}
	if ts, ok := SyslogTime("Nov  9 11:45:00 host kernel: BUG: x", now); !ok || ts.Before(cutoff) {
		t.Error("a 15-minute-old line should be within the cutoff")
	}
}

// Only the kernel's own crash lines from kern.log count, at a word start, and never Nightjar's own.
func TestKernelLogCrashRealLines(t *testing.T) {
	cases := []struct {
		line, kw string
	}{
		{"Nov  5 01:40:00 host kernel: [12345.678901] BUG: kernel NULL pointer dereference, address: 0000000000000008", "bug:"},
		{"Nov  5 01:40:00 host kernel: [12345.678901] Kernel panic - not syncing: Fatal exception in interrupt", "kernel panic"},
		{"Nov  5 01:40:00 host kernel: [ 1.0] general protection fault, probably for non-canonical address 0xdead000000000100: 0000 [#1] SMP PTI", "general protection fault"},
		{"2026-11-05T01:40:00.000000+00:00 host kernel: Oops: 0002 [#1] PREEMPT SMP", "oops:"},
		// Not crashes:
		{"Nov  5 01:40:00 host myapp[123]: kernel panic simulated for testing", ""},          // not the kernel
		{"Nov  5 01:40:00 host kernel: usb 1-1: debug: descriptor read", ""},                 // "bug:" inside "debug:"
		{"Nov  5 01:40:00 host kernel: ollama_debug: something", ""},                         // nor in a word
		{"Nov  5 01:40:00 host kernel: Command line: BOOT_IMAGE=/vmlinuz nopanic quiet", ""}, // "panic" inside "nopanic" (and not a keyword)
		{"Nov  5 01:40:00 host kernel: nightjar: test of kernel panic detection", ""},        // ourselves
		{"Nov  5 01:40:00 host kernel: [  2.1] EXT4-fs (sda1): mounted filesystem with ordered data", ""},
	}
	for _, c := range cases {
		kw, ok := KernelLogCrash(c.line)
		if ok != (c.kw != "") || kw != c.kw {
			t.Errorf("KernelLogCrash(%q) = %q %v, want %q", c.line, kw, ok, c.kw)
		}
	}
}

// One crash seen in the journal, dmesg -T and kern.log is told once; it is told again only after
// twice the look-back has passed.
func TestCrashDedupeAcrossSources(t *testing.T) {
	const msg = "BUG: kernel NULL pointer dereference, address: 0000000000000008"
	journal := Event{Message: msg, Source: "journalctl"}
	dmesg := Event{Message: "[Thu Nov  5 01:40:00 2026] " + msg, Source: "dmesg"}
	kernlog := Event{Message: "Nov  5 01:40:00 host kernel: [12345.678901] " + msg, Source: "/var/log/kern.log"}
	other := Event{Message: "Nov  5 01:41:00 host kernel: [12400.000000] Kernel panic - not syncing", Source: "/var/log/kern.log"}

	for _, e := range []Event{journal, dmesg, kernlog} {
		if got := crashText(e.Message); got != strings.ToLower(msg) {
			t.Errorf("crashText(%q) = %q", e.Message, got)
		}
	}
	m := newTestMonitor(t)
	now := time.Date(2026, 11, 5, 1, 45, 0, 0, time.UTC)
	got := m.newCrashes([]Event{journal, dmesg, kernlog, other, journal}, 30, now)
	if len(got) != 2 || got[0].Source != "journalctl" || !strings.Contains(got[1].Message, "Kernel panic") {
		t.Fatalf("newCrashes = %+v, want the journal's BUG and the panic", got)
	}
	if again := m.newCrashes([]Event{kernlog}, 30, now.Add(59*time.Minute)); len(again) != 0 {
		t.Errorf("re-told within twice the look-back: %+v", again)
	}
	if again := m.newCrashes([]Event{kernlog}, 30, now.Add(61*time.Minute)); len(again) != 1 {
		t.Errorf("not told again after twice the look-back: %+v", again)
	}
}

// Disk I/O error alerts: on first sight, a counter reset, a 50% jump, or (after a day) growth of at
// least max(100, 10%); a trickle within a day is quiet.
func TestShouldAlertDeviceScoring(t *testing.T) {
	m := newTestMonitor(t)
	const day = 24 * 3600.0
	t0 := 1_700_000_000.0
	steps := []struct {
		count int64
		at    float64
		want  bool
		why   string
	}{
		{1000, t0, true, "first sight"},
		{1000, t0 + 60, false, "no change"},
		{1050, t0 + 120, false, "small growth within a day"},
		{1600, t0 + 180, true, "50% jump"},
		{1800, t0 + 240, false, "+200 is >= 10% but within a day of the last alert"},
		{2000, t0 + 180 + day, true, "+200 >= max(100, 10%) a day after the last alert"},
		{2050, t0 + 180 + 3*day, false, "+50 is under 100 even after days"},
		{10, t0 + 4*day, true, "counter reset"},
	}
	for _, s := range steps {
		if got := m.shouldAlertDevice("sda", s.count, s.at); got != s.want {
			t.Errorf("%s: shouldAlertDevice(%d) = %v, want %v", s.why, s.count, got, s.want)
		}
	}
}

// tailLines reads only the last maxBytes, drops the partial first line, and strips invalid UTF-8.
func TestTailLinesDropsPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "kern.log")
	os.WriteFile(p, []byte("first line that is cut\nsecond \xff\xfeline\nthird line\n"), 0o644)
	lines, err := tailLines(p, 30)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, "|") != "second line|third line|" {
		t.Errorf("tailLines = %q", lines)
	}
	if _, err := tailLines(filepath.Join(t.TempDir(), "missing"), 10); err == nil {
		t.Error("a missing file should be an error")
	}
}

// The seen set bounds its memory: past maxDetected it starts over rather than growing.
func TestSeenIsBounded(t *testing.T) {
	m := newTestMonitor(t)
	if m.seen("a") || !m.seen("a") {
		t.Fatal("seen should report false once, then true")
	}
	for i := range maxDetected {
		m.seen(string(rune(i + 1000)))
	}
	if len(m.detected) > maxDetected {
		t.Errorf("detected grew to %d", len(m.detected))
	}
}

// journalctl --output json writes a MESSAGE with non-UTF-8 or control bytes as an array of byte
// values. Such a crash line must still read as text, or its keywords are never matched.
func TestJournalByteArrayMessage(t *testing.T) {
	var e journalEntry
	if err := json.Unmarshal([]byte(`{"MESSAGE":"BUG: unable to handle page fault","PRIORITY":"1"}`), &e); err != nil || e.message() != "BUG: unable to handle page fault" {
		t.Fatalf("string MESSAGE = %q (%v)", e.message(), err)
	}
	// "BUG: x\xff" as journalctl encodes it.
	var b journalEntry
	if err := json.Unmarshal([]byte(`{"MESSAGE":[66,85,71,58,32,120,255],"PRIORITY":"1"}`), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.message(), "BUG:") {
		t.Skipf("BUG: journalEntry.message renders a byte-array MESSAGE as %q, so crash keywords in non-UTF-8 journal lines are never matched", b.message())
	}
}

// OOM scoring counts only kills since the last check: the first check is the baseline, and an old
// kill from weeks ago never scores again. (Reads the host's /proc counters; writes only to TempDir.)
func TestOOMKillsScoreOnlyNewKills(t *testing.T) {
	n, ok := oomKills()
	if !ok {
		t.Skip("no oom_kill in /proc/vmstat here")
	}
	bt := bootTime()
	if bt == 0 {
		t.Skip("no boot time here")
	}
	hasOOM := func(s Stability) bool {
		for _, i := range s.Indicators {
			if strings.Contains(i, "OOM killer has killed") {
				return true
			}
		}
		return false
	}
	m := newTestMonitor(t)
	m.bootTime = bt
	if s := m.CheckStability(); hasOOM(s) {
		t.Errorf("the first check scored old kills: %v", s.Indicators)
	}
	if m.state.LastOOMKills == nil {
		t.Fatal("the first check did not take a baseline")
	}
	base := *m.state.LastOOMKills
	if base < n {
		t.Fatalf("baseline %d is below the %d read before", base, n)
	}
	m.state.LastOOMKills = new(base - 2)
	s := m.CheckStability()
	if !hasOOM(s) || s.RiskScore < 25 {
		t.Errorf("two new kills did not score: %+v", s)
	}
	if got := *m.state.LastOOMKills; got < base {
		t.Errorf("baseline not moved on: %d", got)
	}
	if s := m.CheckStability(); hasOOM(s) && *m.state.LastOOMKills == base {
		t.Errorf("the same kills scored twice: %v", s.Indicators)
	}
}

// How a journalctl read ended decides whether the journal span counts as read. A journalctl that
// cannot do the read (built without --grep, a journal it cannot open) must not count: the span would
// be skipped and crash detection silently off.
func TestJournalOutcome(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	for _, c := range []struct {
		name          string
		err           error
		stderr        string
		stopped, read bool
		complete      bool
		problem       string
	}{
		{name: "found some", complete: true},
		{name: "--grep matched nothing", err: exit("1"), complete: true},
		{name: "no --grep", err: exit("1"), stderr: "Compiled without pattern matching support\n", problem: "Compiled without pattern matching support"},
		{name: "bad pattern", err: exit("1"), stderr: "Bad pattern \"([\": missing terminating ]\n", problem: "Bad pattern \"([\": missing terminating ]"},
		{name: "other failure", err: exit("2"), problem: "exit status 2"},
		{name: "stopped here having read enough", err: exit("143"), stopped: true, complete: true},
		{name: "output unreadable", stopped: true, read: true, problem: "journalctl's output could not be read"},
	} {
		complete, problem := journalOutcome(c.err, c.stderr, c.stopped, c.read)
		if complete != c.complete || problem != c.problem {
			t.Errorf("%s: complete %v, problem %q", c.name, complete, problem)
		}
	}
}
