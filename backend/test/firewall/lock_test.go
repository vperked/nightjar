package firewall_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/firewall"
)

// service puts a process in a fake /proc running exe in a systemd service (none when unit is empty),
// holding the sockets given.
func service(t *testing.T, dir string, pid int, comm, exe, unit string, sockets ...int) {
	t.Helper()
	child(t, dir, pid, 1, comm, exe, unit, sockets...)
}

// child is service for a process another one (ppid) started.
func child(t *testing.T, dir string, pid, ppid int, comm, exe, unit string, sockets ...int) {
	t.Helper()
	runAs(t, dir, pid, comm, comm, exe, 0, ppid, sockets...)
	cg := "0::/user.slice/user-1000.slice/session-3.scope\n"
	if unit != "" {
		cg = "0::/system.slice/" + unit + "\n"
	}
	os.WriteFile(filepath.Join(dir, fmt.Sprint(pid), "cgroup"), []byte(cg), 0o644)
}

// listen sets what listens on tcp in a fake /proc: "port=inode" for each on every address,
// "lo:port=inode" on loopback alone and "lan:port=inode" on 10.0.0.5, plus the SSH listener and login
// proc puts there.
func listen(t *testing.T, dir string, socks ...string) {
	t.Helper()
	lines := []string{fmt.Sprintf("00000000:%04X 00000000:0000 0A 1", sshPort()), fmt.Sprintf("1100A8C0:%04X C86433C6:C350 01 4242", sshPort())}
	for _, s := range socks {
		addr := "00000000"
		if a, rest, ok := strings.Cut(s, ":"); ok {
			addr, s = map[string]string{"lo": "0100007F", "lan": "0500000A"}[a], rest
		}
		var port, inode int
		fmt.Sscanf(s, "%d=%d", &port, &inode)
		lines = append(lines, fmt.Sprintf("%s:%04X 00000000:0000 0A %d", addr, port, inode))
	}
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcpTable(lines...)), 0o644)
}

func gone(dir string, pid int) { os.RemoveAll(filepath.Join(dir, fmt.Sprint(pid))) }

type heard struct {
	mu     sync.Mutex
	states []firewall.LockState
}

func (h *heard) on(s firewall.LockState) { h.mu.Lock(); h.states = append(h.states, s); h.mu.Unlock() }
func (h *heard) last() firewall.LockState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.states) == 0 {
		return firewall.LockState{}
	}
	return h.states[len(h.states)-1]
}
func (h *heard) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.states) }

func TestListeningPortsNameTheirServiceAndExecutable(t *testing.T) {
	dir := proc(t)
	listen(t, dir, "5432=700", "8080=701")
	service(t, dir, 300, "postgres", "/usr/lib/postgresql/16/bin/postgres", "postgresql@16-main.service", 700)
	service(t, dir, 301, "python3", "/usr/bin/python3.12 (deleted)", "", 701)
	var pg, py firewall.Port
	for _, p := range firewall.Listening(dir) {
		switch p.Port {
		case 5432:
			pg = p
		case 8080:
			py = p
		}
	}
	if pg.Program != "postgres" || pg.Exe != "/usr/lib/postgresql/16/bin/postgres" || pg.Unit != "postgresql@16-main.service" || pg.Name() != "postgresql@16-main" {
		t.Errorf("postgres: %+v", pg)
	}
	// A scope is no service, and a program upgraded under itself is still the same program.
	if py.Program != "python3" || py.Exe != "/usr/bin/python3.12" || py.Unit != "" || py.Name() != "python3" {
		t.Errorf("python: %+v", py)
	}
	// The JSON a dashboard reads keeps "program" where it was.
	b, _ := json.Marshal(pg)
	if !strings.Contains(string(b), `"program":"postgres"`) || !strings.Contains(string(b), `"unit":"postgresql@16-main.service"`) {
		t.Errorf("json: %s", b)
	}
}

