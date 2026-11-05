package repair_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

// A script that starts a program in the background, as one bringing back a program systemd does
// not run does, finishes when the script does, not when the program does.
func TestScriptThatStartsAProgramFinishes(t *testing.T) {
	r, dir, _ := runner(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	script(t, dir, "start.sh", "sleep 30 &\necho $! > "+pidFile+"\necho started", 0o755)
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	done := make(chan storage.Repair, 1)
	go func() { done <- r.Run(context.Background(), spec("app", "start.sh"), "", 1, false) }()
	select {
	case rec := <-done:
		if !rec.OK || !strings.Contains(rec.Output, "started") {
			t.Fatalf("ran: %+v", rec)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the repair waited for the program the script started")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("the program the script started was stopped with it")
	}
}

// A program the script starts can go on writing after the repair is done, without being stopped
// for it, and what it writes then is not kept.
func TestProgramKeepsWritingAfterTheRepair(t *testing.T) {
	r, dir, _ := runner(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	script(t, dir, "start.sh", "(sleep 2; while :; do echo later; sleep 0.05; done) &\necho $! > "+pidFile+"\necho started", 0o755)
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	rec := r.Run(context.Background(), spec("app", "start.sh"), "", 1, false)
	if !rec.OK || !strings.Contains(rec.Output, "started") || strings.Contains(rec.Output, "later") {
		t.Fatalf("ran: %+v", rec)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	// Its output goes to a pipe Nightjar reads, not a file that would grow for as long as it runs.
	if fd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/fd/1"); err == nil && !strings.HasPrefix(fd, "pipe:") {
		t.Fatalf("the program writes to %s", fd)
	}
	time.Sleep(3 * time.Second)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("the program the script started was stopped when it wrote after the repair")
	}
}

// Only the end of a long output is kept.
func TestLongOutputKeepsTheEnd(t *testing.T) {
	r, dir, _ := runner(t)
	script(t, dir, "loud.sh", "seq 1 20000", 0o755)
	rec := r.Run(context.Background(), spec("x", "loud.sh"), "", 1, false)
	if !rec.OK || len(rec.Output) > 4000 || !strings.HasSuffix(rec.Output, "\n20000\n") {
		t.Fatalf("ran: ok=%v len=%d", rec.OK, len(rec.Output))
	}
}

// A repair script gets the check's details but none of Nightjar's secrets.
func TestScriptsDoNotGetNightjarsSecrets(t *testing.T) {
	t.Setenv("WEB_AUTH_TOKEN", "secret-token")
	t.Setenv("PLEX_URL", "http://127.0.0.1:32400")
	r, dir, _ := runner(t)
	script(t, dir, "env.sh", `echo "token=$WEB_AUTH_TOKEN plex=$PLEX_URL check=$NIGHTJAR_CHECK"`, 0o755)
	rec := r.Run(context.Background(), spec("db", "env.sh"), "", 1, false)
	if !rec.OK || !strings.Contains(rec.Output, "token= plex=http://127.0.0.1:32400 check=db") {
		t.Fatalf("ran: %+v", rec)
	}
}
