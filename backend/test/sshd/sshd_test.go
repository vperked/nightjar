package sshd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"nightjar/internal/sshd"
)

// Public keys made for these tests with ssh-keygen, and what ssh-keygen -lf says of each.
const (
	ed25519Key   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFloufjWe/SlT1cjuMRtrfu1swKcgY5YDp53eKuIR9oL test@nightjar"
	ed25519Print = "SHA256:4q0pngrU2QKgaMxlLNyyuz1/Uum+PsGm1GRhFUkoDys"
	rsaKey       = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCwILzs098hyBN1e4FPIE3GxA0mstXUPEE1aqtOw+xzj4B9ifxOTkIFhixildOX1HA5IdqfC/OzbdU84FyoZ6cFxPBiiso0utcLjY92EVfVUFYtuUgszpN46grLFMU9yq/OujMWvptUE51bpnBYZnXUJxjVgbkjtLRsT2kMf3shZSL5iMt1yTXJSw+GtnBnMHsCZBUrB+xdGlguvHPRkiwT+qp7RCgDHjCEZU6E0oceb21v2bvMxZTzGcN0JgacfwTH5TxGHFKAKQnP0jix429V69Rp/mn0HfzbgjHH2u+XatruTXHjIOSAlrijUVhx5rSU7u1Zx3z2DHEiV/f99L5J rsa@nightjar"
	rsaPrint     = "SHA256:nAwT6Rp/FPwrfpem6GE4lPMw3sgrosJ9EIhWmRPa79Q"
	ecdsaKey     = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBNJClF6SLJDA8uU/zRqHCSzXi2ZGQO9iHoThfL4wxJhUcgPJm1TiT4IVoFmUc/R4B1k3VZXCVGjnUSftBB5CxlU= ec"
	ecdsaPrint   = "SHA256:uAoN1HRmWNJtULnW8eoEIhC+TEr5lBzOqkvxwtr5HbM"
)

func TestPublicKeysAreReadLikeSSHKeygen(t *testing.T) {
	for line, print := range map[string]string{ed25519Key: ed25519Print, rsaKey: rsaPrint, ecdsaKey: ecdsaPrint} {
		k, err := sshd.ParseKey("  " + line + "\n")
		if err != nil {
			t.Fatalf("%s: %v", line[:12], err)
		}
		if k.Fingerprint() != print {
			t.Errorf("%s: fingerprint %s, want %s", k.Type, k.Fingerprint(), print)
		}
		if k.Line() != line {
			t.Errorf("%s: line %q", k.Type, k.Line())
		}
	}
	// No comment is fine.
	if k, err := sshd.ParseKey(strings.Join(strings.Fields(ed25519Key)[:2], " ")); err != nil || k.Comment != "" {
		t.Fatalf("no comment: %+v %v", k, err)
	}
}

func TestWhatIsNotAPublicKeyIsTurnedDown(t *testing.T) {
	fields := strings.Fields(ed25519Key)
	for text, want := range map[string]string{
		"":                               "paste a public key",
		"hello there":                    "not a key type",
		"ssh-ed25519":                    "missing",
		"ssh-ed25519 !!!notb64":          "not valid base64",
		"ssh-ed25519 AAAA":               "cut short",
		fields[0] + " " + fields[1][:40]: "cut short",
		// The data of an ed25519 key under the rsa type.
		"ssh-rsa " + fields[1]: "is a ssh-ed25519 key",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----": "private key",
		"-----BEGIN RSA PRIVATE KEY-----":                                      "private key",
		"---- BEGIN SSH2 PUBLIC KEY ----\nAAAA\n---- END SSH2 PUBLIC KEY ----": "PuTTY",
		ed25519Key + "\n" + rsaKey:                                             "one key at a time",
		"ssh-dss AAAAB3NzaC1kc3M=":                                             "not a key type",
	} {
		if _, err := sshd.ParseKey(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", clip(text), err, want)
		}
	}
}

