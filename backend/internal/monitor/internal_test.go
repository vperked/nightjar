package monitor

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nightjar/internal/cleanup"
	"nightjar/internal/config"
	"nightjar/internal/kernel"
)

// beaten says whether the freeze monitor heard a heartbeat since it was set back to zero.
func beaten(f *FreezeMonitor) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.lastHeartbeat.IsZero()
}

func unbeat(f *FreezeMonitor) {
	f.mu.Lock()
	f.lastHeartbeat = time.Time{}
	f.mu.Unlock()
}

// An alert waiting on a slow channel beats for the loop only when the loop sent it: one from an
// uptime check or the API must not hide a loop that has hung.
func TestOnlyTheLoopsOwnAlertsBeat(t *testing.T) {
	ctx := &Context{Freeze: NewFreezeMonitor(10, 20)}
	unbeat(ctx.Freeze)
	var wg sync.WaitGroup
	wg.Go(ctx.LoopWaiting)
	wg.Wait()
	if beaten(ctx.Freeze) {
		t.Fatal("no loop is running: nothing beats")
	}

	loopIn, loopOut := make(chan func()), make(chan struct{})
	go func() {
		release := ctx.claimThread()
		defer release()
		for fn := range loopIn {
			fn()
			loopOut <- struct{}{}
		}
	}()
	onLoop := func(fn func()) { loopIn <- fn; <-loopOut }
	defer close(loopIn)

	// Another goroutine, the loop's own children included, waits on its own alert.
	onLoop(func() {
		var child sync.WaitGroup
		child.Go(ctx.LoopWaiting)
		child.Wait()
	})
	wg.Go(ctx.LoopWaiting)
	wg.Wait()
	if beaten(ctx.Freeze) {
		t.Fatal("an alert sent from another goroutine beat for the loop")
	}
	onLoop(ctx.LoopWaiting)
	if !beaten(ctx.Freeze) {
		t.Fatal("the loop waiting on its own alert beats")
	}
}

// A cleanup that timed out goes on; the next one is skipped until it ends, not run beside it.
func TestACleanupStillRunningIsNotJoinedByAnother(t *testing.T) {
	h := newHandler(&Context{}, "Disk")
	release := make(chan struct{})
	if _, ran := cleanupStep(h, 20*time.Millisecond, 0, "slow cleanup", func() int { <-release; return 1 }); !ran {
		t.Fatal("the first cleanup runs")
	}
	var calls atomic.Int32
	if v, ran := cleanupStep(h, time.Second, -1, "second cleanup", func() int { calls.Add(1); return 2 }); ran || v != -1 || calls.Load() != 0 {
		t.Fatalf("a second cleanup must wait for the first to end: %v %v", v, ran)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for h.ctx.cleaning.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if v, ran := cleanupStep(h, time.Second, -1, "third cleanup", func() int { return 3 }); !ran || v != 3 {
		t.Fatalf("once the first has ended, the next runs: %v %v", v, ran)
	}
}

// While a crash check that timed out is still running, no kernel check starts beside it.
func TestKernelChecksWaitForACrashCheckStillRunning(t *testing.T) {
	ctx := &Context{Cfg: config.Settings{KernelMonitorEnabled: true, KernelCheckInterval: 1, KernelStabilityCheckInterval: 1}}
	ctx.Kernel = kernel.New(filepath.Join(t.TempDir(), "k.json"), nil)
	h := &kernelChecks{handler: newHandler(ctx, "Kernel"), lastHardwareAlert: map[string]float64{}}
	h.crashCheckRunning.Store(true)
	h.checkCrashes(&Snapshot{Now: 1000})
	h.checkStability(&Snapshot{Now: 1000})
	if h.crashCounter != 1 || h.stabilityCounter != 1 {
		t.Fatalf("both checks wait for the next cycle: crash %d, stability %d", h.crashCounter, h.stabilityCounter)
	}
}

// Stopping waits for uptime checks and repairs only so long, so it ends inside systemd's stop timeout.
func TestStoppingDoesNotWaitForeverOnChecks(t *testing.T) {
	old := stopBudget
	stopBudget = 50 * time.Millisecond
	defer func() { stopBudget = old }()
	l := NewLoop(&Context{})
	l.services.running.Add(1)
	defer l.services.running.Done()
	start := time.Now()
	if l.Wait() {
		t.Fatal("a check still running is reported")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Wait took %s", took)
	}
}

// The routine log step at DISK_CLEANUP_THRESHOLD removes rotated archives and leaves the live
// logs whole; only at DISK_AGGRESSIVE_CLEANUP_THRESHOLD are they emptied.
func TestTheRoutineLogStepLeavesLiveLogsWhole(t *testing.T) {
	dir := t.TempDir()
	logs := cleanup.NewLogMonitor(filepath.Join(dir, "nightjar.log"), 100, 7)
	logs.LogDir, logs.JournalPath, logs.SystemLogs = dir, "", nil
	live := bytes.Repeat([]byte("kernel: a line\n"), 100)
	kern, rotated := filepath.Join(dir, "kern.log"), filepath.Join(dir, "kern.log.1")
	os.WriteFile(kern, live, 0o600)
	os.WriteFile(rotated, live, 0o600)
	ctx := &Context{Cfg: config.Settings{DiskCleanupThreshold: 85, DiskAggressiveCleanupThreshold: 92}, Logs: logs, Cleaner: &cleanup.Cleaner{Logs: logs}}
	h := &diskCleanup{handler: newHandler(ctx, "Disk")}

	h.cleanLogs(&Snapshot{Now: 1000}, 88)
	if _, err := os.Stat(rotated); !os.IsNotExist(err) {
		t.Fatal("the rotated archive goes at 88%")
	}
	if fi, _ := os.Stat(kern); fi == nil || fi.Size() != int64(len(live)) {
		t.Fatal("kern.log is left whole at 88%")
	}
	h.cleanLogs(&Snapshot{Now: 3000}, 93)
	if fi, _ := os.Stat(kern); fi == nil || fi.Size() != 0 {
		t.Fatal("kern.log is emptied at 93%")
	}
}