func TestAPortIsLockedToTheServiceOnIt(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "5432=700", "9000=800", "9000=801")
	service(t, dir, 300, "postgres", "/usr/lib/postgresql/16/bin/postgres", "postgresql.service", 700)
	service(t, dir, 400, "node", "/usr/bin/node", "app.service", 800)
	service(t, dir, 401, "python3", "/usr/bin/python3", "", 801)

	if _, err := m.Lock(ctx, 6000, "tcp"); err == nil || !strings.Contains(err.Error(), "nothing listens") {
		t.Errorf("nothing there: %v", err)
	}
	if _, err := m.Lock(ctx, 9000, "tcp"); err == nil || !strings.Contains(err.Error(), "more than one program") {
		t.Errorf("two there: %v", err)
	}
	if _, err := m.Lock(ctx, 5432, "sctp"); err == nil {
		t.Error("sctp locked")
	}
	l, err := m.Lock(ctx, 5432, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Proto != "tcp" || l.State != firewall.LockHeld || l.Unit != "postgresql.service" || l.Exe != "/usr/lib/postgresql/16/bin/postgres" {
		t.Errorf("lock: %+v", l)
	}
	if _, err := m.Lock(ctx, 5432, "tcp"); err == nil || !strings.Contains(err.Error(), "locked already") {
		t.Errorf("twice: %v", err)
	}
	// Locking puts nothing in the firewall while the service holds its port.
	if strings.Contains(f.v4, "5432") {
		t.Errorf("held port dropped:\n%s", f.v4)
	}
	st := m.Status(ctx)
	if len(st.Locks) != 1 || st.Locks[0].ID != l.ID {
		t.Errorf("status: %+v", st.Locks)
	}
	// The lock is kept, and comes back with the rules.
	m2 := newManager(t, f, &saved)
	if locks := m2.Status(ctx).Locks; len(locks) != 1 || locks[0].Port != 5432 || locks[0].Unit != "postgresql.service" {
		t.Errorf("restored: %+v", locks)
	}
}

func TestATakenPortIsDroppedUntilItsServiceHasItBack(t *testing.T) {
	f := &fake{}
	var saved []byte
	h := &heard{}
	m := newManager(t, f, &saved)
	m.OnLock = h.on
	dir := m.Proc
	ctx := context.Background()
	m.Status(ctx) // started
	m.Add(ctx, firewall.Rule{Action: "allow", Port: "5432", Proto: "tcp", Note: "db"}, nil)
	listen(t, dir, "5432=700")
	service(t, dir, 300, "postgres", "/usr/lib/postgresql/16/bin/postgres", "postgresql.service", 700)
	l, err := m.Lock(ctx, 5432, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	m.CheckLocks(ctx)
	if h.count() != 0 || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("held port: %d heard\n%s", h.count(), f.v4)
	}

	// Postgres stops: nothing listens, and nothing is dropped, so it can come back.
	gone(dir, 300)
	listen(t, dir)
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockIdle || h.count() != 0 || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("idle: %+v, %d heard\n%s", s, h.count(), f.v4)
	}

	// Something else takes the port: it is dropped ahead of the rule that allows it, and said.
	listen(t, dir, "5432=900")
	service(t, dir, 500, "nc", "/usr/bin/nc.openbsd", "", 900)
	m.CheckLocks(ctx)
	s := m.Locks()[0]
	if s.State != firewall.LockTaken || s.Holder == nil || s.Holder.Exe != "/usr/bin/nc.openbsd" || s.Since == 0 || s.WatchOnly {
		t.Fatalf("taken: %+v", s)
	}
	if h.count() != 1 || h.last().State != firewall.LockTaken || !strings.Contains(h.last().Words(), "held by nc (/usr/bin/nc.openbsd), not postgresql") {
		t.Fatalf("heard: %+v", h.states)
	}
	drop, allow := strings.Index(f.v4, "-p tcp --dport 5432 -m comment --comment nj:"+l.ID+" -j DROP"), strings.Index(f.v4, "-j ACCEPT")
	if drop < 0 || allow < drop || !strings.Contains(f.v4, `--log-prefix "NJ:`) {
		t.Fatalf("not dropped first:\n%s", f.v4)
	}
	if !strings.Contains(f.v6, "nj:"+l.ID) {
		t.Errorf("IPv6 not dropped:\n%s", f.v6)
	}
	// It is said once, however many checks find it so.
	for range 8 {
		m.CheckLocks(ctx)
	}
	if h.count() != 1 {
		t.Errorf("said %d times", h.count())
	}
	// Its drops are named after the lock.
	f.journal = "1700000000.000 host kernel: NJ:" + l.ID[len(l.ID)-10:] + " IN=eth0 OUT= SRC=203.0.113.7 DST=10.0.0.2 PROTO=TCP SPT=5000 DPT=5432\n"
	if d := m.Status(ctx).Drops; len(d) != 1 || d[0].Rule != l.ID {
		t.Errorf("drops: %+v", d)
	}

	// Postgres is back: the drop is lifted, and that is said too.
	gone(dir, 500)
	listen(t, dir, "5432=1000")
	service(t, dir, 301, "postgres", "/usr/lib/postgresql/16/bin/postgres", "postgresql.service", 1000)
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockHeld || s.Holder != nil || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("back: %+v\n%s", s, f.v4)
	}
	if h.count() != 2 || h.last().State != firewall.LockHeld {
		t.Errorf("heard: %+v", h.states)
	}
}

