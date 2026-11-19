package updates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"syscall"
	"time"
)

// Command is one program to run for a check or an update.
type Command struct {
	Argv []string
	Dir  string
	Env  []string // extra KEY=value pairs
	// User runs it as that account (a checkout's owner, say), so files it writes keep their owner.
	User string
	// Root marks commands that need root; they go through passwordless sudo when the monitor is not root.
	Root    bool
	Timeout time.Duration
	// Output, when set, also gets what the command prints as it prints it.
	Output io.Writer
}

// Output is what a command printed and how it exited.
type Output struct {
	Stdout, Stderr string
	Code           int
}

// Tail is the end of the output, where a failing command usually says why.
func (o Output) Tail(n int) string {
	s := o.Stderr
	if len(bytes.TrimSpace([]byte(s))) == 0 {
		s = o.Stdout
	}
	s = string(bytes.TrimSpace([]byte(s)))
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

// Runner runs a command. err is set when it could not start or ran out of time, not for a
// non-zero exit, which Output.Code reports. Tests substitute a fake.
type Runner func(ctx context.Context, c Command) (Output, error)

var passthroughEnv = []string{"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR"}

const defaultPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Exec runs commands on this machine with a minimal environment.
func Exec(ctx context.Context, c Command) (Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	var env []string
	for _, k := range passthroughEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return len(kv) > 5 && kv[:5] == "PATH=" }) {
		env = append(env, defaultPath)
	}
	argv := c.Argv
	root := os.Geteuid() == 0
	// sudo clears the environment it is given, so what the command needs goes through env instead.
	sudo := func(args ...string) []string {
		if len(c.Env) > 0 {
			args = append(append(args, "env"), c.Env...)
		}
		return append(args, c.Argv...)
	}
	switch {
	case c.User != "" && !isCurrentUser(c.User):
		if u, err := user.Lookup(c.User); err == nil {
			env = append(env, "HOME="+u.HomeDir, "USER="+c.User)
		}
		if root {
			argv = append([]string{"runuser", "-u", c.User, "--"}, argv...)
		} else {
			argv = sudo("sudo", "-n", "-u", c.User, "--")
		}
	case c.Root && !root:
		argv = sudo("sudo", "-n", "--")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = append(env, c.Env...)
	var stdout, stderr OutputBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.Output != nil {
		cmd.Stdout, cmd.Stderr = io.MultiWriter(&stdout, c.Output), io.MultiWriter(&stderr, c.Output)
	}
	// Out of time, the whole group goes: runuser, sudo and sh -c start the real work as a child,
	// which killing only them would leave running. Whatever still holds the output open after
	// that is not waited for.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	out := Output{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	// It finished, but left something running in the background that kept its output open.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		out.Code = exitErr.ExitCode()
		return out, nil
	}
	return out, err
}

func isCurrentUser(name string) bool {
	u, err := user.Current()
	return err == nil && u.Username == name
}

// MaxOutput is the most of each stream an Output keeps: half from its start, half from its end. A
// build that prints without end would otherwise fill the agent's 128 MB in the middle of an update.
const MaxOutput = 4 << 20

// OutputBuffer keeps what a command prints, up to MaxOutput: its start, where a parser looks, and
// its end, where a failing command says why. What comes between is left out, and String says how
// much. The zero value is ready to use; like bytes.Buffer, one writer at a time.
type OutputBuffer struct {
	head []byte
	// tail is a ring of the last bytes once head is full: written at end, wrapped once full.
	tail      []byte
	end       int
	tailBytes int64 // written to tail in all
}

const outputHalf = MaxOutput / 2

func (b *OutputBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := outputHalf - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	b.tailBytes += int64(len(p))
	if len(p) > outputHalf {
		p = p[len(p)-outputHalf:]
	}
	if len(b.tail) < outputHalf {
		// Still filling: grows as it goes, so a short output takes little.
		room := outputHalf - len(b.tail)
		take := min(room, len(p))
		b.tail = append(b.tail, p[:take]...)
		p = p[take:]
		b.end = len(b.tail) % outputHalf
	}
	for len(p) > 0 {
		c := copy(b.tail[b.end:], p)
		p = p[c:]
		b.end = (b.end + c) % outputHalf
	}
	return n, nil
}

// Len is how much it keeps.
func (b *OutputBuffer) Len() int { return len(b.head) + len(b.tail) }

func (b *OutputBuffer) String() string {
	var tail []byte
	if len(b.tail) < outputHalf {
		tail = b.tail
	} else {
		tail = append(append(make([]byte, 0, outputHalf), b.tail[b.end:]...), b.tail[:b.end]...)
	}
	if left := b.tailBytes - int64(len(tail)); left > 0 {
		return fmt.Sprintf("%s\n… %d bytes of output left out …\n%s", b.head, left, tail)
	}
	return string(b.head) + string(tail)
}

// shell runs a command line with sh.
func shell(line string) []string { return []string{"sh", "-c", line} }
