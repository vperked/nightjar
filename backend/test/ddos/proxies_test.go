package ddos_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/ddos"
)

// nginxBehindALoadBalancer is nginx's settings for a site behind a load balancer and a CDN that is
// not Cloudflare, with a site enabled through a link as Debian sets them up.
func nginxBehindALoadBalancer(t *testing.T) string {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755)
	os.MkdirAll(filepath.Join(dir, "sites-available"), 0o755)
	os.MkdirAll(filepath.Join(dir, "sites-enabled"), 0o755)
	os.WriteFile(filepath.Join(dir, "conf.d", "realip.conf"), []byte(`
# the load balancer
set_real_ip_from 198.51.100.0/24; real_ip_header X-Forwarded-For;
set_real_ip_from   2001:db8:cd::/48 ;
# set_real_ip_from 192.0.2.1;
set_real_ip_from 0.0.0.0/0;
set_real_ip_from unix:;
`), 0o644)
	os.WriteFile(filepath.Join(dir, "sites-available", "shop"), []byte("server {\n\tset_real_ip_from 203.0.113.40;\n}\n"), 0o644)
	os.Symlink(filepath.Join(dir, "sites-available", "shop"), filepath.Join(dir, "sites-enabled", "shop"))
	return dir
}

// The proxies nginx takes visitors' addresses from are read from its settings; one so wide it would
// trust the whole internet, a socket and a line put out of use are not.
func TestTheProxiesNginxTrustsAreRead(t *testing.T) {
	got := ddos.RealIPFrom(nginxBehindALoadBalancer(t))
	want := map[string]bool{"198.51.100.0/24": true, "2001:db8:cd::/48": true, "203.0.113.40": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("read %q, which is not to be trusted", g)
		}
	}
	for w := range want {
		if !strings.Contains(fmt.Sprint(got), w) {
			t.Errorf("missed %q in %v", w, got)
		}
	}
	if got := ddos.RealIPFrom(filepath.Join(t.TempDir(), "no nginx here")); len(got) != 0 {
		t.Errorf("no settings: %v", got)
	}
	if got := ddos.RealIPFrom("-"); got != nil {
		t.Errorf("none asked for: %v", got)
	}
}

// Behind a load balancer every visitor comes from a few of its addresses, each holding many
// connections. The defender never blocks those, as it never blocks Cloudflare, while it still blocks
// an attacker reaching the server directly.
func TestTheLoadBalancerNginxTrustsIsNeverBlocked(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	d.NginxDir = nginxBehindALoadBalancer(t)
	d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: []string{ddos.ConnectionFlood}, Conntrack: true,
		Sources: []ddos.Source{{Address: "198.51.100.7", Established: 4000}, {Address: "203.0.113.40", Established: 900}, {Address: "192.0.2.66", Established: 900}}})
	var got []string
	for _, b := range d.View().Blocked {
		got = append(got, b.Address)
	}
	if fmt.Sprint(got) != "[192.0.2.66]" || !strings.Contains(f.rules, "-s 198.51.100.0/24 -j RETURN") {
		t.Fatalf("blocked %v\n%s", got, f.rules)
	}
}
