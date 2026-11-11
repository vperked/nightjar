package remote_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/remote"
	"nightjar/internal/sshd"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFloufjWe/SlT1cjuMRtrfu1swKcgY5YDp53eKuIR9oL test@nightjar"

// sshdMachine is a pretend machine for SSH logins: sshd's folder, one account (named after whoever
// can be given files: alice when run as root) and a fake sshd and systemctl.
func sshdMachine(t *testing.T) (*sshd.Manager, string) {
	t.Helper()
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	if uid < 1000 {
		t.Skip("needs to run as root or as a user with uid 1000 or more")
	}
	root := t.TempDir()
	etc := filepath.Join(root, "ssh")
	os.MkdirAll(filepath.Join(etc, "sshd_config.d"), 0o755)
	os.WriteFile(filepath.Join(etc, "sshd_config"), []byte("Include sshd_config.d/*.conf\n"), 0o644)
	os.Mkdir(filepath.Join(root, "alice"), 0o755)
	os.WriteFile(filepath.Join(root, "passwd"), []byte("alice:x:"+strconv.Itoa(uid)+":"+strconv.Itoa(gid)+"::"+filepath.Join(root, "alice")+":/bin/bash\n"), 0o644)
	exec := func(_ context.Context, name string, args ...string) (string, error) {
		if name+" "+strings.Join(args, " ") == "sshd -T" {
			if _, err := os.Stat(filepath.Join(etc, "sshd_config.d", sshd.DropIn)); err == nil {
				return "passwordauthentication no\n", nil
			}
			return "passwordauthentication yes\n", nil
		}
		return "", nil
	}
	return &sshd.Manager{Dir: etc, Passwd: filepath.Join(root, "passwd"), Exec: exec}, etc
}

func TestAnAgentsSSHLoginsAreManagedThroughIt(t *testing.T) {
	ctx := context.Background()
	m, _ := sshdMachine(t)
	s := agent.NewSampler("test")
	s.UseSSHD(m)
	s.Sample(ctx)
	srv := httptest.NewServer(agent.Handler(s, token, nil, true))
	defer srv.Close()

	rm := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	h, err := rm.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rm.SSHD(ctx, h.ID)
	if err != nil || !v.PasswordLogin || v.KeysOnly || len(v.Users) != 1 || v.Users[0].Keys != 0 {
		t.Fatalf("view: %+v %v", v, err)
	}
	// The agent's refusal comes back in its words.
	var ae *remote.AgentError
	if _, err := rm.SetKeysOnly(ctx, h.ID, true, "alice"); !errors.As(err, &ae) || !strings.Contains(err.Error(), "add a key for alice first") {
		t.Fatalf("no key yet: %v", err)
	}
	if v, err = rm.AddSSHKey(ctx, h.ID, "alice", testKey); err != nil || v.Users[0].Keys != 1 {
		t.Fatalf("add key: %+v %v", v, err)
	}
	if v, err = rm.SetKeysOnly(ctx, h.ID, true, "alice"); err != nil || !v.KeysOnly || v.PasswordLogin || v.Pending == nil {
		t.Fatalf("keys only: %+v %v", v, err)
	}
	if v, err = rm.KeepKeysOnly(ctx, h.ID, true); err != nil || v.Pending != nil || !v.KeysOnly {
		t.Fatalf("keep: %+v %v", v, err)
	}
	if v, err = rm.SetKeysOnly(ctx, h.ID, false, ""); err != nil || v.KeysOnly || !v.PasswordLogin {
		t.Fatalf("off: %+v %v", v, err)
	}
}

func TestAnAgentTooOldForSSHLoginsSaysToUpdateIt(t *testing.T) {
	ctx := context.Background()
	// An agent from before: readings, and nothing at the SSH paths.
	s := agent.NewSampler("test")
	s.Sample(ctx)
	h := agent.Handler(s, token, nil, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, agent.SSHDPath) {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	rm := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	host, err := rm.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	var ae *remote.AgentError
	if _, err := rm.SSHD(ctx, host.ID); !errors.As(err, &ae) || ae.Status != http.StatusNotFound || !strings.Contains(err.Error(), "update it from the Servers page") {
		t.Fatalf("too old: %v", err)
	}
	if _, err := rm.SSHD(ctx, "nope"); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("no such server: %v", err)
	}
}
