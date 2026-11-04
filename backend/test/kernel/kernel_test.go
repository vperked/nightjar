package kernel_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shirou/gopsutil/v4/host"

	"nightjar/internal/kernel"
)

func TestRebootDetectedFromSavedBootTime(t *testing.T) {
	boot, err := host.BootTime()
	if runtime.GOOS != "linux" || err != nil {
		t.Skip("linux only")
	}
	path := filepath.Join(t.TempDir(), "k.json")
	saved, _ := json.Marshal(map[string]any{"boot_time": float64(boot) - 3600})
	os.WriteFile(path, saved, 0o600)

	m := kernel.New(path, nil)
	if r := m.CheckReboot(); r == nil || r.PreviousBootTime != float64(boot)-3600 {
		t.Fatalf("expected a reboot from the saved boot time, got %+v", r)
	}
	if r := m.CheckReboot(); r != nil {
		t.Fatal("a reboot is reported once")
	}
	if r := kernel.New(path, nil).CheckReboot(); r != nil {
		t.Fatal("the new boot time is saved, so a restart is not a reboot")
	}
}
