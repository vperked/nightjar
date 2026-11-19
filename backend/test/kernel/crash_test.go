package kernel_test

import (
	"encoding/json"
	"testing"

	"nightjar/internal/kernel"
)

func TestOnlyTheKernelsOwnCrashesCount(t *testing.T) {
	for line, want := range map[string]string{
		// An app's startup in syslog: "OLLAMA_DEBUG:INFO" has "bug:" in it, but it is no kernel crash.
		`2026-11-05T01:31:53.417217+00:00 home-server ollama.listener[6167]: time=2026-11-05T01:31:53.414Z level=INFO msg="server config" env="map[OLLAMA_DEBUG:INFO OLLAMA_DEBUG_LOG_REQUESTS:false]"`: "",
		// An app saying the words is not the kernel saying them.
		`2026-11-05T01:40:00+00:00 home-server myapp[42]: kernel panic: just testing`:                                  "",
		`2026-11-05T01:40:00+00:00 home-server kernel: [ 12.3] kernel BUG at mm/slub.c:4242!`:                          "kernel bug at",
		`2026-11-05T01:40:00+00:00 home-server kernel: BUG: unable to handle page fault for address: 0000000000001000`: "bug:",
		`2026-11-05T01:40:00+00:00 home-server kernel: Kernel panic - not syncing: Fatal exception`:                    "kernel panic",
		`2026-11-05T01:40:00+00:00 home-server kernel: general protection fault, probably for non-canonical address`:   "general protection fault",
		// The kernel mentioning debugging is not a crash either.
		`2026-11-05T01:40:00+00:00 home-server kernel: nvidia: module_debug: on`: "",
	} {
		kw, ok := kernel.KernelLogCrash(line)
		if (want != "") != ok || kw != want {
			t.Errorf("%q: %q %v, want %q", line, kw, ok, want)
		}
	}
	for _, c := range []struct {
		line, kw string
		want     bool
	}{
		{"ollama_debug:info", "bug:", false},
		{"kernel bug: oops", "bug:", true},
		{"bug: at start", "bug:", true},
		{"nopanic=1", "panic", false},
		{"(panic) on cpu", "panic", true},
	} {
		if got := kernel.HasKeyword(c.line, c.kw); got != c.want {
			t.Errorf("HasKeyword(%q, %q) = %v", c.line, c.kw, got)
		}
	}
}

// entry is one journal entry as journalctl -o json writes it.
func entry(pid, uid, comm, userUnit, msg string) string {
	e := map[string]string{"_PID": pid, "_UID": uid, "_COMM": comm, "MESSAGE": msg, "SYSLOG_IDENTIFIER": comm}
	if userUnit != "" {
		e["_SYSTEMD_USER_UNIT"] = userUnit
	}
	b, _ := json.Marshal(e)
	return string(b) + "\n"
}

func TestACleanShutdownIsToldFromACrash(t *testing.T) {
	for name, c := range map[string]struct {
		journal string
		want    bool
	}{
		"the manager reaches its reboot target": {entry("1", "0", "systemd", "", "Stopping user-1000.slice...") + entry("1", "0", "systemd", "", "Reached target System Reboot.") + entry("1", "0", "systemd", "", "Shutting down."), true},
		"newer systemd names the unit":          {entry("1", "0", "systemd", "", "Reached target reboot.target - System Reboot."), true},
		"the manager reaches shutdown":          {entry("1", "0", "systemd", "", "Reached target shutdown.target - System Shutdown."), true},
		"logind is told to reboot":              {entry("812", "0", "systemd-logind", "", "System is rebooting."), true},
		"logind is told to power off":           {entry("812", "0", "systemd-logind", "", "System is powering down."), true},
		// A user's own systemd reaches its shutdown target whenever their last session ends.
		"a user logs out": {entry("1", "0", "systemd", "", "Stopped user@1000.service - User Manager for UID 1000.") +
			entry("4242", "1000", "systemd", "init.scope", "Reached target shutdown.target - Shutdown."), false},
		// root's own user manager runs as root, outside PID 1.
		"root logs out": {entry("4243", "0", "systemd", "init.scope", "Reached target shutdown.target - Shutdown."), false},
		// Daemons say it when they restart.
		"a daemon restarts": {entry("900", "0", "nginx", "", "shutting down."), false},
		// Anyone can tag a line "systemd" or name a process "systemd-logind".
		"a forged tag": {entry("5000", "1000", "systemd-logind", "", "System is rebooting.") + entry("5001", "1000", "logger", "", "Reached target System Reboot."), false},
		// A boot that ended mid-sentence, as a crash or a power cut leaves it.
		"a crash": {entry("1", "0", "systemd", "", "Started session-4.scope - Session 4 of User admin.") + "-- No entries --\n" + "{not json\n", false},
		"nothing": {"", false},
	} {
		if got := kernel.CleanShutdown([]byte(c.journal)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	// A message journalctl writes as bytes (it held a control character) still counts.
	raw := `{"_PID":"1","_UID":"0","_COMM":"systemd","MESSAGE":[83,104,117,116,116,105,110,103,32,100,111,119,110,46,7]}`
	if !kernel.CleanShutdown([]byte(raw)) {
		t.Error("a byte-array message is read")
	}
}
