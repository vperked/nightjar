package remote_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/remote"
	"nightjar/internal/services"
	"nightjar/internal/smart"
	"nightjar/internal/storage"
)

const token = "0123456789abcdef0123456789abcdef"

// fakeAgent serves readings the test controls, and can go down.
type fakeAgent struct {
	mu   sync.Mutex
	r    agent.Readings
	down bool
	srv  *httptest.Server
}

func newAgent(t *testing.T) *fakeAgent {
	temp := 50.0
	a := &fakeAgent{r: agent.Readings{Version: agent.Version, Agent: "test", Time: 1, System: collect.System{Hostname: "nas"},
		CPU: agent.CPU{Percent: 12, Count: 4}, Memory: collect.Memory{Percent: 40}, Temperature: &temp,
		Disks:  []collect.Partition{{Mountpoint: "/", Percent: 50}},
		Drives: []smart.Drive{{Device: "/dev/sda", Health: "good"}}}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != agent.Path || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(a.r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAgent) set(f func(r *agent.Readings, down *bool)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(&a.r, &a.down)
}

// alerts records what the monitor sends; sends happen in the background.
type alerts struct {
	mu  sync.Mutex
	got []string
}

func (s *alerts) SendEmbed(title, _ string, sev discord.Severity, _ []discord.Field, _ string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, sev.String()+": "+title)
	return true
}

// after waits briefly for n alerts and returns them.
func (s *alerts) after(n int) []string {
	for range 100 {
		s.mu.Lock()
		l := len(s.got)
		s.mu.Unlock()
		if l >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // and no more than that
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func newMonitor(t *testing.T, db *storage.Store, sent *alerts) *remote.Monitor {
	return remote.New(remote.Options{Store: db, Alerts: sent, Limits: func() remote.Limits { return remote.Limits{Temp: 80, Memory: 90, Disk: 90} }})
}

func openDB(t *testing.T) *storage.Store {
	db := storage.Open(filepath.Join(t.TempDir(), "r.db"))
	t.Cleanup(db.Close)
	return db
}

func TestAddChecksTheAgentAndKeepsTheTokenPrivate(t *testing.T) {
	a := newAgent(t)
	db := openDB(t)
	m := newMonitor(t, db, &alerts{})
	ctx := context.Background()

	if _, err := m.Add(ctx, "", a.srv.URL, "wrong"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := m.Add(ctx, "", "http://127.0.0.1:1", token); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("nothing listening: %v", err)
	}
	st, err := m.Add(ctx, "", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "nas" || st.Status != "online" || st.Readings == nil || len(st.History) != 1 {
		t.Fatalf("added: %+v", st)
	}
	if _, err := m.Add(ctx, "again", a.srv.URL, token); err != remote.ErrExists {
		t.Fatalf("the same agent twice: %v", err)
	}
	raw, _ := json.Marshal(m.List(60))
	if strings.Contains(string(raw), token) {
		t.Fatal("the token must never reach the browser")
	}

	// Saved: a fresh monitor knows it, and polls it with the saved token.
	m2 := newMonitor(t, db, &alerts{})
	if l := m2.List(60); len(l) != 1 || l[0].Status != "waiting" {
		t.Fatalf("after a restart: %+v", l)
	}
	m2.PollAll(ctx)
	if l := m2.List(60); l[0].Status != "online" {
		t.Fatalf("polled: %+v", l[0])
	}

	if _, err := m2.Update(ctx, st.ID, "Living room NAS", a.srv.URL, ""); err != nil {
		t.Fatalf("rename keeping the token: %v", err)
	}
	if s, _ := m2.Get(st.ID); s.Name != "Living room NAS" {
		t.Fatalf("renamed: %+v", s)
	}
	if err := m2.Remove(st.ID); err != nil || len(m2.List(60)) != 0 {
		t.Fatalf("remove: %v", err)
	}
}

func TestOfflineAndBackOnlineAlertOnce(t *testing.T) {
	a := newAgent(t)
	sent := &alerts{}
	m := newMonitor(t, openDB(t), sent)
	ctx := context.Background()
	added, err := m.Add(ctx, "nas", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	a.set(func(_ *agent.Readings, down *bool) { *down = true })
	m.PollAll(ctx)
	m.PollAll(ctx)
	if got := sent.after(0); len(got) != 0 {
		t.Fatalf("two missed polls are not an outage yet: %v", got)
	}
	m.PollAll(ctx)
	m.PollAll(ctx)
	if got := sent.after(1); len(got) != 1 || got[0] != "critical: Server offline: nas" {
		t.Fatalf("offline once: %v", got)
	}
	if l := m.List(60); l[0].Status != "offline" || len(l[0].Problems) != 1 {
		t.Fatalf("state: %+v", l[0])
	}
	if s, _ := m.Get(added.ID); len(s.History) != 2 || s.History[1].CPU != nil {
		t.Fatalf("the outage leaves one gap in the history: %+v", s.History)
	}
	a.set(func(_ *agent.Readings, down *bool) { *down = false })
	m.PollAll(ctx)
	if got := sent.after(2); len(got) != 2 || got[1] != "ok: Server back online: nas" {
		t.Fatalf("back online: %v", got)
	}
}

func TestReadingsOverTheLimitsAlertAndRecover(t *testing.T) {
	a := newAgent(t)
	sent := &alerts{}
	db := openDB(t)
	m := newMonitor(t, db, sent)
	ctx := context.Background()
	if _, err := m.Add(ctx, "nas", a.srv.URL, token); err != nil {
		t.Fatal(err)
	}
	hot := 86.0
	a.set(func(r *agent.Readings, _ *bool) {
		r.Temperature = &hot
		r.Disks[0].Percent = 95
		r.Drives[0].Health, r.Drives[0].Problems = "failing", []string{"12 reallocated sectors"}
	})
	m.PollAll(ctx) // one hot reading is not enough; a full disk and a failing drive are
	got := sent.after(2)
	if len(got) != 2 || got[0] != "warning: nas: / is 95% full" || got[1] != "critical: nas: drive /dev/sda is failing" {
		t.Fatalf("first poll: %v", got)
	}
	m.PollAll(ctx)
	m.PollAll(ctx)
	got = sent.after(3)
	if len(got) != 3 || got[2] != "warning: nas: CPU at 86°C" {
		t.Fatalf("hot twice, alerted once: %v", got)
	}

	// A restart does not announce the same problems again.
	m2 := newMonitor(t, db, sent)
	m2.PollAll(ctx)
	m2.PollAll(ctx)
	if got := sent.after(3); len(got) != 3 {
		t.Fatalf("after a restart: %v", got)
	}

	// 78° is under the limit but within 5° of it: still hot. 70° clears it.
	a.set(func(r *agent.Readings, _ *bool) { t := 78.0; r.Temperature = &t })
	m2.PollAll(ctx)
	if got := sent.after(3); len(got) != 3 {
		t.Fatalf("near the limit: %v", got)
	}
	a.set(func(r *agent.Readings, _ *bool) {
		t := 70.0
		r.Temperature = &t
		r.Disks[0].Percent = 60
		r.Drives[0].Health = "good"
	})
	m2.PollAll(ctx)
	got = sent.after(6)
	recovered := strings.Join(got[3:], "|")
	for _, want := range []string{"ok: nas: CPU temperature back to 70°C", "ok: nas: / back to 60% full", "ok: nas: drive /dev/sda is healthy again"} {
		if !strings.Contains(recovered, want) {
			t.Errorf("missing %q in %v", want, got[3:])
		}
	}
	if l := m2.List(60); len(l[0].Problems) != 0 {
		t.Fatalf("nothing is wrong now: %+v", l[0].Problems)
	}
}

func TestAddressesAreNormalized(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.0.20":           "https://192.168.0.20:9101",
		"nas:9200":               "https://nas:9200",
		"http://nas.local:9101/": "http://nas.local:9101",
		"https://nas.example":    "https://nas.example:9101",
		"[fd00::5]":              "https://[fd00::5]:9101",
	} {
		if got, err := remote.NormalizeAddress(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://nas", "http://nas/some/path", "http://user:pw@nas", "nas:99999"} {
		if _, err := remote.NormalizeAddress(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestHTTPSAgentsArePinnedToTheirTokensKey(t *testing.T) {
	ctx := context.Background()
	serve := func(certToken string) *httptest.Server {
		a := newAgent(t)
		srv := httptest.NewUnstartedServer(a.srv.Config.Handler)
		cert, _ := agent.Certificate(certToken)
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		return srv
	}
	m := newMonitor(t, openDB(t), &alerts{})
	real := serve(token)
	st, err := m.Add(ctx, "nas", real.URL, token)
	if err != nil || st.Status != "online" {
		t.Fatalf("an HTTPS agent with its own key: %+v %v", st, err)
	}
	m.PollAll(ctx)
	if l := m.List(60); l[0].Status != "online" {
		t.Fatalf("polled over HTTPS: %+v", l[0])
	}

	impostor := serve("someone-elses-token-0123456789abcdef")
	if _, err := m.Add(ctx, "fake", impostor.URL, token); err == nil || !strings.Contains(err.Error(), "certificate does not belong") {
		t.Fatalf("an HTTPS server without the token's key: %v", err)
	}

	plain := newAgent(t)
	if _, err := m.Add(ctx, "plain", strings.Replace(plain.srv.URL, "http://", "https://", 1), token); err == nil || !strings.Contains(err.Error(), "use http://") {
		t.Fatalf("https:// to a plain agent: %v", err)
	}
	if _, err := m.Add(ctx, "wrong scheme", strings.Replace(real.URL, "https://", "http://", 1), token); err == nil || !strings.Contains(err.Error(), "use https://") {
		t.Fatalf("http:// to an HTTPS agent: %v", err)
	}
}

func TestServersWithoutAnAgentBecomeUptimeChecks(t *testing.T) {
	db := openDB(t)
	a := newAgent(t)
	// As saved before: one agent, and a router and a port watched with no agent.
	saved := []remote.Host{
		{ID: "agent1", Name: "nas", Kind: remote.KindAgent, Address: a.srv.URL, Token: token},
		{ID: "router1", Name: "router", Kind: remote.KindCheck, Address: "https://192.168.0.1", Check: &remote.Check{Type: "http", Target: "https://192.168.0.1", SelfSigned: true}},
		{ID: "vps1", Name: "vps", Kind: remote.KindCheck, Address: "vps.example.net:22", Check: &remote.Check{Type: "tcp", Target: "vps.example.net:22"}},
	}
	raw, _ := json.Marshal(saved)
	db.SetMeta("remote.hosts", string(raw))
	now := float64(time.Now().Unix())
	lat := 3.5
	db.AddSamples([]storage.Sample{{Host: "router1", TS: now - 120, Up: 1, Latency: &lat}, {Host: "router1", TS: now - 60, Up: 0}})

	m := newMonitor(t, db, &alerts{})
	if got := m.List(1); len(got) != 1 || got[0].Name != "nas" {
		t.Fatalf("only the agent is a server: %+v", got)
	}
	// A check called router already exists, so the moved one takes the next free name.
	checks := services.NewMonitor([]services.Spec{{Name: "router", Type: "ping", Target: "10.0.0.1", Interval: 60}}, db)
	db.SaveMonitor("", "router", []byte(`{"name":"router","type":"ping","target":"10.0.0.1"}`))
	m.MoveLegacyChecks(checks)

	specs := checks.Specs()
	if len(specs) != 3 || specs[1].Name != "router 2" || specs[2].Name != "vps" {
		t.Fatalf("moved checks: %+v", specs)
	}
	router, vps := specs[1], specs[2]
	if router.Type != "http" || router.Target != "https://192.168.0.1" || router.VerifyTLS || !slices.Contains(router.ExpectedStatus, 401) {
		t.Fatalf("the router keeps its target, its self-signed certificate and counting a sign-in page as up: %+v", router)
	}
	if vps.Type != "tcp" || vps.Target != "vps.example.net:22" || !vps.VerifyTLS {
		t.Fatalf("vps: %+v", vps)
	}
	if hist := db.Series("router 2", 0, 10); len(hist) != 2 || !hist[0].Up || hist[1].Up {
		t.Fatalf("the router's minutes became its check's history: %+v", hist)
	}
	if left := db.SampleBuckets("router1", 0, 60); len(left) != 0 {
		t.Fatalf("its server readings are gone: %+v", left)
	}

	// Moved once: a restart finds only the agent, which kept its token.
	again := newMonitor(t, db, &alerts{})
	again.MoveLegacyChecks(checks)
	if len(checks.Specs()) != 3 || len(again.List(1)) != 1 {
		t.Fatalf("a second start moves nothing: %d checks, %d servers", len(checks.Specs()), len(again.List(1)))
	}
	var stored []remote.Host
	json.Unmarshal([]byte(db.Meta("remote.hosts")), &stored)
	if len(stored) != 1 || stored[0].Token != token {
		t.Fatalf("saved servers: %+v", stored)
	}
}

// A drive that drops off the bus is how a failing disk often goes: its alert is not dropped with it.
// It is announced as gone (critical when it was already failing), its health alert kept, and
// resolved once it is back. An agent that has not read its drives yet since it started leaves the
// drive alerts as they are.
func TestADriveThatDisappearsIsAnnounced(t *testing.T) {
	a := newAgent(t)
	sent := &alerts{}
	m := newMonitor(t, openDB(t), sent)
	ctx := context.Background()
	if _, err := m.Add(ctx, "nas", a.srv.URL, token); err != nil {
		t.Fatal(err)
	}
	a.set(func(r *agent.Readings, _ *bool) {
		r.Drives = []smart.Drive{{Device: "/dev/sda", Health: "failing", Problems: []string{"12 reallocated sectors"}}, {Device: "/dev/sdb", Health: "good"}}
	})
	m.PollAll(ctx)
	if got := sent.after(1); len(got) != 1 || got[0] != "critical: nas: drive /dev/sda is failing" {
		t.Fatalf("failing: %v", got)
	}
	// The agent restarted, and has not read its drives yet.
	a.set(func(r *agent.Readings, _ *bool) { r.Drives, r.DrivesPending = []smart.Drive{}, true })
	m.PollAll(ctx)
	if l := m.List(60); len(sent.after(1)) != 1 || !strings.Contains(fmt.Sprint(l[0].Problems), "Drive /dev/sda is failing") {
		t.Fatalf("drive alerts kept while the drives are not read yet: %v %v", sent.after(1), l[0].Problems)
	}
	// Both drop off.
	a.set(func(r *agent.Readings, _ *bool) {
		r.DrivesPending = false
		r.Drives = []smart.Drive{{Device: "/dev/sda", Health: "failing", Missing: 2000}, {Device: "/dev/sdb", Health: "good", Missing: 2000}}
	})
	m.PollAll(ctx)
	m.PollAll(ctx)
	got := sent.after(3)
	if len(got) != 3 || !slices.Contains(got, "critical: nas: drive /dev/sda disappeared") || !slices.Contains(got, "warning: nas: drive /dev/sdb disappeared") {
		t.Fatalf("disappeared, once: %v", got)
	}
	if l := m.List(60); !strings.Contains(fmt.Sprint(l[0].Problems), "Drive /dev/sda is failing") {
		t.Fatalf("the failing alert stays: %v", l[0].Problems)
	}
	// One comes back.
	a.set(func(r *agent.Readings, _ *bool) {
		r.Drives = []smart.Drive{{Device: "/dev/sda", Health: "failing", Missing: 2000}, {Device: "/dev/sdb", Health: "good"}}
	})
	m.PollAll(ctx)
	if got := sent.after(4); len(got) != 4 || got[3] != "ok: nas: drive /dev/sdb is back" {
		t.Fatalf("back: %v", got)
	}
	// Forgotten by the agent a week on: nothing left to say about it.
	a.set(func(r *agent.Readings, _ *bool) { r.Drives = []smart.Drive{{Device: "/dev/sdb", Health: "good"}} })
	m.PollAll(ctx)
	if l := m.List(60); len(l[0].Problems) != 0 || len(sent.after(4)) != 4 {
		t.Fatalf("forgotten: %v %v", l[0].Problems, sent.after(4))
	}
}
