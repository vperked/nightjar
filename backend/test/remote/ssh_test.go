package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/remote"
	"nightjar/internal/storage"
)

// fakeSSH stands in for ssh and ssh-keygen: ssh-keygen -f writes a key pair, -l names one, and ssh
// records its arguments and answers with reply.
type fakeSSH struct {
	mu     sync.Mutex
	calls  [][]string
	stdins [][]byte
	reply  string
	stderr string
	fail   bool
	// replies answers particular remote commands.
	replies map[string]string
}

func (f *fakeSSH) exec(_ context.Context, stdin []byte, name string, args ...string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.stdins = append(f.stdins, stdin)
	if r, ok := f.replies[args[len(args)-1]]; ok && name == "ssh" {
		return r, "", nil
	}
	switch {
	case name == "ssh-keygen" && slices.Contains(args, "-l"):
		return "256 SHA256:abcdef nightjar@test (ED25519)\n", "", nil
	case name == "ssh-keygen":
		path := args[len(args)-1]
		os.WriteFile(path, []byte("PRIVATE"), 0o600)
		os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAAC3Nz nightjar@test\n"), 0o644)
		return "", "", nil
	case name == "ssh" && f.fail:
		return "", f.stderr, &os.PathError{Op: "ssh", Err: os.ErrPermission}
	}
	return f.reply, "", nil
}

func (f *fakeSSH) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func sshMonitor(t *testing.T) (*remote.Monitor, *fakeSSH, string, *storage.Store) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "ssh")
	f := &fakeSSH{}
	db := openDB(t)
	m := remote.New(remote.Options{Store: db, Alerts: &alerts{}, SSHDir: dir, Exec: f.exec})
	return m, f, dir, db
}