func clip(s string) string {
	if len(s) > 30 {
		return s[:30]
	}
	return s
}

// machine is a pretend machine: sshd's config folder, a passwd file and home folders, and a fake
// sshd and systemctl.
type machine struct {
	t       *testing.T
	root    string
	etc     string
	m       *sshd.Manager
	uid     int // alice's
	gid     int
	mu      sync.Mutex
	now     time.Time
	calls   []string
	badConf bool   // sshd -t turns the config down
	stillPW bool   // sshd -T still says passwords, whatever the config
	noUnit  string // the systemctl unit that does not exist
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	x := &machine{t: t, root: t.TempDir(), now: time.Unix(1_800_000_000, 0), uid: os.Getuid(), gid: os.Getgid()}
	// A test run as root gives alice an account of her own; otherwise she is whoever runs it, who can
	// be given files.
	if x.uid == 0 {
		x.uid, x.gid = 1000, 1000
	}
	if x.uid < 1000 {
		t.Skip("needs to run as root or as a user with uid 1000 or more")
	}
	x.etc = filepath.Join(x.root, "etc", "ssh")
	must(t, os.MkdirAll(filepath.Join(x.etc, "sshd_config.d"), 0o755))
	x.config("Port 2222\nInclude " + filepath.Join(x.etc, "sshd_config.d") + "/*.conf\nUsePAM yes\n")
	for _, h := range []string{"root", "home/alice", "home/bob"} {
		must(t, os.MkdirAll(filepath.Join(x.root, h), 0o755))
	}
	passwd := strings.Join([]string{
		"root:x:0:0:root:" + filepath.Join(x.root, "root") + ":/bin/bash",
		"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin",
		"alice:x:" + strconv.Itoa(x.uid) + ":" + strconv.Itoa(x.gid) + ":Alice:" + filepath.Join(x.root, "home/alice") + ":/bin/bash",
		"bob:x:1001:1001::" + filepath.Join(x.root, "home/bob") + ":/usr/sbin/nologin",
		"carol:x:1002:1002::" + filepath.Join(x.root, "home/carol") + ":/bin/bash",
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin",
	}, "\n") + "\n"
	must(t, os.WriteFile(filepath.Join(x.root, "passwd"), []byte(passwd), 0o644))
	x.m = &sshd.Manager{Dir: x.etc, Passwd: filepath.Join(x.root, "passwd"), Exec: x.exec, Now: x.clock}
	return x
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (x *machine) config(s string) {
	must(x.t, os.WriteFile(filepath.Join(x.etc, "sshd_config"), []byte(s), 0o644))
}

func (x *machine) dropIn() string { return filepath.Join(x.etc, "sshd_config.d", sshd.DropIn) }

func (x *machine) clock() time.Time {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.now
}

func (x *machine) wait(d time.Duration) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.now = x.now.Add(d)
}

func (x *machine) exec(_ context.Context, name string, args ...string) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	x.calls = append(x.calls, call)
	switch call {
	case "sshd -t":
		if x.badConf {
			return "/etc/ssh/sshd_config.d/00-nightjar-keys-only.conf line 3: Bad configuration option: x", errors.New("exit status 255")
		}
	case "sshd -T":
		pw := "yes"
		if _, err := os.Stat(x.dropIn()); err == nil && !x.stillPW {
			pw = "no"
		}
		return "port 2222\nusepam yes\npasswordauthentication " + pw + "\nkbdinteractiveauthentication no\n", nil
	case "systemctl reload " + x.noUnit:
		return "Failed to reload " + x.noUnit + ".service: Unit " + x.noUnit + ".service not found.", errors.New("exit status 5")
	}
	return "", nil
}

