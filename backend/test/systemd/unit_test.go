package systemd_test

import (
	"strings"
	"testing"

	"nightjar/internal/systemd"
)

// Root is left unnamed: systemd 255 starts a service with User=root, NoNewPrivileges and a sandbox
// option without CAP_SETUID, which broke apt and git. The hardening stays.
func TestTheUnitRunsAsRootWithoutNamingIt(t *testing.T) {
	unit := systemd.UnitContent("/opt/nightjar/nightjar", "/opt/nightjar", "root", "")
	if strings.Contains(unit, "User=") || strings.Contains(unit, "Group=") {
		t.Fatalf("root is named:\n%s", unit)
	}
	for _, want := range []string{"NoNewPrivileges=true", "ProtectControlGroups=true", "RestrictSUIDSGID=true", "ExecStart=/opt/nightjar/nightjar", "WorkingDirectory=/opt/nightjar"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit misses %q", want)
		}
	}
	if other := systemd.UnitContent("/opt/nightjar/nightjar", "/opt/nightjar", "nightjar", ""); !strings.Contains(other, "User=nightjar\nGroup=nightjar\n") {
		t.Fatalf("another user is named:\n%s", other)
	}
}

// Another user reaches root through sudo, which NoNewPrivileges and RestrictSUIDSGID turn off, so
// its unit leaves them out and keeps the rest of the hardening.
func TestAUnitForAnotherUserCanStillUseSudo(t *testing.T) {
	unit := systemd.UnitContent("/opt/nightjar/nightjar", "/opt/nightjar", "nightjar", "")
	for _, gone := range []string{"NoNewPrivileges", "RestrictSUIDSGID"} {
		if strings.Contains(unit, gone) {
			t.Errorf("the unit keeps %s, which stops sudo:\n%s", gone, unit)
		}
	}
	if !strings.Contains(unit, "ProtectControlGroups=true") || !strings.Contains(unit, "[Install]") {
		t.Fatalf("the rest of the unit is missing:\n%s", unit)
	}
}

// ProtectKernelModules hides /lib/modules from apt run by the service, so an upgrade that rebuilds
// the initramfs writes one without modules and the next boot cannot find the root disk.
func TestTheUnitLeavesKernelModulesVisibleToApt(t *testing.T) {
	for _, user := range []string{"root", "nightjar"} {
		if unit := systemd.UnitContent("/opt/nightjar/nightjar", "/opt/nightjar", user, ""); strings.Contains(unit, "ProtectKernelModules") {
			t.Errorf("the unit for %s hides kernel modules:\n%s", user, unit)
		}
	}
}

// Stopping takes up to about 70s (a check under way gets a minute) before the last minute's readings
// are saved and the database closed; systemd must not kill it before then.
func TestTheUnitGivesStoppingTimeToSave(t *testing.T) {
	unit := systemd.UnitContent("/opt/nightjar/nightjar", "/opt/nightjar", "root", "")
	if !strings.Contains(unit, "TimeoutStopSec=90\n") {
		t.Fatalf("the unit's stop timeout is too short:\n%s", unit)
	}
}
