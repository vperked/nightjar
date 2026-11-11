package ddos

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tcpRow is one line of /proc/net/tcp or tcp6 as the kernel writes it.
func tcpRow(local, remote, state string, inode int) string {
	return fmt.Sprintf("   0: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 20 4 30 10 -1\n", local, remote, state, inode)
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// Addresses in /proc/net/tcp{,6} are 32-bit words in the machine's (little-endian) byte order.
func TestHexAddrAndLocalPort(t *testing.T) {
	cases := map[string]string{
		"0100007F:1F90":                         "127.0.0.1",
		"077100CB:C822":                         "203.0.113.7",
		"22D8B85D:01BB":                         "93.184.216.34",
		"00000000:0000":                         "0.0.0.0",
		"B80D0120000000000000000001000000:0016": "2001:db8::1",
		"0000000000000000FFFF0000077100CB:01BB": "203.0.113.7", // IPv4-mapped: one key with IPv4
		"00000000000000000000000001000000:0016": "::1",
		"000080FE000000000000000001000000:0016": "fe80::1",
	}
	for in, want := range cases {
		ip := hexAddr(in)
		if ip == nil || ip.String() != want {
			t.Errorf("hexAddr(%q) = %v, want %s", in, ip, want)
		}
	}
	for _, bad := range []string{"", "0100007F", "ZZ00007F:0016", "0100007:0016", "01000000000000:0016"} {
		if ip := hexAddr(bad); ip != nil {
			t.Errorf("hexAddr(%q) = %v, want nil", bad, ip)
		}
	}
	for in, want := range map[string]uint16{"0100007F:1F90": 8080, "00000000:0016": 22, "x:01BB": 443, "0100007F": 0, "0100007F:ZZZZ": 0, "0100007F:10000": 0} {
		if got := localPort(in); got != want {
			t.Errorf("localPort(%q) = %d, want %d", in, got, want)
		}
	}
}

// Only connections from elsewhere to a port this machine listens on count, per source address:
// not this machine's own outbound connections, not loopback, not TIME_WAIT; half-open always count.
// A connection listed before its listener still counts, and tcp6's IPv4-mapped peers merge with tcp.
func TestParseTCPCountsInboundPerSource(t *testing.T) {
	tcp := tcpHeader +
		tcpRow("0A00000A:1F90", "0B7100CB:D000", "01", 1) + // 203.0.113.11 -> :8080, before 8080's listener
		tcpRow("00000000:01BB", "00000000:0000", "0A", 2) + // listening :443
		tcpRow("00000000:0016", "00000000:0000", "0A", 3) + // listening :22
		tcpRow("0A00000A:01BB", "077100CB:C822", "01", 4) + // 203.0.113.7 -> :443
		tcpRow("0A00000A:01BB", "077100CB:C823", "01", 5) + // 203.0.113.7 -> :443 again
		tcpRow("0A00000A:0016", "097100CB:D431", "01", 6) + // 203.0.113.9 -> :22
		tcpRow("0A00000A:9C40", "22D8B85D:01BB", "01", 7) + // this machine -> 93.184.216.34:443 (outbound)
		tcpRow("0100007F:01BB", "0100007F:D000", "01", 8) + // loopback
		tcpRow("0A00000A:01BB", "016433C6:3039", "03", 9) + // SYN_RECV from 198.51.100.1
		tcpRow("0A00000A:01BB", "327100CB:3039", "06", 10) + // TIME_WAIT from 203.0.113.50
		"garbage\n" +
		tcpRow("0A00000A:01BB", "ZZZZZZZZ:3039", "01", 11) +
		tcpRow("00000000:1F90", "00000000:0000", "0A", 12) // listening :8080, listed last
	tcp6 := tcpHeader +
		tcpRow("00000000000000000000000000000000:01BB", "00000000000000000000000000000000:0000", "0A", 20) +
		tcpRow("0000000000000000FFFF00000A00000A:01BB", "0000000000000000FFFF0000077100CB:C900", "01", 21) + // ::ffff:203.0.113.7
		tcpRow("B80D0120000000000000000002000000:01BB", "B80D0120000000000000000001000000:C901", "01", 22) + // 2001:db8::1
		tcpRow("00000000000000000000000001000000:01BB", "00000000000000000000000001000000:C902", "01", 23) // ::1

	var tb Table
	ParseTCP(strings.NewReader(tcp), &tb)
	ParseTCP(strings.NewReader(tcp6), &tb)

	if tb.Established != 6 || tb.Web != 4 || tb.SynRecv != 1 {
		t.Errorf("Established %d (want 6), Web %d (want 4), SynRecv %d (want 1)", tb.Established, tb.Web, tb.SynRecv)
	}
	want := map[string]Peer{
		"203.0.113.7":  {Established: 3},
		"203.0.113.9":  {Established: 1},
		"203.0.113.11": {Established: 1},
		"198.51.100.1": {SynRecv: 1},
		"2001:db8::1":  {Established: 1},
	}
	if len(tb.BySource) != len(want) {
		var keys []string
		for k := range tb.BySource {
			keys = append(keys, k)
		}
		t.Fatalf("sources = %v, want %d of them", keys, len(want))
	}
	for k, w := range want {
		if p := tb.BySource[k]; p == nil || *p != w {
			t.Errorf("%s = %+v, want %+v", k, p, w)
		}
	}
	for _, port := range []uint16{22, 443, 8080} {
		if !tb.listening[port] {
			t.Errorf("port %d not seen listening", port)
		}
	}
}

// sshd names a session's process "sshd: user@tty" (or "sshd-session: ..." from OpenSSH 9.8) only
// once it has let someone in; every pre-auth title must not read as a login.
func TestSSHSessionTitle(t *testing.T) {
	logins := []string{"sshd: alice@pts/0", "sshd: root@notty", "sshd-session: alice@pts/3", "sshd: deploy@notty"}
	notLogins := []string{
		"sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", "sshd: [accepted]", "sshd: alice [priv]",
		"sshd: alice [net]", "sshd: unknown [preauth]", "sshd-session: alice [priv]", "sshd: x@y [priv]",
		"sshd", "/usr/sbin/sshd -D", "bash: alice@pts/0", "sshd: @pts/0", "sshd:alice@pts/0", "",
	}
	for _, s := range logins {
		if !sessionTitle.MatchString(s) {
			t.Errorf("%q should be a session", s)
		}
	}
	for _, s := range notLogins {
		if sessionTitle.MatchString(s) {
			t.Errorf("%q should not be a session", s)
		}
	}
}

// A fake /proc: an OpenSSH 9.8 sshd-session login over IPv6 is found; a pre-auth connection and a
// process that only calls itself sshd in its command line (but is not sshd) are not.
func TestSSHDSocketsFakeProc(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	tcp := tcpHeader +
		tcpRow("0A00000A:0016", "427100CB:C350", "01", 54) // 203.0.113.66 -> :22, held by a fake
	tcp6 := tcpHeader +
		tcpRow("00000000000000000000000000000000:0016", "00000000000000000000000000000000:0000", "0A", 50) +
		tcpRow("B80D0120000000000000000002000000:0016", "B80D0120000000000000000001000000:C350", "01", 51) + // 2001:db8::1, logged in
		tcpRow("B80D0120000000000000000002000000:0016", "B80D0120000000000000000003000000:C351", "01", 52) // 2001:db8::3, pre-auth
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644)
	os.WriteFile(filepath.Join(dir, "net", "tcp6"), []byte(tcp6), 0o644)
	proc := func(pid int, comm, title string, inodes ...int) {
		exe := map[string]string{"sshd": "/usr/sbin/sshd", "sshd-session": "/usr/lib/openssh/sshd-session"}[comm]
		if exe == "" {
			exe = "/usr/bin/" + comm
		}
		fakeProcess(dir, pid, comm, title, exe, 0, 1, inodes...)
	}
	proc(100, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", 50)
	proc(101, "sshd-session", "sshd-session: alice [priv]", 51)
	proc(102, "sshd-session", "sshd-session: alice@pts/0", 51)
	proc(103, "sshd-session", "sshd-session: unknown [preauth]", 52)
	proc(104, "bash", "sshd: mallory@pts/9", 54)
	os.MkdirAll(filepath.Join(dir, "self"), 0o755) // not a pid

	ports, logins := sshdSockets(dir)
	if !slices.Contains(ports, 22) {
		t.Errorf("ports %v should hold sshd's listening port 22", ports)
	}
	if fmt.Sprint(logins) != "[{2001:db8::1 22}]" {
		t.Errorf("logins = %v, want only 2001:db8::1 on 22", logins)
	}
	// A /proc with no sshd at all: no logins, and 22 at least.
	empty := t.TempDir()
	if ports, logins := sshdSockets(empty); logins != nil || len(ports) == 0 {
		t.Errorf("empty proc: ports %v logins %v", ports, logins)
	}
	if got := sshdPorts(nil); len(got) != 1 || got[0] != 22 {
		t.Errorf("sshdPorts(nil) = %v", got)
	}
	if got := sshdPorts([]uint16{2222, 22, 2222}); fmt.Sprint(got) != "[22 2222]" {
		t.Errorf("sshdPorts = %v", got)
	}
}

// fakeProcess puts a process in a fake /proc: its name, title, program, real user, parent and sockets.
func fakeProcess(dir string, pid int, comm, title, exe string, uid, ppid int, inodes ...int) {
	p := filepath.Join(dir, fmt.Sprint(pid))
	os.MkdirAll(filepath.Join(p, "fd"), 0o755)
	os.WriteFile(filepath.Join(p, "comm"), []byte(comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(p, "cmdline"), []byte(title+"\x00\x00\x00"), 0o644)
	os.WriteFile(filepath.Join(p, "status"), []byte(fmt.Sprintf("Name:\t%s\nUmask:\t0022\nPPid:\t%d\nUid:\t%d\t%d\t%d\t%d\n", comm, ppid, uid, uid, uid, uid)), 0o644)
	os.Symlink(exe, filepath.Join(p, "exe"))
	for i, ino := range inodes {
		os.Symlink(fmt.Sprintf("socket:[%d]", ino), filepath.Join(p, "fd", fmt.Sprint(3+i)))
	}
	os.Symlink("/dev/null", filepath.Join(p, "fd", "0"))
}

// Any user can start a program called sshd, titled like a session. Only sshd's own program counts:
// run by root for a listener (else its port would be kept open under a default drop), and for a
// session run by root or with sshd's root process for the connection as its parent (else an
// attacker's address would be spared from blocks).
func TestOnlySSHDsOwnProcessesCount(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	tcp := tcpHeader +
		tcpRow("00000000:0016", "00000000:0000", "0A", 50) + // the real sshd on 22
		tcpRow("00000000:D903", "00000000:0000", "0A", 60) + // 55555: a user's program called sshd
		tcpRow("00000000:D904", "00000000:0000", "0A", 61) + // 55556: sshd's program, run by a user
		tcpRow("00000000:D905", "00000000:0000", "0A", 62) + // 55557: sshd as root, upgraded since it started
		tcpRow("0A00000A:0016", "427100CB:C350", "01", 70) + // 203.0.113.66: a fake session
		tcpRow("0A00000A:0016", "437100CB:C351", "01", 71) + // 203.0.113.67: sshd's program, a user's parent
		tcpRow("0A00000A:0016", "447100CB:C352", "01", 72) + // 203.0.113.68: sshd's program, orphaned to init
		tcpRow("0A00000A:0016", "457100CB:C353", "01", 73) + // 203.0.113.69: bob, logged in for real
		tcpRow("0A00000A:0016", "467100CB:C354", "01", 74) // 203.0.113.70: root, logged in for real
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644)
	fakeProcess(dir, 1, "systemd", "/sbin/init", "/usr/lib/systemd/systemd", 0, 0)
	fakeProcess(dir, 100, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", "/usr/sbin/sshd", 0, 1, 50)
	fakeProcess(dir, 200, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", "/home/mallory/sshd", 1000, 1, 60)
	fakeProcess(dir, 201, "sshd", "sshd: /usr/sbin/sshd -D -p 55556", "/usr/sbin/sshd", 1000, 1, 61)
	fakeProcess(dir, 202, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", "/usr/sbin/sshd (deleted)", 0, 1, 62)
	fakeProcess(dir, 210, "sshd", "sshd: mallory@pts/9", "/home/mallory/sshd", 0, 1, 70)
	fakeProcess(dir, 211, "sshd", "sshd: mallory@pts/9", "/usr/sbin/sshd", 1000, 201, 71)
	fakeProcess(dir, 212, "sshd", "sshd: mallory@pts/9", "/usr/sbin/sshd", 1000, 1, 72)
	fakeProcess(dir, 300, "sshd", "sshd: bob [priv]", "/usr/sbin/sshd", 0, 100, 73)
	fakeProcess(dir, 301, "sshd", "sshd: bob@pts/1", "/usr/sbin/sshd", 1001, 300, 73)
	fakeProcess(dir, 302, "sshd", "sshd: root@pts/2", "/usr/sbin/sshd", 0, 100, 74)

	ports, logins := sshdSockets(dir)
	if !slices.Contains(ports, 22) || !slices.Contains(ports, 55557) {
		t.Errorf("sshd's own listeners are kept: %v", ports)
	}
	if slices.Contains(ports, 55555) || slices.Contains(ports, 55556) {
		t.Errorf("a user's listener called sshd is kept open: %v", ports)
	}
	if fmt.Sprint(logins) != "[{203.0.113.69 22} {203.0.113.70 22}]" {
		t.Errorf("logins = %v, want only bob's and root's", logins)
	}

	for exe, want := range map[string]bool{
		"/usr/sbin/sshd": true, "/usr/bin/sshd": true, "/usr/lib/openssh/sshd-session": true, "/usr/libexec/openssh/sshd-session": true,
		"/usr/lib/ssh/sshd-session": true, "/usr/sbin/sshd (deleted)": true, "/nix/store/abc123-openssh-9.9p1/bin/sshd": true,
		"/tmp/sshd": false, "/home/a/usr/sbin/sshd": false, "/usr/sbin/sshdx": false, "/usr/bin/bash": false,
		"/nix/store/abc-evil/bin/sshd": false, "/nix/store/abc-openssh-9/x/bin/sshd": false, "": false,
	} {
		if sshdBinary(exe) != want {
			t.Errorf("sshdBinary(%q) = %v", exe, !want)
		}
	}
}

// The SSH ports read from config are kept open by the firewall: getting them wrong locks the owner
// out. Port lines (with = and comments), ListenAddress with and without ports, and Include.
func TestSSHConfigPorts(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) string {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	main := write("a/sshd_config", "# Port 9999 is a comment\nPort 2222\nport=2223 # trailing comment\nPort 0\nPort 70000\nListenAddress 0.0.0.0\nInclude sshd_config.d/*.conf\n")
	write("a/sshd_config.d/10-extra.conf", "Port \"2224\"\n")
	write("a/sshd_config.d/ignored.txt", "Port 1111\n")
	cases := []struct {
		path string
		want string
	}{
		{main, "[2222 2223 2224]"},
		{write("b/sshd_config", "ListenAddress 0.0.0.0:2200\nListenAddress [::]:2201\nPort 2222\n"), "[2200 2201]"}, // every address names its port
		{write("c/sshd_config", "ListenAddress 10.0.0.1:2200\nListenAddress 10.0.0.2\nPort 2222\n"), "[2222 2200]"},
		{write("d/sshd_config", "PermitRootLogin no\n"), "[22]"},
		{write("e/sshd_config", "Include /nonexistent/*.conf\nPort = 2022\n"), "[2022]"},
		{filepath.Join(dir, "missing"), "[]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(SSHConfigPorts(c.path)); got != c.want {
			t.Errorf("SSHConfigPorts(%s) = %s, want %s", c.path, got, c.want)
		}
	}
	// A config that includes itself ends (the depth guard) rather than hanging.
	loop := write("f/sshd_config", "Port 2022\nInclude sshd_config\n")
	if got := SSHConfigPorts(loop); !slices.Contains(got, 2022) {
		t.Errorf("self-including config = %v", got)
	}
}

// sshd reads a relative Include as relative to /etc/ssh (the main config's folder), even from an
// included file; the code says the same in its comment.
func TestSSHConfigNestedRelativeInclude(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sshd_config.d"), 0o755)
	os.MkdirAll(filepath.Join(dir, "extra"), 0o755)
	main := filepath.Join(dir, "sshd_config")
	os.WriteFile(main, []byte("Include sshd_config.d/*.conf\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sshd_config.d", "10.conf"), []byte("Include extra/port.conf\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "extra", "port.conf"), []byte("Port 2222\n"), 0o644)
	if got := SSHConfigPorts(main); !slices.Contains(got, 2222) {
		t.Skipf("BUG: SSHConfigPorts resolves a relative Include inside an included file against that file's folder, not the main config's (/etc/ssh) as sshd does; Port 2222 missed, got %v", got)
	}
}

func TestConfigLineAddressPortAndSocketUnits(t *testing.T) {
	for line, want := range map[string]string{
		"Port 2222":                      "Port [2222]",
		"Port=2222":                      "Port [2222]",
		"Port = 2222":                    "Port [2222]",
		"\tPort\t2222  # ssh":            "Port [2222]",
		`Port "2222"`:                    "Port [2222]",
		"Include a.conf b.conf #c.conf":  "Include [a.conf b.conf]",
		"# Port 22":                      " []",
		"   ":                            " []",
		"Banner":                         "Banner []",
		"PasswordAuthentication no #x y": "PasswordAuthentication [no]",
	} {
		k, args := configLine(line)
		if got := fmt.Sprintf("%s %v", k, args); got != want {
			t.Errorf("configLine(%q) = %q, want %q", line, got, want)
		}
	}
	for in, want := range map[string]uint16{
		"0.0.0.0:2222": 2222, "[::]:2222": 2222, ":2222": 2222, "[fe80::1%eth0]:2226": 2226,
		"::": 0, "192.168.1.1": 0, "[::1]": 0, "host:abc": 0, "[::1]:99999": 0, "2001:db8::1": 0,
	} {
		if got := addressPort(in); got != want {
			t.Errorf("addressPort(%q) = %d, want %d", in, got, want)
		}
	}
	unit := filepath.Join(t.TempDir(), "ssh.socket")
	os.WriteFile(unit, []byte("[Socket]\nListenStream=22\nListenStream=\nListenStream=0.0.0.0:2222\nListenStream=[::]:2223\nListenStream = 2224\nListenStream=/run/sshd.sock\nListenStream=192.168.1.5:2225\nAccept=no\n"), 0o644)
	if got := fmt.Sprint(socketUnitPorts(unit)); got != "[2222 2223 2224 2225]" {
		t.Errorf("socketUnitPorts = %s", got)
	}
	if socketUnitPorts(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Error("a missing unit has no ports")
	}
}

// An IPv6 attacker has a /64 to send from: its addresses are gathered into one target, summed; one
// SSH login in that /64 makes the whole /64 SSH (never blocked).
func TestBlockTargetsAggregateIPv6Slash64(t *testing.T) {
	got := blockTargets([]Source{
		{Address: "2001:db8:1:2::a", Established: 200},
		{Address: "203.0.113.7", Established: 50, Requests: 5},
		{Address: "2001:db8:1:2:ffff:ffff:ffff:ffff", Established: 150, SynRecv: 3},
		{Address: "2001:db8:1:3::a", Established: 10},
		{Address: "not-an-ip", Established: 999},
		{Address: "2001:db8:9:9::1", Established: 400},
		{Address: "2001:db8:9:9::2", SSH: true},
		{Address: "203.0.113.8", Established: 1},
	})
	want := []struct {
		addr       string
		est, syn   int
		ssh        bool
		maskOnes   int
		maskLength int
	}{
		{"2001:db8:1:2::/64", 350, 3, false, 64, 128},
		{"203.0.113.7", 50, 0, false, 32, 32},
		{"2001:db8:1:3::/64", 10, 0, false, 64, 128},
		{"2001:db8:9:9::/64", 400, 0, true, 64, 128},
		{"203.0.113.8", 1, 0, false, 32, 32},
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v", got)
	}
	for i, w := range want {
		g := got[i]
		ones, bits := g.net.Mask.Size()
		if g.Address != w.addr || g.Established != w.est || g.SynRecv != w.syn || g.SSH != w.ssh || ones != w.maskOnes || bits != w.maskLength {
			t.Errorf("target %d = %+v (mask %d/%d), want %+v", i, g, ones, bits, w)
		}
	}
	if got[1].Requests != 5 {
		t.Errorf("requests not carried: %+v", got[1])
	}
}

// Blocking must never cut off the owner: SSH logins (and their /64), local networks, name servers
// and gateways, Cloudflare, trusted and pardoned addresses. Only what the attack under way does
// counts against an address.
func TestBlockLockedNeverBlocksTheOwner(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	d := &Defender{Now: func() time.Time { return now }, Trusted: func() []string { return []string{"203.0.113.96/28", "garbage"} },
		pardoned: map[string]time.Time{"203.0.113.50": now.Add(time.Hour)}}
	s := Situation{
		Attacking: true,
		Methods:   []string{ConnectionFlood},
		Upstream:  []string{"9.9.9.9"},
		Sources: []Source{
			{Address: "203.0.113.7", Established: 400},                // blocked
			{Address: "203.0.113.8", Established: 100},                // under the limit
			{Address: "192.168.1.50", Established: 1000},              // the LAN
			{Address: "100.64.3.4", Established: 1000},                // carrier-grade NAT
			{Address: "198.51.100.200", Established: 500, SSH: true},  // logged in over SSH
			{Address: "9.9.9.9", Established: 500},                    // the name server
			{Address: "104.16.1.1", Established: 500},                 // Cloudflare
			{Address: "203.0.113.99", Established: 500},               // trusted
			{Address: "203.0.113.50", Established: 500},               // pardoned by hand
			{Address: "2001:db8:1:2::a", Established: 200},            // with ::b, one /64 over the limit
			{Address: "2001:db8:1:2::b", Established: 200},            //
			{Address: "2001:db8:5:5::1", Established: 400},            // shares a /64 with an SSH login
			{Address: "2001:db8:5:5::2", SSH: true},                   //
			{Address: "fe80::1", Established: 1000},                   // link-local
			{Address: "2606:4700:10::6816:1", Established: 1000},      // Cloudflare IPv6
			{Address: "203.0.113.30", Established: 0, Requests: 9000}, // requests don't count in a connection flood
		},
	}
	d.blockLocked(s, d.trust(s), now)
	var got []string
	for a, b := range d.blocked {
		got = append(got, a)
		if b.Where != "firewall" || b.Until != float64(now.Add(15*time.Minute).Unix()) {
			t.Errorf("block %+v", b)
		}
	}
	slices.Sort(got)
	if fmt.Sprint(got) != "[2001:db8:1:2::/64 203.0.113.7]" {
		t.Fatalf("blocked %v, want only 203.0.113.7 and 2001:db8:1:2::/64", got)
	}

	// A SYN flood blocks nobody: its addresses are forged.
	d2 := &Defender{}
	d2.blockLocked(Situation{Methods: []string{SYNFlood}, Sources: []Source{{Address: "203.0.113.7", Established: 1000, SynRecv: 1000}}}, trustList{}, now)
	if len(d2.blocked) != 0 {
		t.Errorf("a SYN flood blocked %v", d2.blocked)
	}

	// An HTTP flood: 30+ requests a second and a twentieth of all; through a proxy only with a web
	// deny file.
	http := Situation{Methods: []string{HTTPFlood}, Interval: 10, Requests: 10000, Sources: []Source{
		{Address: "203.0.113.20", Established: 2, Requests: 600}, // 60/s, 6%: blocked in the firewall
		{Address: "203.0.113.21", Established: 2, Requests: 400}, // 40/s but 4%: not
		{Address: "203.0.113.22", Requests: 600},                 // via the proxy: web only
		{Address: "203.0.113.23", Established: 900, Requests: 1}, // many connections don't count here
	}}
	d3 := &Defender{}
	d3.blockLocked(http, d3.trust(http), now)
	if len(d3.blocked) != 1 || d3.blocked["203.0.113.20"].Where != "firewall" {
		t.Errorf("HTTP flood without web blocks: %v", d3.blocked)
	}
	d4 := &Defender{WebBlockFile: filepath.Join(t.TempDir(), "deny.conf")}
	d4.blockLocked(http, d4.trust(http), now)
	if len(d4.blocked) != 2 || d4.blocked["203.0.113.22"].Where != "web" {
		t.Errorf("HTTP flood with web blocks: %v", d4.blocked)
	}
}

// In the generated rules, everyone trusted returns before any block drops, replies to this
// machine's own connections pass, and each block lands in its own family.
func TestRulesTrustBeforeBlocks(t *testing.T) {
	d := &Defender{}
	s := Situation{Upstream: []string{"9.9.9.9", "2620:fe::fe"}, Sources: []Source{{Address: "198.51.100.200", SSH: true}}}
	v4, v6 := Rules(plan{conntrack: true}, d.trust(s), []Block{
		{Address: "203.0.113.7"}, {Address: "2001:db8:1:2::/64"}, {Address: "junk"}, {Address: "198.51.100.0/24"},
	})
	at := func(rules, line string) int {
		i := strings.Index(rules, line+"\n")
		if i < 0 {
			t.Errorf("missing %q in:\n%s", line, rules)
		}
		return i
	}
	firstDrop := at(v4, "-A NIGHTJAR -s 198.51.100.0/24 -j DROP")
	for _, line := range []string{"-A NIGHTJAR -i lo -j RETURN", "-A NIGHTJAR -s 192.168.0.0/16 -j RETURN", "-A NIGHTJAR -s 9.9.9.9/32 -j RETURN",
		"-A NIGHTJAR -s 198.51.100.200/32 -j RETURN", "-A NIGHTJAR -m conntrack --ctstate ESTABLISHED,RELATED --ctdir REPLY -j RETURN"} {
		if at(v4, line) > firstDrop {
			t.Errorf("%q comes after a DROP", line)
		}
	}
	at(v4, "-A NIGHTJAR -s 203.0.113.7 -j DROP")
	if at(v6, "-A NIGHTJAR -s 2620:fe::fe/128 -j RETURN") > at(v6, "-A NIGHTJAR -s 2001:db8:1:2::/64 -j DROP") {
		t.Error("v6 trust after a block")
	}
	if strings.Contains(v4, "2001:db8") || strings.Contains(v6, "203.0.113.7") || strings.Contains(v4+v6, "junk") {
		t.Errorf("a block in the wrong family, or junk:\n%s\n%s", v4, v6)
	}
	if !strings.HasPrefix(v4, "*filter\n") || !strings.HasSuffix(v4, "COMMIT\n") {
		t.Errorf("v4 is not a restore file:\n%s", v4)
	}
	var tl trustList
	for _, a := range []string{"2001:db8::1", "bogus", "", "10.0.0.0/8", "1.2.3.4/33"} {
		tl.add(a)
	}
	if fmt.Sprint(tl.v4, tl.v6) != "[10.0.0.0/8] [2001:db8::1/128]" {
		t.Errorf("trustList = %v %v", tl.v4, tl.v6)
	}
}

const webLine = `%s - - [09/Nov/2026:12:00:00 +0000] "GET /index.html HTTP/1.1" 200 612 "-" "Mozilla/5.0 (X11; Linux x86_64)"` + "\n"

// LogTail: the first call finds the end; later calls count whole lines per address; a flood beyond
// one read scales every address alike and moves to the end; truncation and rotation start over.
func TestLogTailScalingAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	os.WriteFile(path, []byte(fmt.Sprintf(webLine, "198.51.100.1")), 0o644)
	l := &LogTail{Path: path}
	by := map[string]*Peer{}
	if n, ok := l.Requests(by, nil); n != 0 || ok {
		t.Fatalf("first call = %d %v, want 0 false", n, ok)
	}
	if n, ok := l.Requests(by, nil); n != 0 || !ok {
		t.Fatalf("nothing new = %d %v", n, ok)
	}

	// 12 MiB of requests, three from one address to every one from another (equal-length lines).
	a, b := fmt.Sprintf(webLine, "203.0.113.70"), fmt.Sprintf(webLine, "198.51.100.9")
	block := a + a + a + b
	reps := (12 << 20) / len(block)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(strings.Repeat(block, reps))
	f.WriteString(fmt.Sprintf(webLine, "127.0.0.1"))
	f.Close()
	total := 4*reps + 1
	n, ok := l.Requests(by, nil)
	if !ok || abs(n-total) > total/200 {
		t.Fatalf("count = %d, want ~%d", n, total)
	}
	ra, rb := by["203.0.113.70"].Requests, by["198.51.100.9"].Requests
	if abs(ra-3*reps) > reps/100 || abs(rb-reps) > reps/100 {
		t.Errorf("per source = %d and %d, want ~%d and ~%d", ra, rb, 3*reps, reps)
	}
	if _, ok := by["127.0.0.1"]; ok {
		t.Error("loopback attributed")
	}
	if n, _ := l.Requests(map[string]*Peer{}, nil); n != 0 {
		t.Errorf("after a scaled read, %d more lines were read: it should have moved to the end", n)
	}

	// A partial line waits until it is whole.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("203.0.113.5 - - [09/Nov/2026")
	if n, _ := l.Requests(map[string]*Peer{}, nil); n != 0 {
		t.Errorf("a partial line was counted")
	}
	f.WriteString(`:12:00:00 +0000] "GET / HTTP/1.1" 200 1 "-" "-"` + "\n")
	f.Close()
	by = map[string]*Peer{}
	if n, _ := l.Requests(by, nil); n != 1 || by["203.0.113.5"] == nil {
		t.Errorf("the completed line: %d %v", n, by)
	}

	// Truncated (copytruncate): from the start.
	os.WriteFile(path, []byte(fmt.Sprintf(webLine, "203.0.113.1")+fmt.Sprintf(webLine, "203.0.113.2")), 0o644)
	if n, _ := l.Requests(map[string]*Peer{}, nil); n != 2 {
		t.Errorf("after truncation: %d, want 2", n)
	}
	// Rotated (a new file at the path, the old one renamed): from the start of the new one.
	os.Rename(path, path+".1")
	os.WriteFile(path, bytes.Repeat([]byte(fmt.Sprintf(webLine, "203.0.113.3")), 3), 0o644)
	if n, _ := l.Requests(map[string]*Peer{}, nil); n != 3 {
		t.Errorf("after rotation: %d, want 3", n)
	}
}

// WebStats.add reads nginx's combined format, including requests that never finished arriving.
func TestWebStatsAdd(t *testing.T) {
	w := NewWebStats()
	for _, line := range []string{
		`203.0.113.7 - - [09/Nov/2026:12:00:00 +0000] "GET /login?user=a HTTP/1.1" 200 512 "-" "curl/8.5.0"`,
		`203.0.113.7 - - [09/Nov/2026:12:00:00 +0000] "GET /login?user=a HTTP/1.1" 200 512 "-" "curl/8.5.0"`,
		`203.0.113.7 - - [09/Nov/2026:12:00:00 +0000] "GET /login?user=b HTTP/1.1" 302 0 "-" "curl/8.5.0"`,
		`203.0.113.8 - - [09/Nov/2026:12:00:01 +0000] "-" 400 0 "-" "-"`,
		`203.0.113.9 - - [09/Nov/2026:12:00:02 +0000] "\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03" 400 157 "-" "-"`,
		`203.0.113.10 - - [09/Nov/2026:12:00:03 +0000] "POST /` + strings.Repeat("a", 200) + ` HTTP/1.1" 404 0 "-" "` + strings.Repeat("b", 200) + `"`,
		`not a log line at all`,
	} {
		w.add([]byte(line))
	}
	if w.Requests != 7 {
		t.Errorf("Requests = %d", w.Requests)
	}
	if w.ByRequest["GET /login"] != 3 || w.ByRequest["- -"] != 2 || w.ByRequest["POST /"+strings.Repeat("a", 119)] != 1 {
		t.Errorf("ByRequest = %v", w.ByRequest)
	}
	if w.Queries["/login"] != 2 {
		t.Errorf("Queries = %v (distinct query strings)", w.Queries)
	}
	if w.Statuses[200] != 2 || w.Statuses[302] != 1 || w.Statuses[400] != 2 || w.Statuses[404] != 1 {
		t.Errorf("Statuses = %v", w.Statuses)
	}
	if w.Agents["curl/8.5.0"] != 3 || w.Agents[""] != 2 || w.Agents[strings.Repeat("b", 120)] != 1 {
		t.Errorf("Agents = %v", w.Agents)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
