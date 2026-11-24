package tailscale_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"nightjar/internal/tailscale"
)

func manager(f *tailscale.Fake) *tailscale.Manager {
	return &tailscale.Manager{Exec: f.Exec, LoginWait: time.Millisecond}
}

func ptr[T any](v T) *T { return &v }

func TestTheTailnetIsReadAsTheDashboardShowsIt(t *testing.T) {
	m := manager(tailscale.NewFake())
	st := m.State(context.Background())
	if !st.Installed || !st.Running || !st.LoggedIn || st.Backend != "Running" || st.Problem != "" {
		t.Fatalf("state: %+v", st)
	}
	if st.Version != "1.104.7" || st.Tailnet != "owner@example.com" || st.MagicDNS != "tail1234.ts.net" {
		t.Fatalf("tailnet: %+v", st)
	}
	if st.Self == nil || st.Self.Name != "home" || st.Self.DNSName != "home.tail1234.ts.net" || st.Self.User != "owner@example.com" {
		t.Fatalf("self: %+v", st.Self)
	}
	if len(st.Peers) != 2 || st.Peers[0].Name != "laptop" || st.Peers[1].Name != "vps" {
		t.Fatalf("peers: %+v", st.Peers)
	}
	laptop, vps := st.Peers[0], st.Peers[1]
	if laptop.Direct != "192.168.1.20:41641" || laptop.RX != 100 || laptop.User != "owner@example.com" {
		t.Fatalf("laptop: %+v", laptop)
	}
	// A tagged machine belongs to the tailnet; the exit routes it offers are not subnets.
	if vps.User != "" || !vps.ExitNodeOption || vps.Relay != "fra" || !slices.Equal(vps.Routes, []string{"10.0.0.0/24"}) {
		t.Fatalf("vps: %+v", vps)
	}
	if !st.Settings.AcceptDNS || st.Settings.AcceptRoutes || st.Settings.ExitNode != "" || len(st.Settings.AdvertiseRoutes) != 0 {
		t.Fatalf("settings: %+v", st.Settings)
	}
}

func TestTailscaleNotInstalledOrNotRunningIsSaid(t *testing.T) {
	m := &tailscale.Manager{Exec: func(context.Context, string, ...string) (string, error) {
		return "failed to connect to local tailscaled; it doesn't appear to be running", errors.New("exit status 1")
	}}
	st := m.State(context.Background())
	if !st.Installed || st.Running || !strings.Contains(st.Problem, "tailscaled is not running") {
		t.Fatalf("not running: %+v", st)
	}
	if err := m.Set(context.Background(), tailscale.Settings{SSH: ptr(true)}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("change while not running: %v", err)
	}
}

func TestConnectingAndLoggingInAndOut(t *testing.T) {
	ctx := context.Background()
	f := tailscale.NewFake()
	m := manager(f)
	if err := m.Down(ctx, "192.168.1.5"); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); st.Backend != "Stopped" || !st.LoggedIn {
		t.Fatalf("down: %+v", st)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Calls, "up") {
		t.Fatalf("up runs tailscale up alone, so it changes no setting: %v", f.Calls)
	}
	if err := m.Login(ctx, ""); err == nil || !strings.Contains(err.Error(), "already logged in") {
		t.Fatalf("login while logged in: %v", err)
	}
	if err := m.Logout(ctx, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	st := m.State(ctx)
	if st.LoggedIn || st.Backend != "NeedsLogin" {
		t.Fatalf("logged out: %+v", st)
	}
	if err := m.Up(ctx); err == nil || !strings.Contains(err.Error(), "log in") {
		t.Fatalf("up while logged out: %v", err)
	}
	// Without a key, tailscaled gives a link to open and logs in by itself once someone does.
	if err := m.Login(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); st.AuthURL != "https://login.tailscale.com/a/fake" {
		t.Fatalf("no link: %+v", st)
	}
	if err := m.Login(ctx, "hunter2"); err == nil || !strings.Contains(err.Error(), "tskey-") {
		t.Fatalf("not a key: %v", err)
	}
	// A key goes through a file, never the command line.
	if err := m.Login(ctx, " tskey-auth-kAbC123-xyz "); err != nil {
		t.Fatal(err)
	}
	if f.AuthKey != "tskey-auth-kAbC123-xyz" || strings.Contains(strings.Join(f.Calls, "\n"), "tskey-") {
		t.Fatalf("key %q, calls %v", f.AuthKey, f.Calls)
	}
	if st := m.State(ctx); !st.LoggedIn || st.Backend != "Running" {
		t.Fatalf("logged in with a key: %+v", st)
	}
}

func TestCuttingOffWhoeverAsksOverTailscaleIsRefused(t *testing.T) {
	ctx := context.Background()
	f := tailscale.NewFake()
	m := manager(f)
	for _, via := range []string{"100.64.0.2", "100.127.255.1", "fd7a:115c:a1e0::2", "::ffff:100.100.1.1"} {
		if err := m.Down(ctx, via); err == nil || !strings.Contains(err.Error(), "over Tailscale") {
			t.Fatalf("down from %s: %v", via, err)
		}
		if err := m.Logout(ctx, via); err == nil || !strings.Contains(err.Error(), "over Tailscale") {
			t.Fatalf("logout from %s: %v", via, err)
		}
	}
	for _, via := range []string{"100.63.255.255", "100.128.0.1", "192.168.1.5", "fd00::1", ""} {
		if tailscale.OverTailscale(via) {
			t.Fatalf("%q is not a Tailscale address", via)
		}
	}
	if slices.Contains(f.Calls, "down --accept-risk=lose-ssh") || slices.Contains(f.Calls, "logout") {
		t.Fatalf("ran anyway: %v", f.Calls)
	}
}

