package systemd

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// SystemState is what systemd says of the whole machine: "initializing" or "starting" while it
// boots, "running" or "degraded" once it has, "stopping" while it shuts down; "" when it cannot be
// asked (no systemd, or it did not answer in time).
func SystemState() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// It exits non-zero for every state but "running", and still says which.
	out, _ := exec.CommandContext(ctx, "systemctl", "is-system-running").Output()
	return strings.TrimSpace(string(out))
}

// Settling reports whether the machine is booting or shutting down, by SystemState.
func Settling(state string) bool {
	switch state {
	case "initializing", "starting", "stopping":
		return true
	}
	return false
}