// withAgent is a watched server with a (fake) agent on 127.0.0.1.
func withAgent(t *testing.T, m *remote.Monitor) remote.State {
	a := newAgent(t)
	st, err := m.Add(context.Background(), "box", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSSHKeysAreFoundAndMadeWithoutReadingThePrivateHalf(t *testing.T) {
	m, _, dir, _ := sshMonitor(t)
	ctx := context.Background()
	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("SECRET"), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 AAAAhome me@laptop\n"), 0o644)
	// A .pub with no private key beside it, and known_hosts, are not keys.
	os.WriteFile(filepath.Join(home, ".ssh", "lonely.pub"), []byte("ssh-ed25519 AAAAx\n"), 0o644)
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte("nas ssh-ed25519 AAAA\n"), 0o644)

	keys := m.SSHKeys(ctx)
	if len(keys) != 1 || keys[0].Name != "id_ed25519" || keys[0].Own || keys[0].Fingerprint != "SHA256:abcdef" || keys[0].Type != "ED25519" || keys[0].Comment != "me@laptop" {
		t.Fatalf("keys = %+v", keys)
	}
	for _, k := range keys {
		if strings.Contains(k.Public, "SECRET") {
			t.Fatal("the private key leaked")
		}
	}

	k, err := m.NewSSHKey(ctx, "nightjar")
	if err != nil || !k.Own || k.Path != filepath.Join(dir, "nightjar") || !strings.HasPrefix(k.Public, "ssh-ed25519 ") {
		t.Fatalf("new key = %+v, %v", k, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the key folder should be private: %v %v", st, err)
	}
	if _, err := m.NewSSHKey(ctx, "nightjar"); err == nil {
		t.Fatal("a second key of the same name was made")
	}
	for _, bad := range []string{"../escape", "known_hosts", "-rf"} {
		if _, err := m.NewSSHKey(ctx, bad); err == nil {
			t.Fatalf("made a key called %q", bad)
		}
	}
	if got := m.SSHKeys(ctx); len(got) != 2 || got[0].Name != "nightjar" {
		t.Fatalf("own keys come first: %+v", got)
	}
}

func TestSSHLoginIsCheckedSavedAndUsed(t *testing.T) {
	m, f, dir, db := sshMonitor(t)
	ctx := context.Background()
	h := withAgent(t, m)
	k, err := m.NewSSHKey(ctx, "nightjar")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.RunSSH(ctx, h.ID, remote.SSHTest); err == nil || !strings.Contains(err.Error(), "first") {
		t.Fatalf("no login yet: %v", err)
	}
	for _, bad := range []remote.SSH{
		{Key: "/etc/shadow"},
		{Key: k.Path, User: "root; rm -rf /"},
		{Key: k.Path, Port: 70000},
		{Key: k.Path, Host: "-oProxyCommand=evil"},
	} {
		if _, err := m.SetSSH(ctx, h.ID, &bad); err == nil {
			t.Fatalf("saved %+v", bad)
		}
	}

	st, err := m.SetSSH(ctx, h.ID, &remote.SSH{Key: k.Path})
	if err != nil || st.SSH == nil || st.SSH.User != "root" || st.SSH.Port != 22 {
		t.Fatalf("defaults: %+v %v", st.SSH, err)
	}
	if st.SSHCommand != "ssh -i "+k.Path+" root@127.0.0.1" {
		t.Fatalf("command = %q", st.SSHCommand)
	}

	f.reply = "root@box\n 10:00 up 3 days\n"
	out, err := m.RunSSH(ctx, h.ID, remote.SSHTest)
	if err != nil || out != "root@box\n 10:00 up 3 days" {
		t.Fatalf("test = %q, %v", out, err)
	}
	args := f.last()
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i " + k.Path, "-p 22", "-l root", "BatchMode=yes", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=" + filepath.Join(dir, "known_hosts"), "-- 127.0.0.1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh args miss %q: %s", want, joined)
		}
	}

	f.reply = "nightjar-agent restarted\n"
	if out, err := m.RunSSH(ctx, h.ID, remote.SSHRestartAgent); err != nil || out != "nightjar-agent restarted" || !strings.Contains(strings.Join(f.last(), " "), "systemctl restart nightjar-agent") {
		t.Fatalf("restart the agent: %q %v", out, err)
	}
	if _, err := m.RunSSH(ctx, h.ID, "rm -rf /"); err == nil {
		t.Fatal("ran an action that is not one of the fixed ones")
	}

	f.fail, f.stderr = true, "root@127.0.0.1: Permission denied (publickey).\n"
	if _, err := m.RunSSH(ctx, h.ID, remote.SSHTest); err == nil || !strings.Contains(err.Error(), "authorized_keys") {
		t.Fatalf("a refused key says what to do: %v", err)
	}
	f.stderr = "Connection to 127.0.0.1 closed by remote host.\n"
	if out, err := m.RunSSH(ctx, h.ID, remote.SSHReboot); err != nil || out != "rebooting" {
		t.Fatalf("a reboot drops the connection, and that is fine: %q %v", out, err)
	}

	// The login survives a restart of the monitor, and can be forgotten.
	again := remote.New(remote.Options{Store: db, Alerts: &alerts{}, SSHDir: dir, Exec: f.exec})
	if got, _ := again.Get(h.ID); got.SSH == nil || got.SSH.Key != k.Path {
		t.Fatalf("after a restart: %+v", got.SSH)
	}
	if st, err := m.SetSSH(ctx, h.ID, nil); err != nil || st.SSH != nil || st.SSHCommand != "" {
		t.Fatalf("forget: %+v %v", st.SSH, err)
	}
}

