package cleanup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"nightjar/internal/cleanup"
)

// At DISK_CLEANUP_THRESHOLD only rotated archives go: the live logs, kern.log among them, stay
// whole. Emptying them is for DISK_AGGRESSIVE_CLEANUP_THRESHOLD.
func TestRoutineLogCleanupRemovesOnlyRotatedLogs(t *testing.T) {
	m, dir := newMonitor(t, 1<<20)
	m.SystemLogs = nil
	live := bytes.Repeat([]byte("kernel: line\n"), 1000)
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	kern := write("kern.log", live)
	syslog := write("syslog", live)
	rotated := write("kern.log.1", live)
	archive := write("syslog.2.gz", []byte("old"))
	auth := write("auth.log.1", live) // a security archive is never removed
	c := &cleanup.Cleaner{Logs: m}

	st := c.CleanLogs(false)
	if st.LogsCleaned != 2 || len(st.Errors) != 0 {
		t.Fatalf("two rotated archives removed: %+v", st)
	}
	for _, p := range []string{rotated, archive} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should be gone", p)
		}
	}
	for _, p := range []string{kern, syslog, auth} {
		if fi, err := os.Stat(p); err != nil || fi.Size() != int64(len(live)) {
			t.Errorf("%s should be left whole", p)
		}
	}

	st = c.CleanLogs(true)
	for _, p := range []string{kern, syslog} {
		if fi, err := os.Stat(p); err != nil || fi.Size() != 0 {
			t.Errorf("%s should be emptied at the aggressive threshold", p)
		}
	}
	if fi, err := os.Stat(auth); err != nil || fi.Size() != int64(len(live)) {
		t.Error("the security archive is still kept whole")
	}
	if st.LogsCleaned != 2 {
		t.Fatalf("two live logs emptied, the protected archive not counted: %+v", st)
	}
}