func (x *machine) ran(call string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, c := range x.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (x *machine) giveRootAKey() {
	must(x.t, os.MkdirAll(filepath.Join(x.root, "root/.ssh"), 0o700))
	must(x.t, os.WriteFile(filepath.Join(x.root, "root/.ssh/authorized_keys"), []byte(`# mine
from="10.0.0.0/8",command="echo \"hi there\"" `+rsaKey+"\n"), 0o600))
}

func TestTheAccountsThatCanLogInAndTheirKeys(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	a := x.m.State(context.Background())
	if a.Problem != "" || !a.PasswordLogin || a.KeysOnly || a.Pending != nil {
		t.Fatalf("state: %+v", a)
	}
	if len(a.Ports) != 1 || a.Ports[0] != 2222 {
		t.Errorf("ports: %v", a.Ports)
	}
	// root and alice; not daemon or nobody (system accounts), bob (no login shell) or carol (no home).
	if len(a.Users) != 2 || a.Users[0].Name != "root" || a.Users[1].Name != "alice" {
		t.Fatalf("users: %+v", a.Users)
	}
	if a.Users[0].Keys != 1 || a.Users[0].Fingerprints[0] != rsaPrint {
		t.Errorf("root's key, behind options: %+v", a.Users[0])
	}
	if a.Users[1].Keys != 0 || a.Users[1].Fingerprints == nil {
		t.Errorf("alice has none: %+v", a.Users[1])
	}
}

func TestAKeyIsAddedSafely(t *testing.T) {
	x := newMachine(t)
	ctx := context.Background()
	if err := x.m.AddKey(ctx, "bob", ed25519Key); err == nil || !strings.Contains(err.Error(), "no account") {
		t.Fatalf("an account that can't log in: %v", err)
	}
	if err := x.m.AddKey(ctx, "alice", "-----BEGIN OPENSSH PRIVATE KEY-----"); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("a private key: %v", err)
	}
	must(t, x.m.AddKey(ctx, "alice", ed25519Key))
	dir := filepath.Join(x.root, "home/alice/.ssh")
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh: %v %v", st, err)
	}
	file := filepath.Join(dir, "authorized_keys")
	st, err = os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys: %v %v", st, err)
	}
	// Both are alice's, as sshd wants them.
	for _, p := range []string{dir, file} {
		st, _ := os.Stat(p)
		if sys := st.Sys().(*syscall.Stat_t); int(sys.Uid) != x.uid || int(sys.Gid) != x.gid {
			t.Errorf("%s belongs to %d:%d", p, sys.Uid, sys.Gid)
		}
	}
	// The same key again changes nothing; another is added after it, and what was there is kept.
	must(t, x.m.AddKey(ctx, "alice", strings.Replace(ed25519Key, "test@nightjar", "another name", 1)))
	must(t, os.WriteFile(file, append(must2(os.ReadFile(file)), "# a note, no newline"...), 0o600))
	must(t, x.m.AddKey(ctx, "alice", rsaKey))
	b := string(must2(os.ReadFile(file)))
	if b != ed25519Key+"\n# a note, no newline\n"+rsaKey+"\n" {
		t.Fatalf("authorized_keys:\n%s", b)
	}
	a := x.m.State(ctx)
	if a.Users[1].Keys != 2 || a.Users[1].Fingerprints[0] != ed25519Print || a.Users[1].Fingerprints[1] != rsaPrint {
		t.Fatalf("alice: %+v", a.Users[1])
	}
	// Nothing is left behind from writing.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left in .ssh: %v", entries)
	}
}

func must2[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestAKeyIsNotWrittenThroughALink(t *testing.T) {
	x := newMachine(t)
	ctx := context.Background()
	target := filepath.Join(x.root, "shadow")
	must(t, os.WriteFile(target, []byte("root:secret\n"), 0o600))
	dir := filepath.Join(x.root, "home/alice/.ssh")
	must(t, os.Mkdir(dir, 0o700))
	must(t, os.Symlink(target, filepath.Join(dir, "authorized_keys")))
	if err := x.m.AddKey(ctx, "alice", ed25519Key); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a linked authorized_keys: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "root:secret\n" {
		t.Fatalf("written through the link: %q", b)
	}
	// Nor through ~/.ssh as a link to somewhere else.
	must(t, os.RemoveAll(dir))
	must(t, os.Symlink(x.root, dir))
	if err := x.m.AddKey(ctx, "alice", ed25519Key); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a linked .ssh: %v", err)
	}
	// Nor a hard link to another file: its contents would be copied into the account's.
	must(t, os.Remove(dir))
	must(t, os.Mkdir(dir, 0o700))
	must(t, os.Link(target, filepath.Join(dir, "authorized_keys")))
	if err := x.m.AddKey(ctx, "alice", ed25519Key); err == nil || !strings.Contains(err.Error(), "other names") {
		t.Fatalf("a hard-linked authorized_keys: %v", err)
	}
}