func TestOwnKeysAreKeptPrivateAndPassphrasesRefused(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("needs ssh-keygen")
	}
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "ssh")
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}, SSHDir: dir})
	ctx := context.Background()
	mine := t.TempDir()
	for _, k := range []struct{ file, pass string }{{"plain", ""}, {"locked", "hunter2"}} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", k.pass, "-C", "me@laptop", "-f", filepath.Join(mine, k.file)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %s", out)
		}
	}
	plain, _ := os.ReadFile(filepath.Join(mine, "plain"))
	pub, _ := os.ReadFile(filepath.Join(mine, "plain.pub"))

	// Pasted from Windows, with \r\n line ends.
	k, err := m.ImportSSHKey(ctx, "laptop", strings.ReplaceAll(string(plain), "\n", "\r\n"))
	if err != nil || !k.Own || k.Name != "laptop" || k.Type != "ED25519" {
		t.Fatalf("import: %+v %v", k, err)
	}
	if strings.Join(strings.Fields(k.Public)[:2], " ") != strings.Join(strings.Fields(string(pub))[:2], " ") {
		t.Fatalf("the public half is worked out from it: %q vs %q", k.Public, pub)
	}
	if st, _ := os.Stat(k.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("the private key is owner-only: %v", st.Mode())
	}
	if !slices.ContainsFunc(m.SSHKeys(ctx), func(x remote.SSHKey) bool { return x.Path == k.Path }) {
		t.Fatal("it is listed to choose")
	}

	locked, _ := os.ReadFile(filepath.Join(mine, "locked"))
	if _, err := m.ImportSSHKey(ctx, "locked", string(locked)); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("a key with a passphrase: %v", err)
	}
	if _, err := m.ImportSSHKey(ctx, "pub", string(pub)); err == nil {
		t.Fatal("a public key is not a private key")
	}
	if _, err := m.ImportSSHKey(ctx, "laptop", string(plain)); err == nil {
		t.Fatal("a second key of the same name")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".import-*")); len(left) != 0 {
		t.Fatalf("no half-imported files left behind: %v", left)
	}
}

func TestServersCanBeJustAnSSHLogin(t *testing.T) {
	m, _, _, db := sshMonitor(t)
	ctx := context.Background()
	k, err := m.NewSSHKey(ctx, "nightjar")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if _, err := m.AddSSHServer(ctx, "vps", remote.SSH{Key: k.Path, Port: port}); err == nil {
		t.Fatal("added with no address")
	}
	st, err := m.AddSSHServer(ctx, "vps", remote.SSH{Host: "127.0.0.1", Port: port, Key: k.Path})
	if err != nil || st.Kind != remote.KindSSH || st.Status != "online" || st.Address != "127.0.0.1" || st.SSH == nil || st.SSH.User != "root" {
		t.Fatalf("add: %+v %v", st, err)
	}
	if st.SSHCommand != fmt.Sprintf("ssh -i %s -p %d root@127.0.0.1", k.Path, port) {
		t.Fatalf("command: %q", st.SSHCommand)
	}
	if _, err := m.AddSSHServer(ctx, "again", remote.SSH{Host: "127.0.0.1", Port: port, Key: k.Path}); !errors.Is(err, remote.ErrExists) {
		t.Fatalf("the same login twice: %v", err)
	}
	if _, err := m.RunSSH(ctx, st.ID, remote.SSHRestartAgent); err == nil {
		t.Fatal("restarted an agent it does not have")
	}
	if _, err := m.SetSSH(ctx, st.ID, nil); err == nil {
		t.Fatal("its login is all it has; forgetting it would leave nothing")
	}
	if _, err := m.Update(ctx, st.ID, "vps", "https://127.0.0.1:9101", token); err == nil {
		t.Fatal("it has no agent address to change")
	}
	if got, err := m.Rename(st.ID, "ny-ovh"); err != nil || got.Name != "ny-ovh" {
		t.Fatalf("rename: %+v %v", got, err)
	}

	// Offline once its SSH port stops answering three times; nothing about it is recorded for analytics.
	ln.Close()
	for range 3 {
		m.PollAll(ctx)
	}
	if got, _ := m.Get(st.ID); got.Status != "offline" || !strings.Contains(got.Error, "nothing answered SSH") {
		t.Fatalf("offline: %+v", got)
	}
	if rows := db.SampleBuckets(st.ID, 0, 60); len(rows) != 0 {
		t.Fatalf("no readings for a server with no agent: %+v", rows)
	}

	// Changing its login moves it; it survives a restart.
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln2.Close()
	port2 := ln2.Addr().(*net.TCPAddr).Port
	if got, err := m.SetSSH(ctx, st.ID, &remote.SSH{Host: "127.0.0.1", Port: port2, Key: k.Path, User: "admin"}); err != nil || got.SSH.Port != port2 || got.SSH.User != "admin" {
		t.Fatalf("change login: %+v %v", got, err)
	}
	m.PollAll(ctx)
	if got, _ := m.Get(st.ID); got.Status != "online" {
		t.Fatalf("back online on the new port: %+v", got)
	}
	again := remote.New(remote.Options{Store: db, Alerts: &alerts{}, SSHDir: filepath.Dir(k.Path)})
	if got, ok := again.Get(st.ID); !ok || got.Kind != remote.KindSSH || got.Name != "ny-ovh" || got.SSH.Port != port2 {
		t.Fatalf("after a restart: %+v", got)
	}

	ln3, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln3.Addr().(*net.TCPAddr).Port
	ln3.Close()
	if _, err := m.AddSSHServer(ctx, "gone", remote.SSH{Host: "127.0.0.1", Port: closed, Key: k.Path}); err == nil || !strings.Contains(err.Error(), "nothing answered SSH") {
		t.Fatalf("a closed port is not added: %v", err)
	}
}

