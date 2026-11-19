package ddos

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A site behind a CDN other than Cloudflare, or a load balancer, gets every visitor's traffic from a
// handful of the proxy's addresses, and each of them holds the connections of many visitors at
// once. Blocked, one of them takes all those visitors with it. nginx already knows such proxies: it
// is told to take the visitor's own address from their requests with set_real_ip_from. The
// defender reads those lines and never blocks those networks, as it never blocks Cloudflare.

// defaultNginxDir is where nginx's settings are, when the defender is not told otherwise.
const defaultNginxDir = "/etc/nginx"

// proxiesEvery is how often nginx's settings are read again.
const proxiesEvery = 5 * time.Minute

// Limits on what is read, so a strange settings folder cannot slow the defender down.
const (
	maxNginxFiles    = 500
	maxNginxFileSize = 1 << 20
)

// proxiesLocked are the networks nginx takes visitors' addresses from, read again now and then.
func (d *Defender) proxiesLocked() []string {
	now := d.now()
	if d.proxiesRead.IsZero() || now.Sub(d.proxiesRead) >= proxiesEvery || now.Before(d.proxiesRead) {
		dir := d.NginxDir
		if dir == "" {
			dir = defaultNginxDir
		}
		d.proxies, d.proxiesRead = RealIPFrom(dir), now
	}
	return d.proxies
}

// RealIPFrom reads the addresses and networks in the set_real_ip_from lines of nginx's settings
// under dir ("-" for none). A network so wide it would take in a large part of the internet (as
// set_real_ip_from 0.0.0.0/0, a common shortcut, does) is left out: trusting it would turn the
// defender off.
func RealIPFrom(dir string) []string {
	if dir == "-" {
		return nil
	}
	var out []string
	files := 0
	filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			return nil
		}
		// Links are how sites-enabled names its sites; Stat follows them.
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxNginxFileSize {
			return nil
		}
		if files++; files > maxNginxFiles {
			return filepath.SkipAll
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line, _, _ = strings.Cut(line, "#")
			for _, stmt := range strings.Split(line, ";") {
				f := strings.Fields(stmt)
				if len(f) == 2 && f[0] == "set_real_ip_from" && narrowEnough(f[1]) {
					out = append(out, f[1])
				}
			}
		}
		return nil
	})
	return out
}

// narrowEnough says whether a set_real_ip_from value is an address, or a network no wider than an
// IPv4 /8 or an IPv6 /16. Anything else (a unix socket, a host name, the whole internet) is not.
func narrowEnough(s string) bool {
	if ip := net.ParseIP(s); ip != nil {
		return true
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return false
	}
	ones, bits := n.Mask.Size()
	return (bits == 32 && ones >= 8) || (bits == 128 && ones >= 16)
}