func TestSettingsChangeOnlyWhatIsGiven(t *testing.T) {
	ctx := context.Background()
	f := tailscale.NewFake()
	m := manager(f)
	if err := m.Set(ctx, tailscale.Settings{AcceptRoutes: ptr(true), SSH: ptr(true), Hostname: ptr("media-box")}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls[len(f.Calls)-1]; got != "set --accept-routes=true --ssh=true --hostname=media-box" {
		t.Fatalf("ran %q", got)
	}
	st := m.State(ctx)
	if !st.Settings.AcceptRoutes || !st.Settings.SSH || st.Settings.Hostname != "media-box" || !st.Settings.AcceptDNS || st.Pending != nil {
		t.Fatalf("settings: %+v", st)
	}
	// Subnets are checked and written as Tailscale does; offering to be an exit node keeps them.
	if err := m.Set(ctx, tailscale.Settings{AdvertiseRoutes: &[]string{"192.168.1.0/24", " 10.0.0.0/8 ", "", "10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(ctx, tailscale.Settings{AdvertiseExitNode: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	st = m.State(ctx)
	if !st.Settings.AdvertiseExitNode || !slices.Equal(st.Settings.AdvertiseRoutes, []string{"192.168.1.0/24", "10.0.0.0/8"}) {
		t.Fatalf("advertised: %+v", st.Settings)
	}
	for routes, want := range map[string]string{
		"192.168.1.1/24": "host bits",
		"0.0.0.0/0":      "exit node instead",
		"lan":            "not a subnet",
	} {
		if err := m.Set(ctx, tailscale.Settings{AdvertiseRoutes: &[]string{routes}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", routes, err)
		}
	}
	for _, name := range []string{"-box", "my box", strings.Repeat("a", 64)} {
		if err := m.Set(ctx, tailscale.Settings{Hostname: ptr(name)}); err == nil {
			t.Fatalf("hostname %q taken", name)
		}
	}
	if err := m.Set(ctx, tailscale.Settings{ExitNode: ptr("nLaptop")}); err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Fatalf("laptop as exit node: %v", err)
	}
	if err := m.Set(ctx, tailscale.Settings{ExitNode: ptr("nope")}); err == nil || !strings.Contains(err.Error(), "no machine") {
		t.Fatalf("unknown exit node: %v", err)
	}
}

func TestAnExitNodeGoesBackUnlessKept(t *testing.T) {
	ctx := context.Background()
	f := tailscale.NewFake()
	now := time.Unix(1_700_000_000, 0)
	m := &tailscale.Manager{Exec: f.Exec, Now: func() time.Time { return now }}
	if err := m.Set(ctx, tailscale.Settings{ExitNode: ptr("nExit"), ExitNodeAllowLAN: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	// The machine is given by its IPv4 address.
	if got := f.Calls[len(f.Calls)-1]; got != "set --exit-node=100.64.0.3 --exit-node-allow-lan-access=true" {
		t.Fatalf("ran %q", got)
	}
	st := m.State(ctx)
	if st.Settings.ExitNode != "nExit" || !st.Peers[1].ExitNode || st.Pending == nil || st.Pending.Until != now.Add(2*time.Minute).Unix() || st.Pending.What != "the exit node" {
		t.Fatalf("exit node: %+v %+v", st.Settings, st.Pending)
	}
	// Shields up while that waits: going back undoes both, to how things were before either.
	if err := m.Set(ctx, tailscale.Settings{ShieldsUp: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); st.Pending == nil || st.Pending.What != "the exit node and shields up" {
		t.Fatalf("both: %+v", st.Pending)
	}
	now = now.Add(time.Minute)
	m.Lapse(ctx)
	if f.Pref("ExitNodeID") != "nExit" {
		t.Fatal("went back early")
	}
	now = now.Add(2 * time.Minute)
	m.Lapse(ctx)
	st = m.State(ctx)
	if st.Settings.ExitNode != "" || st.Settings.ExitNodeAllowLAN || st.Settings.ShieldsUp || st.Pending != nil {
		t.Fatalf("not put back: %+v %+v", st.Settings, st.Pending)
	}

	// Kept, it stays; taking it away again is safe and waits for nothing.
	if err := m.Set(ctx, tailscale.Settings{ExitNode: ptr("100.64.0.3")}); err != nil {
		t.Fatal(err)
	}
	if err := m.Keep(ctx, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	m.Lapse(ctx)
	if st := m.State(ctx); st.Settings.ExitNode != "nExit" || st.Pending != nil {
		t.Fatalf("kept: %+v", st)
	}
	if err := m.Keep(ctx, true); err == nil {
		t.Fatal("nothing waits to be kept")
	}
	if err := m.Set(ctx, tailscale.Settings{ExitNode: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); st.Settings.ExitNode != "" || st.Pending != nil {
		t.Fatalf("no exit node: %+v", st)
	}
	// Undone at once on request.
	m.Set(ctx, tailscale.Settings{ShieldsUp: ptr(true)})
	if err := m.Keep(ctx, false); err != nil {
		t.Fatal(err)
	}
	if st := m.State(ctx); st.Settings.ShieldsUp || st.Pending != nil {
		t.Fatalf("undo: %+v", st)
	}
}

func TestAChangeWaitingWhenNightjarStopsGoesBack(t *testing.T) {
	f := tailscale.NewFake()
	m := &tailscale.Manager{Exec: f.Exec, CheckEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	if err := m.Set(context.Background(), tailscale.Settings{ShieldsUp: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	if f.Pref("ShieldsUp") != false {
		t.Fatal("shields still up")
	}
}
