package kernel_test

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"nightjar/internal/kernel"
)

// A crash check the loop stopped waiting for goes on in the background while the next cycle's
// checks run: they must not touch the monitor's state at the same time (run with -race).
func TestChecksMayOverlap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	m := kernel.New(filepath.Join(t.TempDir(), "k.json"), nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 3 {
				m.CheckStability()
				m.CheckReboot()
			}
		})
	}
	wg.Go(func() { m.CheckCrashes(1) })
	wg.Wait()
}
