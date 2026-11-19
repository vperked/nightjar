package agent_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/updates"
)

// lockedBuffer is live output, read while the runner writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeSystemdRun stands in for systemd-run: it notes its arguments in calls, then runs the command
// as a unit would, with the environment, folder and output files it was given. No unit is made.
func fakeSystemdRun(t *testing.T, dir, version string) (path, calls string) {
	calls = filepath.Join(dir, "calls")
	return script(t, dir, "systemd-run", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "systemd `+version+` (`+version+`.4-1)"; echo "+PAM +AUDIT"; exit 0; fi
printf '%s\n' "$@" > `+calls+`
out=/dev/null; err=/dev/null
while [ $# -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    --property=StandardOutput=append:*) out="${1#--property=StandardOutput=append:}" ;;
    --property=StandardError=append:*) err="${1#--property=StandardError=append:}" ;;
    --setenv=*) export "${1#--setenv=}" ;;
    --working-directory=*) cd "${1#--working-directory=}" ;;
  esac
  shift
done
exec "$@" >>"$out" 2>>"$err"
`), calls
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestUpdateCommandsRunAsUnitsOutsideTheSandbox(t *testing.T) {
	dir := t.TempDir()
	run, calls := fakeSystemdRun(t, dir, "255")
	stops := filepath.Join(dir, "stops")
	ctl := script(t, dir, "systemctl", "#!/bin/sh\necho \"$@\" >> "+stops+"\n")
	logs := filepath.Join(dir, "logs")
	var mu sync.Mutex
	var direct []string
	r := &agent.UnitRunner{Dir: logs, SystemdRun: run, Systemctl: ctl, Direct: func(_ context.Context, c updates.Command) (updates.Output, error) {
		mu.Lock()
		defer mu.Unlock()
		direct = append(direct, strings.Join(c.Argv, " "))
		return updates.Output{Stdout: "active\n"}, nil
	}}
	ctx := context.Background()

	// A command runs as its own unit: its output reaches the update as it prints, stdout and stderr
	// apart, with its exit code.
	work := t.TempDir()
	live := &lockedBuffer{}
	out, err := r.Run(ctx, updates.Command{Argv: []string{"sh", "-c", `echo "first $GREETING"; pwd; echo oops >&2; exit 3`},
		Env: []string{"GREETING=hello"}, Dir: work, User: "root", Output: live})
	if err != nil || out.Code != 3 || out.Stdout != "first hello\n"+work+"\n" || out.Stderr != "oops\n" {
		t.Fatalf("ran: %+v %v", out, err)
	}
	if !strings.Contains(live.String(), "first hello") || !strings.Contains(live.String(), "oops") {
		t.Fatalf("live output: %q", live.String())
	}
	args := readLines(t, calls)
	unit := args[0]
	if !strings.HasPrefix(unit, "--unit=nightjar-update-sh-") || !strings.HasSuffix(unit, ".service") {
		t.Fatalf("unit name: %s", unit)
	}
	for _, want := range []string{"--quiet", "--collect", "--wait", "--setenv=GREETING=hello", "--working-directory=" + work, "--uid=root", "--setenv=HOME=/root",
		"--setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"} {
		if !strings.Contains(strings.Join(args, "\n")+"\n", "\n"+want+"\n") {
			t.Errorf("systemd-run without %s: %q", want, args)
		}
	}
	var appends int
	for _, a := range args {
		for _, p := range []string{"--property=StandardOutput=append:", "--property=StandardError=append:"} {
			if f, ok := strings.CutPrefix(a, p); ok && filepath.Dir(f) == logs {
				appends++
			}
		}
	}
	if appends != 2 {
		t.Errorf("output goes to two files in the agent's folder: %q", args)
	}
	if i := strings.Index(strings.Join(args, " "), " -- sh -c "); i < 0 {
		t.Errorf("the command comes last, after --: %q", args)
	}
	if left, _ := os.ReadDir(logs); len(left) != 0 {
		t.Errorf("output files are removed: %v", left)
	}

	// Output shows while the command runs, not only at its end.
	live = &lockedBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, updates.Command{Argv: []string{"sh", "-c", "echo begun; sleep 2; echo ended"}, Output: live})
	}()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for !strings.Contains(live.String(), "begun") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("the command ended before its output showed")
	default:
	}
	if !strings.Contains(live.String(), "begun") || strings.Contains(live.String(), "ended") {
		t.Fatalf("live output while running: %q", live.String())
	}
	<-done

	// What only looks runs in the agent itself: no unit every few seconds while a service starts.
	os.Remove(calls)
	if out, err := r.Run(ctx, updates.Command{Argv: []string{"systemctl", "is-active", "plexmediaserver"}}); err != nil || out.Stdout != "active\n" {
		t.Fatalf("is-active: %+v %v", out, err)
	}
	if _, err := os.Stat(calls); err == nil || len(direct) != 1 || direct[0] != "systemctl is-active plexmediaserver" {
		t.Fatalf("read-only commands run directly: %v", direct)
	}

	// Out of time, the unit itself is stopped, not only the systemd-run waiting on it.
	start := time.Now()
	_, err = r.Run(ctx, updates.Command{Argv: []string{"sleep", "30"}, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Fatalf("timed out: %v after %s", err, time.Since(start))
	}
	unit = strings.TrimPrefix(readLines(t, calls)[0], "--unit=")
	if got := readLines(t, stops); len(got) != 1 || got[0] != "stop "+unit {
		t.Fatalf("stopped %q, want stop %s", got, unit)
	}
}

// Without a systemd-run that can write to a file, commands run in the agent itself, and say so.
func TestUpdateCommandsFallBackToRunningDirectly(t *testing.T) {
	dir := t.TempDir()
	oldRun, _ := fakeSystemdRun(t, t.TempDir(), "237")
	for name, systemdRun := range map[string]string{"missing": filepath.Join(dir, "no-systemd-run"), "older than 240": oldRun} {
		var ran []string
		r := &agent.UnitRunner{Dir: dir, SystemdRun: systemdRun, Direct: func(_ context.Context, c updates.Command) (updates.Output, error) {
			ran = append(ran, strings.Join(c.Argv, " "))
			return updates.Output{Stderr: "E: Could not open lock file /var/lib/dpkg/lock-frontend", Code: 100}, nil
		}}
		live := &lockedBuffer{}
		out, err := r.Run(context.Background(), updates.Command{Argv: []string{"apt-get", "upgrade", "-y"}, Output: live})
		if err != nil || out.Code != 100 || len(ran) != 1 || ran[0] != "apt-get upgrade -y" {
			t.Fatalf("%s: %+v %v %v", name, out, err, ran)
		}
		if !strings.Contains(out.Stderr, "lock file") || !strings.Contains(out.Stderr, "inside the agent's sandbox") || !strings.Contains(out.Stderr, name) {
			t.Errorf("%s: a failure says where it ran: %q", name, out.Stderr)
		}
		if !strings.Contains(live.String(), "inside the agent's sandbox") {
			t.Errorf("%s: the live output says so too: %q", name, live.String())
		}
	}
}
