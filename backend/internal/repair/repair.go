// Package repair brings a down check back when the check says how: by restarting a systemd
// service, or by running a script from the repairs folder. Each outage gets a few attempts, spaced
// out, and every attempt is kept, with whether the check came back after it.
package repair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nightjar/internal/config"
	"nightjar/internal/docker"
	"nightjar/internal/logx"
	"nightjar/internal/services"
	"nightjar/internal/storage"
	"nightjar/internal/units"
)

var log = logx.Component("Repair")

// scriptTimeout is how long a repair script may run.
const scriptTimeout = 2 * time.Minute

// maxOutput is how much of a script's output is kept.
const maxOutput = 4000

// Runner repairs checks. Its methods are safe for concurrent use.
type Runner struct {
	Units  *units.Monitor // nil without systemd
	Docker *docker.Client // nil without Docker
	Dir    string         // the repairs folder
	Store  *storage.Store

	mu      sync.Mutex
	outages map[string]*outage
}

// outage is one check's time down: the attempts made, and the last that worked.
type outage struct {
	attempts int
	last     float64
	running  bool
	worked   *storage.Repair
}

func New(u *units.Monitor, dir string, store *storage.Store) *Runner {
	return &Runner{Units: u, Dir: dir, Store: store, outages: map[string]*outage{}}
}

// Due claims an attempt for a down check when its repair allows one now: none running, fewer
// than its most, and long enough since the last. The caller then calls Run with outage true.
func (r *Runner) Due(spec services.Spec, now float64) bool {
	if r == nil || spec.Repair == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.outages[spec.Name]
	if o == nil {
		o = &outage{}
		r.outages[spec.Name] = o
	}
	if o.running || o.attempts >= spec.Repair.Max || (o.attempts > 0 && now-o.last < spec.Repair.Wait) {
		return false
	}
	o.attempts++
	o.last, o.running = now, true
	return true
}

// Attempts is how many times the check's current outage has been repaired.
func (r *Runner) Attempts(name string) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if o := r.outages[name]; o != nil {
		return o.attempts
	}
	return 0
}

// Run repairs the check once and keeps the attempt. With outage, it is one of the outage's
// attempts (claimed with Due); without, it was asked for from the dashboard.
func (r *Runner) Run(ctx context.Context, spec services.Spec, detail string, now float64, outage bool) storage.Repair {
	rec := storage.Repair{TS: now, Service: spec.Name}
	if spec.Repair == nil {
		rec.Detail = "the check has no repair"
		return rec
	}
	rec.Action = spec.Repair.Action
	switch spec.Repair.Action {
	case "restart":
		rec.Target = spec.Repair.Unit
		if r.Units == nil {
			rec.Detail = "systemd is not available here"
			break
		}
		ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
		err := r.Units.Control(ctx, "restart", spec.Repair.Unit)
		cancel()
		if errors.Is(err, units.ErrUnknown) {
			err = fmt.Errorf("systemd has no service called %s", spec.Repair.Unit)
		}
		rec.OK = err == nil
		rec.Detail = "restarted " + spec.Repair.Unit
		if err != nil {
			rec.Detail = err.Error()
		}
	case "container":
		rec.Target = spec.Repair.Container
		ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
		err := r.Docker.Restart(ctx, spec.Repair.Container)
		cancel()
		rec.OK = err == nil
		rec.Detail = "restarted the " + spec.Repair.Container + " container"
		if err != nil {
			rec.Detail = err.Error()
		}
	case "script":
		rec.Target = spec.Repair.Script
		rec.OK, rec.Detail, rec.Output = r.runScript(ctx, spec, detail)
	}
	rec.ID = r.Store.AddRepair(rec)
	if outage {
		r.mu.Lock()
		if o := r.outages[spec.Name]; o != nil {
			o.running = false
			if rec.OK {
				kept := rec
				o.worked = &kept
			}
		}
		r.mu.Unlock()
	}
	return rec
}

// Recovered ends the check's outage. It returns the last attempt that worked, marked as the fix,
// or nil when the check came back without one.
func (r *Runner) Recovered(name string) *storage.Repair {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	o := r.outages[name]
	delete(r.outages, name)
	r.mu.Unlock()
	if o == nil || o.worked == nil {
		return nil
	}
	r.Store.MarkRepairFixed(o.worked.ID)
	fixed := *o.worked
	fixed.Fixed = true
	return &fixed
}

// Forget drops a check's outage, when it is paused, changed or deleted.
func (r *Runner) Forget(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.outages, name)
	r.mu.Unlock()
}

