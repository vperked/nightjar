package selfcheck

import (
	"context"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"time"
)

// capNames are the Linux capabilities by bit, as /proc/self/status counts them.
var capNames = []string{
	"cap_chown", "cap_dac_override", "cap_dac_read_search", "cap_fowner", "cap_fsetid", "cap_kill", "cap_setgid", "cap_setuid",
	"cap_setpcap", "cap_linux_immutable", "cap_net_bind_service", "cap_net_broadcast", "cap_net_admin", "cap_net_raw", "cap_ipc_lock",
	"cap_ipc_owner", "cap_sys_module", "cap_sys_rawio", "cap_sys_chroot", "cap_sys_ptrace", "cap_sys_pacct", "cap_sys_admin",
	"cap_sys_boot", "cap_sys_nice", "cap_sys_resource", "cap_sys_time", "cap_sys_tty_config", "cap_mknod", "cap_lease",
	"cap_audit_write", "cap_audit_control", "cap_setfcap", "cap_mac_override", "cap_mac_admin", "cap_syslog", "cap_wake_alarm",
	"cap_block_suspend", "cap_audit_read", "cap_perfmon", "cap_bpf", "cap_checkpoint_restore",
}

// CapSetuid is the capability to switch to another user, which apt, git as a checkout's owner and
// app installs need.
const CapSetuid = 7

// Caps reads this process's effective capabilities from a /proc status file.
func Caps(status string) (uint64, bool) {
	for line := range strings.Lines(status) {
		if rest, ok := strings.CutPrefix(line, "CapEff:"); ok {
			v, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// OwnCaps are this process's effective capabilities now.
func OwnCaps() (uint64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	return Caps(string(b))
}

// HasCap says whether this process holds a capability now; true when it cannot tell.
func HasCap(bit int) bool {
	c, ok := OwnCaps()
	return !ok || c&(1<<bit) != 0
}

// CapNames names the capabilities in a set.
func CapNames(set uint64) []string {
	var out []string
	for set != 0 {
		b := bits.TrailingZeros64(set)
		if b < len(capNames) {
			out = append(out, capNames[b])
		} else {
			out = append(out, "cap_"+strconv.Itoa(b))
		}
		set &^= 1 << b
	}
	return out
}

// WatchCaps calls lost, once for each, when a capability this process had at the start is gone:
// something took it, and what needs it fails from then on.
func WatchCaps(ctx context.Context, every time.Duration, lost func(names []string)) {
	start, ok := OwnCaps()
	if !ok {
		return
	}
	reported := uint64(0)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now, ok := OwnCaps()
		if !ok {
			continue
		}
		if gone := start &^ now &^ reported; gone != 0 {
			reported |= gone
			lost(CapNames(gone))
		}
	}
}
