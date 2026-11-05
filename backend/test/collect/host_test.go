package collect_test

import (
	"runtime"
	"strings"
	"testing"

	"nightjar/internal/collect"
)

func TestAFilesystemIsListedOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	seen := map[string]string{}
	for _, p := range collect.Partitions(false) {
		if !strings.HasPrefix(p.Device, "/dev/") {
			continue
		}
		if first, ok := seen[p.Device]; ok {
			t.Errorf("%s is listed at %s and again at %s", p.Device, first, p.Mountpoint)
		}
		seen[p.Device] = p.Mountpoint
	}
}

func TestHostCountersRead(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	if _, err := collect.DiskIO(); err != nil {
		t.Fatal(err)
	}
	if _, err := collect.NetworkIO(); err != nil {
		t.Fatal(err)
	}
}
