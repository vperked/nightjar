package repair_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/repair"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

func runner(t *testing.T) (*repair.Runner, string, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	return repair.New(nil, dir, store), dir, store
}

func script(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, mode)
}

func spec(name, scriptName string) services.Spec {
	return services.Spec{Name: name, Type: "tcp", Target: "127.0.0.1:1", Repair: &services.Repair{Action: "script", Script: scriptName, Max: 2, Wait: 60}}
}

func TestScriptRepair(t *testing.T) {
	r, dir, store := runner(t)
	script(t, dir, "fix.sh", `echo "fixing $NIGHTJAR_CHECK after: $NIGHTJAR_DETAIL"`, 0o755)
	script(t, dir, "broken.sh", "echo nope; exit 4", 0o755)
	script(t, dir, "open.sh", "echo hi", 0o777)

	s := spec("db", "fix.sh")
	if !r.Due(s, 100) || r.Due(s, 110) {
		t.Fatal("one attempt at a time")
	}
	rec := r.Run(context.Background(), s, "connection refused", 100, true)
	if !rec.OK || !strings.Contains(rec.Output, "fixing db after: connection refused") {
		t.Fatalf("ran: %+v", rec)
	}
	if r.Due(s, 130) || !r.Due(s, 161) || r.Due(s, 400) {
		t.Fatal("the next attempt waits, and an outage gets at most two")
	}
	r.Run(context.Background(), s, "", 161, true)
	if fixed := r.Recovered("db"); fixed == nil || !fixed.Fixed {
		t.Fatalf("the check came back after it: %+v", fixed)
	}
	if !r.Due(s, 500) {
		t.Fatal("a new outage starts over")
	}
	if list := store.Repairs("db", 10); len(list) != 2 || !list[0].Fixed {
		t.Fatalf("attempts kept, the last marked as the fix: %+v", list)
	}

	if rec := r.Run(context.Background(), spec("x", "broken.sh"), "", 1, false); rec.OK || !strings.Contains(rec.Detail, "code 4") || !strings.Contains(rec.Output, "nope") {
		t.Fatalf("a failing script: %+v", rec)
	}
	if rec := r.Run(context.Background(), spec("x", "open.sh"), "", 1, false); rec.OK || !strings.Contains(rec.Detail, "chmod go-w") {
		t.Fatalf("a script others can change does not run: %+v", rec)
	}
	if rec := r.Run(context.Background(), spec("x", "missing.sh"), "", 1, false); rec.OK {
		t.Fatalf("a missing script: %+v", rec)
	}
	list := r.Scripts()
	if len(list) != 3 || list[2].Name != "open.sh" || list[2].Problem == "" || list[1].Problem != "" {
		t.Fatalf("scripts: %+v", list)
	}
}

func TestRestartWithoutSystemd(t *testing.T) {
	r, _, _ := runner(t)
	s := services.Spec{Name: "plex", Repair: &services.Repair{Action: "restart", Unit: "plexmediaserver.service", Max: 1, Wait: 30}}
	if rec := r.Run(context.Background(), s, "", 1, false); rec.OK || rec.Target != "plexmediaserver.service" {
		t.Fatalf("no systemd here: %+v", rec)
	}
}