func TestTheSameExecutableInAnotherServiceTakesThePort(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "8000=700")
	service(t, dir, 300, "python3", "/usr/bin/python3", "app.service", 700)
	l, _ := m.Lock(ctx, 8000, "tcp")
	gone(dir, 300)
	listen(t, dir, "8000=701")
	service(t, dir, 301, "python3", "/usr/bin/python3", "", 701)
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockTaken || !strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("same program outside its service: %+v", s)
	}
}

func TestASocketHandedToAnotherProgramIsFoundTakenToo(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "8000=700")
	service(t, dir, 300, "app", "/opt/app/bin/app", "app.service", 700)
	m.Lock(ctx, 8000, "tcp")
	m.CheckLocks(ctx)
	// The same socket, now held by a shell outside the service: one it ran over itself, moved off.
	gone(dir, 300)
	service(t, dir, 300, "sh", "/usr/bin/dash", "", 700)
	for range 6 {
		m.CheckLocks(ctx)
	}
	if s := m.Locks()[0]; s.State != firewall.LockTaken {
		t.Fatalf("not found taken: %+v", s)
	}
}

func TestPortsKeptOpenAreOnlyWatched(t *testing.T) {
	f := &fake{}
	var saved []byte
	h := &heard{}
	m := newManager(t, f, &saved)
	m.OnLock = h.on
	dir := m.Proc
	ctx := context.Background()
	ssh := int(sshPort())
	listen(t, dir)
	service(t, dir, 200, "sshd", "/usr/sbin/sshd", "ssh.service", 1)
	l, err := m.Lock(ctx, uint16(ssh), "tcp")
	if err != nil || !l.WatchOnly {
		t.Fatalf("%+v %v", l, err)
	}
	gone(dir, 200)
	listen(t, dir) // the listener's inode 1 stays; now held by another program
	service(t, dir, 201, "dropbear", "/usr/sbin/dropbear", "", 1)
	for range 6 {
		m.CheckLocks(ctx)
	}
	if s := m.Locks()[0]; s.State != firewall.LockTaken || !s.WatchOnly || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("SSH dropped: %+v\n%s", s, f.v4)
	}
	if h.count() != 1 || !h.last().WatchOnly {
		t.Errorf("heard: %+v", h.states)
	}
	// The dashboard's port is kept open too: a mistake there would cut the dashboard off.
	listen(t, dir, "8080=900")
	service(t, dir, 300, "nightjar", "/usr/local/bin/nightjar", "nightjar.service", 900)
	d, err := m.Lock(ctx, 8080, "tcp")
	if err != nil || !d.WatchOnly {
		t.Fatalf("%+v %v", d, err)
	}
	gone(dir, 300)
	listen(t, dir, "8080=901")
	service(t, dir, 301, "nc", "/usr/bin/nc", "", 901)
	m.CheckLocks(ctx)
	if s := m.Locks()[1]; s.State != firewall.LockTaken || !s.WatchOnly || strings.Contains(f.v4, "nj:"+d.ID) {
		t.Fatalf("dashboard's port dropped: %+v\n%s", s, f.v4)
	}
}