func TestKeysOnlyNeedsAKeyFirst(t *testing.T) {
	x := newMachine(t)
	ctx := context.Background()
	if err := x.m.SetKeysOnly(ctx, true, "root"); err == nil || !strings.Contains(err.Error(), "add a key for root first") {
		t.Fatalf("without a key: %v", err)
	}
	if err := x.m.SetKeysOnly(ctx, true, ""); err == nil {
		t.Fatal("without an account")
	}
	if _, err := os.Stat(x.dropIn()); err == nil || x.ran("systemctl reload ssh") != 0 {
		t.Fatal("nothing should have changed")
	}
}

func TestKeysOnlyIsKeptOrGoesBack(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	ctx := context.Background()
	must(t, x.m.SetKeysOnly(ctx, true, "root"))
	b, err := os.ReadFile(x.dropIn())
	if err != nil || !strings.Contains(string(b), "PasswordAuthentication no\n") || !strings.Contains(string(b), "KbdInteractiveAuthentication no\n") {
		t.Fatalf("drop-in: %s %v", b, err)
	}
	if x.ran("sshd -t") != 1 || x.ran("systemctl reload ssh") != 1 {
		t.Fatalf("checked and reloaded: %v", x.calls)
	}
	a := x.m.State(ctx)
	if !a.KeysOnly || a.PasswordLogin || a.Pending == nil || a.Pending.Until != x.clock().Add(2*time.Minute).Unix() {
		t.Fatalf("keys only, waiting: %+v", a)
	}

	// Not kept in time, it goes back by itself.
	x.wait(time.Minute)
	x.m.Lapse(ctx)
	if _, err := os.Stat(x.dropIn()); err != nil {
		t.Fatal("went back too soon")
	}
	x.wait(time.Minute)
	x.m.Lapse(ctx)
	if a := x.m.State(ctx); a.KeysOnly || !a.PasswordLogin || a.Pending != nil || x.ran("systemctl reload ssh") != 2 {
		t.Fatalf("went back: %+v", a)
	}
	if err := x.m.Keep(ctx, true); err == nil {
		t.Fatal("nothing to keep")
	}

	// Kept, it stays, with no deadline left in the file.
	must(t, x.m.SetKeysOnly(ctx, true, "root"))
	must(t, x.m.Keep(ctx, true))
	x.wait(time.Hour)
	x.m.Lapse(ctx)
	a = x.m.State(ctx)
	if !a.KeysOnly || a.Pending != nil || a.PasswordLogin {
		t.Fatalf("kept: %+v", a)
	}
	// Again changes nothing; off goes at once.
	must(t, x.m.SetKeysOnly(ctx, true, "root"))
	if a := x.m.State(ctx); a.Pending != nil {
		t.Fatalf("on again: %+v", a)
	}
	must(t, x.m.SetKeysOnly(ctx, false, ""))
	if a := x.m.State(ctx); a.KeysOnly || !a.PasswordLogin {
		t.Fatalf("off: %+v", a)
	}

	// Undone while waiting, it goes back at once.
	must(t, x.m.SetKeysOnly(ctx, true, "root"))
	must(t, x.m.Keep(ctx, false))
	if a := x.m.State(ctx); a.KeysOnly || a.Pending != nil {
		t.Fatalf("undone: %+v", a)
	}
}

