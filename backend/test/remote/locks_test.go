package remote_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
	"nightjar/internal/remote"
)

// fakeListener makes a fake /proc where one process (pid, exe, unit) listens on tcp port with the
// socket inode given, and nothing else does.
func fakeListener(t *testing.T, dir string, port, inode, pid int, exe, unit string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		fmt.Sprintf("   0: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 20 4 30 10 -1\n", port, inode)
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "net" {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	p := filepath.Join(dir, fmt.Sprint(pid))
	os.MkdirAll(filepath.Join(p, "fd"), 0o755)
	os.WriteFile(filepath.Join(p, "comm"), []byte(filepath.Base(exe)+"\n"), 0o644)
	os.Symlink(exe, filepath.Join(p, "exe"))
	os.WriteFile(filepath.Join(p, "cgroup"), []byte("0::/system.slice/"+unit+"\n"), 0o644)
	os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(p, "fd", "3"))
}

func TestAPortIsLockedThroughTheAgentAndATakenOneAnnounced(t *testing.T) {
	ctx := context.Background()
	exec := func(_ context.Context, _ []byte, name string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "-C" {
			return "", errors.New("no such rule")
		}
		return "", nil
	}
	dir := t.TempDir()
	fakeListener(t, dir, 5432, 700, 300, "/usr/lib/postgresql/16/bin/postgres", "postgresql.service")
	fw := &firewall.Manager{Exec: exec, Proc: dir}
	s := agent.NewSampler("test")
	s.UseFirewall(fw, &ddos.Watcher{Defender: &ddos.Defender{Exec: exec}}, 9101)
	s.Sample(ctx)
	srv := httptest.NewServer(agent.Handler(s, token, nil, true))
	defer srv.Close()
	sent := &alerts{}
	m := remote.New(remote.Options{Store: openDB(t), Alerts: sent})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}

	v, err := m.Firewall(ctx, h.ID)
	if err != nil || len(v.Firewall.Listening) != 1 || v.Firewall.Listening[0].Unit != "postgresql.service" || v.Firewall.Locks == nil {
		t.Fatalf("firewall: %+v %v", v.Firewall, err)
	}
	var ae *remote.AgentError
	if _, err := m.LockPort(ctx, h.ID, 6000, "tcp"); !errors.As(err, &ae) || !strings.Contains(err.Error(), "nothing listens") {
		t.Fatalf("nothing there: %v", err)
	}
	v, err = m.LockPort(ctx, h.ID, 5432, "tcp")
	if err != nil || len(v.Firewall.Locks) != 1 || v.Firewall.Locks[0].State != firewall.LockHeld {
		t.Fatalf("lock: %+v %v", v.Firewall.Locks, err)
	}
	lock := v.Firewall.Locks[0].ID

	// Something else takes the port: the agent says so with its readings, and that is announced once.
	fakeListener(t, dir, 5432, 701, 301, "/usr/bin/nc", "")
	fw.CheckLocks(ctx)
	s.Sample(ctx)
	m.PollAll(ctx)
	m.PollAll(ctx)
	if got := lockAlerts(sent.after(1)); len(got) != 1 || got[0] != "critical: Locked port taken: box" {
		t.Fatalf("announced: %v", got)
	}
	if st, _ := m.Get(h.ID); !slices.ContainsFunc(st.Problems, func(p string) bool { return strings.Contains(p, "5432/tcp is held by nc") }) {
		t.Fatalf("its page says so: %v", st.Problems)
	}

	// The new program is trusted: relocked, the port is its again, and that is said.
	if v, err = m.RelockPort(ctx, h.ID, lock); err != nil || v.Firewall.Locks[0].Exe != "/usr/bin/nc" || v.Firewall.Locks[0].State != firewall.LockHeld {
		t.Fatalf("relock: %+v %v", v.Firewall.Locks, err)
	}
	s.Sample(ctx)
	m.PollAll(ctx)
	if got := lockAlerts(sent.after(2)); len(got) != 2 || got[1] != "ok: Locked port back: box" {
		t.Fatalf("back: %v", got)
	}
	if v, err = m.UnlockPort(ctx, h.ID, lock); err != nil || len(v.Firewall.Locks) != 0 {
		t.Fatalf("unlock: %+v %v", v.Firewall.Locks, err)
	}
	if _, err := m.UnlockPort(ctx, h.ID, lock); !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("unlock twice: %v", err)
	}
}

// lockAlerts are the alerts about locked ports, leaving out what the machine running the test says of
// itself (its drives).
func lockAlerts(got []string) []string {
	return slices.DeleteFunc(got, func(a string) bool { return !strings.Contains(a, "Locked port") })
}