func TestRelockingTrustsTheNewProgramAndUnlockingLetsGo(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "8000=700")
	service(t, dir, 300, "java", "/usr/lib/jvm/java-17/bin/java", "app.service", 700)
	l, _ := m.Lock(ctx, 8000, "tcp")
	// The app moved to a service of its own, rightly.
	gone(dir, 300)
	listen(t, dir, "8000=701")
	service(t, dir, 301, "java", "/usr/lib/jvm/java-21/bin/java", "app-v2.service", 701)
	m.CheckLocks(ctx)
	if !strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatal("not dropped")
	}
	s, err := m.Relock(ctx, l.ID)
	if err != nil || s.State != firewall.LockHeld || s.Unit != "app-v2.service" || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("relock: %+v %v\n%s", s, err, f.v4)
	}
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockHeld {
		t.Fatalf("after relock: %+v", s)
	}
	// Taken again, then unlocked: the drop goes with the lock.
	gone(dir, 301)
	listen(t, dir, "8000=702")
	service(t, dir, 302, "nc", "/usr/bin/nc", "", 702)
	m.CheckLocks(ctx)
	if !strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatal("not dropped")
	}
	if err := m.Unlock(ctx, l.ID); err != nil || strings.Contains(f.v4, "nj:"+l.ID) || len(m.Locks()) != 0 {
		t.Fatalf("unlock: %v\n%s", err, f.v4)
	}
	if err := m.Unlock(ctx, l.ID); err != firewall.ErrNotFound {
		t.Errorf("unlock twice: %v", err)
	}
	if _, err := m.Relock(ctx, l.ID); err != firewall.ErrNotFound {
		t.Errorf("relock gone: %v", err)
	}
}

func TestSystemdHoldingASocketNeitherHoldsNorTakesIt(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "631=700")
	service(t, dir, 1, "systemd", "/usr/lib/systemd/systemd", "", 700)
	if _, err := m.Lock(ctx, 631, "tcp"); err == nil || !strings.Contains(err.Error(), "starts when needed") {
		t.Fatalf("socket activated: %v", err)
	}
	service(t, dir, 300, "cupsd", "/usr/sbin/cupsd", "cups.service", 700)
	if _, err := m.Lock(ctx, 631, "tcp"); err != nil {
		t.Fatal(err)
	}
	gone(dir, 300)
	for range 6 {
		m.CheckLocks(ctx)
	}
	if s := m.Locks()[0]; s.State != firewall.LockIdle {
		t.Fatalf("systemd alone: %+v", s)
	}
}

func TestTakenPortDropsAreValidIPTables(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("iptables-restore --test needs root")
	}
	taken := []firewall.Lock{{ID: "lmmwg3mj6uik", Port: 5432, Proto: "tcp"}, {ID: "l2", Port: 53, Proto: "udp"}}
	for cmd, six := range map[string]bool{"iptables-restore": false, "ip6tables-restore": true} {
		if _, err := exec.LookPath(cmd); err != nil {
			t.Skip("needs " + cmd)
		}
		rules := firewall.Build(nil, "", nil, six, taken...)
		c := exec.Command(cmd, "--test", "--noflush")
		c.Stdin = strings.NewReader(rules)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s\n%s", cmd, err, out, rules)
		}
	}
}

