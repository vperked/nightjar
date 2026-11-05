package kernel_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nightjar/internal/kernel"
)

func TestNoEdacDriverMeansNoCounts(t *testing.T) {
	// Kernels built without EDAC have no /sys/devices/system/edac at all.
	if got := kernel.EdacCounts(filepath.Join(t.TempDir(), "missing")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEdacCountsAreKeptPerController(t *testing.T) {
	root := t.TempDir()
	write := func(rel, v string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v+"\n"), 0o644)
	}
	write("mc0/ce_count", "120")
	write("mc0/ue_count", "0")
	write("mc0/ce_noinfo_count", "3")
	// The per-row counts are the controller's total split up, not more errors.
	write("mc0/csrow0/ce_count", "100")
	write("mc0/csrow1/ce_count", "20")
	write("mc1/ce_count", "7")
	write("mc1/ue_count", "1")
	want := map[string]int64{"mc0/ce_count": 120, "mc0/ue_count": 0, "mc1/ce_count": 7, "mc1/ue_count": 1}
	if got := kernel.EdacCounts(root); !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSyslogTime(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	for line, want := range map[string]time.Time{
		"2026-01-02T01:40:00.417217+00:00 host kernel: BUG: x": time.Date(2026, 1, 2, 1, 40, 0, 417217000, time.UTC),
		"Jan  2 02:59:00 host kernel: BUG: x":                  time.Date(2026, 1, 2, 2, 59, 0, 0, time.UTC),
		// A traditional line has no year: December's is last year's.
		"Dec 31 23:00:00 host kernel: BUG: x": time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC),
	} {
		got, ok := kernel.SyslogTime(line, now)
		if !ok || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", line, got, ok, want)
		}
	}
	if _, ok := kernel.SyslogTime("kernel: no time here", now); ok {
		t.Error("a line without a timestamp has none")
	}
}