func (r *Runner) runScript(ctx context.Context, spec services.Spec, detail string) (bool, string, string) {
	path, err := r.script(spec.Repair.Script)
	if err != nil {
		return false, err.Error(), ""
	}
	// A program the script starts in the background (the usual way to bring back one systemd does
	// not run) keeps the script's output. That output goes to a temp file, unlinked at once, not to
	// a pipe Nightjar reads: the repair finishes when the script does, and the program never gets
	// SIGPIPE (and dies) when Nightjar restarts and the reading end goes away.
	out, err := os.CreateTemp("", "nightjar-repair-*")
	if err != nil {
		return false, err.Error(), ""
	}
	os.Remove(out.Name())
	defer out.Close()
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	// Outside Nightjar's own service, so the program lives on when Nightjar restarts: systemd
	// stops a service with everything in it.
	if scope := ScopeCommand(); len(scope) > 0 {
		cmd = exec.CommandContext(ctx, scope[0], append(scope[1:], path)...)
	}
	cmd.Dir = filepath.Dir(path)
	// Without Nightjar's secrets (webhooks, tokens, passwords): a script has no need of them.
	cmd.Env = append(config.Environ(),
		"NIGHTJAR_CHECK="+spec.Name, "NIGHTJAR_TYPE="+spec.Type, "NIGHTJAR_TARGET="+spec.Target, "NIGHTJAR_DETAIL="+detail,
		"NIGHTJAR_ATTEMPT="+strconv.Itoa(r.Attempts(spec.Name)))
	cmd.Stdout, cmd.Stderr = out, out
	// Its own session (and so process group): a script that runs out of time is stopped with
	// everything it started, and a program it leaves running is not tied to Nightjar's terminal.
	// systemd-run --scope runs the script in its own place, so the group is the script's.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	// What the script wrote; a program it started may go on writing, which is not kept.
	text := tail(out)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return false, fmt.Sprintf("%s ran longer than %s and was stopped", spec.Repair.Script, scriptTimeout), text
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return false, fmt.Sprintf("%s exited with code %d", spec.Repair.Script, exit.ExitCode()), text
		}
		return false, err.Error(), text
	}
	return true, "ran " + spec.Repair.Script, text
}

// ScopeCommand is how a script is started outside Nightjar's service: a transient systemd scope,
// collected when the script and whatever it started are gone. Without systemd, or without root to
// ask it, the script runs directly (in its own session). Tests replace it.
var ScopeCommand = func() []string {
	if os.Getenv("NIGHTJAR_REPAIR_NO_SYSTEMD") != "" {
		return nil
	}
	return scopeCommand()
}

// scopeCommand tries a scope once: one that cannot be made (no systemd, not root, a container
// without its bus) would fail every repair before the script even ran.
var scopeCommand = sync.OnceValue(func() []string {
	if os.Geteuid() != 0 {
		return nil
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return nil
	}
	bin, err := exec.LookPath("systemd-run")
	if err != nil {
		return nil
	}
	argv := []string{bin, "--scope", "--collect", "--quiet", "--description=Nightjar repair script"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, argv[0], append(argv[1:], "true")...).CombinedOutput(); err != nil {
		log.Warn("Repair scripts run inside Nightjar's service: systemd would not start a scope for them, so programs they start stop when Nightjar does", "err", strings.TrimSpace(string(out)))
		return nil
	}
	return argv
})

// tail is the last maxOutput bytes of what was written to f so far.
func tail(f *os.File) string {
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	from := max(0, size-maxOutput)
	buf := make([]byte, size-from)
	n, _ := f.ReadAt(buf, from)
	return string(buf[:n])
}

// script is the path of a repair script, when it may run. Like plugins, and like sshd with its
// keys, a script others could change is not run, since it runs as Nightjar.
func (r *Runner) script(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%q is not a script's file name", name)
	}
	dir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return "", fmt.Errorf("the repairs folder %s does not exist", r.Dir)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("there is no %s in the repairs folder", name)
	}
	if filepath.Dir(path) != dir {
		return "", fmt.Errorf("%s points outside the repairs folder", name)
	}
	for _, p := range []string{dir, path} {
		info, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("%s can be changed by other users; run chmod go-w on it", filepath.Base(p))
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
			return "", fmt.Errorf("%s belongs to another user; it must belong to root or to the user Nightjar runs as", filepath.Base(p))
		}
	}
	info, _ := os.Stat(path)
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", name)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable; run chmod +x on it", name)
	}
	return path, nil
}

// Script is a file in the repairs folder, and why it cannot run, if it cannot.
type Script struct {
	Name    string `json:"name"`
	Problem string `json:"problem,omitempty"`
}

// Scripts lists the repairs folder.
func (r *Runner) Scripts() []Script {
	out := []Script{}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		s := Script{Name: e.Name()}
		if _, err := r.script(e.Name()); err != nil {
			s.Problem = err.Error()
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Script) int { return strings.Compare(a.Name, b.Name) })
	return out
}
