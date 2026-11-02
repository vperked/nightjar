package units

import (
	"os"
	"strconv"
	"strings"
)

// monotonicNow is seconds since boot, the clock systemd's *Monotonic timestamps count on.
var monotonicNow = func() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