func TestKeysOnlyReloadsSSHDByEitherName(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	x.noUnit = "ssh"
	must(t, x.m.SetKeysOnly(context.Background(), true, "root"))
	if x.ran("systemctl reload sshd") != 1 {
		t.Fatalf("reloaded: %v", x.calls)
	}
}

func TestKeysOnlyTurnedDownBySSHDIsTakenOut(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	ctx := context.Background()
	x.badConf = true
	if err := x.m.SetKeysOnly(ctx, true, "root"); err == nil || !strings.Contains(err.Error(), "Bad configuration option") {
		t.Fatalf("sshd -t failing: %v", err)
	}
	if _, err := os.Stat(x.dropIn()); err == nil || x.ran("systemctl reload ssh") != 0 {
		t.Fatal("the file should be gone, and sshd not reloaded")
	}
}

func TestKeysOnlyOverriddenIsTakenOut(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	ctx := context.Background()
	// The main config turns passwords on before it reads sshd_config.d: that value wins.
	x.config("PasswordAuthentication yes\nInclude " + filepath.Join(x.etc, "sshd_config.d") + "/*.conf\n")
	x.stillPW = true
	err := x.m.SetKeysOnly(ctx, true, "root")
	if err == nil || !strings.Contains(err.Error(), "sshd_config line 1 sets PasswordAuthentication yes") {
		t.Fatalf("overridden: %v", err)
	}
	if _, err := os.Stat(x.dropIn()); err == nil || x.ran("systemctl reload ssh") != 2 {
		t.Fatalf("the file should be gone and sshd reloaded again: %v", x.calls)
	}
}

func TestKeysOnlyNeedsTheConfigToReadItsFolder(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	ctx := context.Background()
	x.config("Port 22\n")
	if a := x.m.State(ctx); !strings.Contains(a.Problem, "doesn't read") {
		t.Fatalf("problem: %+v", a)
	}
	if err := x.m.SetKeysOnly(ctx, true, "root"); err == nil || !strings.Contains(err.Error(), "Include") {
		t.Fatalf("refused: %v", err)
	}
	// Read inside a Match block, it would apply to some logins only.
	x.config("Match User git\n  Include sshd_config.d/*.conf\n")
	if a := x.m.State(ctx); !strings.Contains(a.Problem, "inside a Match block") {
		t.Fatalf("in a match: %+v", a)
	}
	// A relative Include, as Debian's, is read from sshd's folder.
	x.config("Include sshd_config.d/*.conf\n")
	if a := x.m.State(ctx); a.Problem != "" {
		t.Fatalf("relative include: %+v", a)
	}
}

func TestARestartWhileWaitingPutsPasswordsBack(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	ctx := context.Background()
	must(t, x.m.SetKeysOnly(ctx, true, "root"))
	// A new run (Nightjar restarted) finds keys only waiting: nobody can keep it now.
	x.wait(10 * time.Second)
	again := &sshd.Manager{Dir: x.m.Dir, Passwd: x.m.Passwd, Exec: x.exec, Now: x.clock, CheckEvery: time.Millisecond}
	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		again.Run(run)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(x.dropIn()); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keys only should have gone back on start")
		}
		time.Sleep(time.Millisecond)
	}
	// Kept keys only is left alone by a restart, and by stopping.
	must(t, again.SetKeysOnly(ctx, true, "root"))
	must(t, again.Keep(ctx, true))
	stop()
	<-done
	if _, err := os.Stat(x.dropIn()); err != nil {
		t.Fatal("kept keys only should stay")
	}
}

func TestStoppingWhileWaitingPutsPasswordsBack(t *testing.T) {
	x := newMachine(t)
	x.giveRootAKey()
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		x.m.Run(run)
	}()
	must(t, x.m.SetKeysOnly(context.Background(), true, "root"))
	stop()
	<-done
	if _, err := os.Stat(x.dropIn()); err == nil {
		t.Fatal("keys only waiting should go back as it stops")
	}
}
