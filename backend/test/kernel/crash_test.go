package kernel_test

import (
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

func TestACleanShutdownIsToldFromACrash(t *testing.T) {
	for tail, want := range map[string]bool{
		"Stopping user-1000.slice...\nReached target System Reboot.\nShutting down.\n": true,
		"systemd-logind[812]: System is rebooting.\nStopped target Network.\n":         true,
		"Reached target reboot.target - System Reboot.\n":                              true,
		"systemd-logind[812]: System is powering down.\n":                              true,
		// A boot that ended mid-sentence, as a crash or a power cut leaves it.
		"kernel: nouveau 0000:01:00.0: timer: stalled at ffffffffffffffff\n":                        false,
		"kernel: Out of memory: Killed process 4242 (java)\nsystemd-journald[300]: Journal stopped": false,
	} {
		if got := kernel.CleanShutdown(tail); got != want {
			t.Errorf("%q: %v, want %v", tail, got, want)
		}
	}
}