func TestServersThatHandTheirSocketToHelpersKeepTheirPort(t *testing.T) {
	for _, unit := range []string{"postfix@-.service", ""} {
		t.Run("unit="+unit, func(t *testing.T) {
			f := &fake{}
			var saved []byte
			h := &heard{}
			m := newManager(t, f, &saved)
			m.OnLock = h.on
			dir := m.Proc
			ctx := context.Background()
			// Postfix's master holds :25, and hands it to smtpd as mail comes in.
			listen(t, dir, "25=700")
			service(t, dir, 300, "master", "/usr/lib/postfix/sbin/master", unit, 700)
			child(t, dir, 301, 300, "smtpd", "/usr/lib/postfix/sbin/smtpd", unit, 700)
			l, err := m.Lock(ctx, 25, "tcp")
			if err != nil || l.Exe != "/usr/lib/postfix/sbin/master" {
				t.Fatalf("lock while smtpd runs: %+v %v", l, err)
			}
			for range 6 {
				m.CheckLocks(ctx)
			}
			if s := m.Locks()[0]; s.State != firewall.LockHeld || h.count() != 0 || strings.Contains(f.v4, "nj:"+l.ID) {
				t.Fatalf("helper read as a takeover: %+v %+v\n%s", s, h.states, f.v4)
			}
			// A program outside the service holding the same socket still takes it.
			service(t, dir, 400, "nc", "/usr/bin/nc", "", 700)
			for range 6 {
				m.CheckLocks(ctx)
			}
			if s := m.Locks()[0]; s.State != firewall.LockTaken || s.Holder.Program != "nc" {
				t.Fatalf("outsider: %+v", s)
			}
		})
	}
}

func TestALoopbackListenerBesideTheServiceTakesNothing(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	// Postgres on the LAN address, and a port-forward on loopback beside it.
	listen(t, dir, "lan:5432=700", "lo:5432=701")
	service(t, dir, 300, "postgres", "/usr/lib/postgresql/16/bin/postgres", "postgresql.service", 700)
	service(t, dir, 400, "ssh", "/usr/bin/ssh", "", 701)
	l, err := m.Lock(ctx, 5432, "tcp")
	if err != nil || l.Unit != "postgresql.service" {
		t.Fatalf("lock beside a forward: %+v %v", l, err)
	}
	for range 6 {
		m.CheckLocks(ctx)
	}
	if s := m.Locks()[0]; s.State != firewall.LockHeld || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("loopback read as a takeover: %+v\n%s", s, f.v4)
	}
	// On every address, the network reaches it: a takeover.
	listen(t, dir, "lan:5432=700", "5432=702")
	service(t, dir, 401, "nc", "/usr/bin/nc", "", 702)
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockTaken || s.Holder.Program != "nc" {
		t.Fatalf("reachable outsider: %+v", s)
	}
}

func TestAnUpgradeWithinTheServiceKeepsThePort(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "8443=700")
	service(t, dir, 300, "app", "/snap/app/120/bin/app", "snap.app.daemon.service", 700)
	l, _ := m.Lock(ctx, 8443, "tcp")
	// snapd refreshed it: a new revision, so a new path, in the same unit.
	gone(dir, 300)
	listen(t, dir, "8443=701")
	service(t, dir, 301, "app", "/snap/app/121/bin/app", "snap.app.daemon.service", 701)
	m.CheckLocks(ctx)
	if s := m.Locks()[0]; s.State != firewall.LockHeld || strings.Contains(f.v4, "nj:"+l.ID) {
		t.Fatalf("upgrade read as a takeover: %+v", s)
	}
}

func TestAProcessExitingTakesNothing(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	dir := m.Proc
	ctx := context.Background()
	listen(t, dir, "8000=700")
	service(t, dir, 300, "app", "/opt/app/bin/app", "", 700)
	m.Lock(ctx, 8000, "tcp")
	// Caught exiting: its executable is gone, its sockets not yet.
	os.Remove(filepath.Join(dir, "300", "exe"))
	for range 6 {
		m.CheckLocks(ctx)
	}
	if s := m.Locks()[0]; s.State == firewall.LockTaken {
		t.Fatalf("exiting read as a takeover: %+v", s)
	}
}