func TestTheAgentIsReinstalledOverSSH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "ssh")
	f := &fakeSSH{replies: map[string]string{"uname -m": "aarch64\n"}}
	var asked []string
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}, SSHDir: dir, Exec: f.exec,
		AgentBuild: func(arch string) ([]byte, string, error) {
			asked = append(asked, arch)
			return []byte("BUILD-" + arch), "h", nil
		}})
	ctx := context.Background()
	a := newAgent(t)
	h, err := m.Add(ctx, "box", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := m.NewSSHKey(ctx, "nightjar")
	if _, err := m.SetSSH(ctx, h.ID, &remote.SSH{Key: k.Path}); err != nil {
		t.Fatal(err)
	}

	// Its readings do not say which processor it has, so the server is asked; the build goes in on stdin.
	f.reply = "Reinstalling with this machine's agent settings.\nThe agent is running as nightjar-agent.service\n"
	out, err := m.RunSSH(ctx, h.ID, remote.SSHReinstallAgent)
	if err != nil || !strings.Contains(out, "Reinstalling") {
		t.Fatalf("reinstall: %q %v", out, err)
	}
	last := strings.Join(f.last(), " ")
	if !strings.HasSuffix(last, `"$f" install; fi`) || strings.Contains(last, token) || string(f.stdins[len(f.stdins)-1]) != "BUILD-arm64" {
		t.Fatalf("command %q with stdin %q", last, f.stdins[len(f.stdins)-1])
	}
	if got, _ := m.Get(h.ID); got.Update == nil || got.Update.State != "updated" {
		t.Fatalf("its page says it was reinstalled: %+v", got.Update)
	}

	// When its readings name its processor, nobody needs to ask.
	a.set(func(r *agent.Readings, _ *bool) { r.System.Architecture = "amd64" })
	m.PollAll(ctx)
	calls := len(f.calls)
	if _, err := m.RunSSH(ctx, h.ID, remote.SSHReinstallAgent); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != calls+1 || string(f.stdins[len(f.stdins)-1]) != "BUILD-amd64" {
		t.Fatalf("%d calls, stdin %q", len(f.calls)-calls, f.stdins[len(f.stdins)-1])
	}
	if asked[0] != "arm64" || slices.Contains(asked, "") {
		t.Fatalf("builds asked for: %v", asked)
	}
}
