package notify_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/notify"
	"nightjar/internal/storage"
)

// fake is a channel that records what it is sent.
type fake struct {
	name  string
	fail  bool
	delay time.Duration
	mu    sync.Mutex
	got   []string
}

func (f *fake) Name() string { return f.name }
func (f *fake) LastError() string {
	if f.fail {
		return "HTTP 500"
	}
	return ""
}
func (f *fake) SendEmbed(title, _ string, _ discord.Severity, _ []discord.Field, _ string) bool {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, title)
	return !f.fail
}
func (f *fake) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func level(l notify.Level) func(string) notify.Level { return func(string) notify.Level { return l } }

func newHub(t *testing.T, dests ...notify.Destination) (*notify.Hub, *storage.Store) {
	t.Helper()
	db := storage.Open(filepath.Join(t.TempDir(), "n.db"))
	t.Cleanup(db.Close)
	return notify.NewHub(db, dests), db
}

func at(clock string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-02 "+clock, time.Local)
	return t
}

func history(db *storage.Store) []storage.Notification {
	n, _ := db.Notifications(storage.NotificationQuery{})
	return n
}

func TestHubKeepsEveryAlertAndRoutesByArea(t *testing.T) {
	discordCh, ntfy := &fake{name: "Discord"}, &fake{name: "ntfy"}
	hub, db := newHub(t, notify.Destination{Channel: discordCh, Default: level(notify.All)}, notify.Destination{Channel: ntfy, Default: level(notify.Warnings)})

	hub.Route("resources").SendEmbed("CPU Usage Alert", "high", discord.Warning, nil, "")
	discord.About(hub.Route("resources"), "check:Plex").SendEmbed("Service Down: Plex", "timeout", discord.Critical, nil, "Uptime")
	hub.Route("media").SendEmbed("Now playing: Alien", "", discord.Info, nil, "")

	if got := discordCh.sent(); len(got) != 3 {
		t.Fatalf("Discord takes everything by default, got %v", got)
	}
	if got := ntfy.sent(); len(got) != 2 {
		t.Fatalf("ntfy at warnings should skip the info alert, got %v", got)
	}
	h := history(db)
	if len(h) != 3 {
		t.Fatalf("every alert is kept, got %d", len(h))
	}
	plex := h[1]
	if plex.Area != "uptime" || plex.Subject != "check:Plex" || plex.Status != "sent" || strings.Join(plex.Channels, ",") != "Discord,ntfy" {
		t.Fatalf("the check alert should be filed under uptime with its subject, got %+v", plex)
	}
	if h[0].Area != "media" || strings.Join(h[0].Channels, ",") != "Discord" {
		t.Fatalf("the Plex alert should go to Discord only, got %+v", h[0])
	}

	// A rule turns media off for Discord; other areas keep their defaults.
	if err := hub.SetRules(map[string]map[string]notify.Level{"media": {"Discord": notify.Off}}, hub.Quiet()); err != nil {
		t.Fatal(err)
	}
	hub.Route("media").SendEmbed("Now playing: Heat", "", discord.Info, nil, "")
	if got := discordCh.sent(); len(got) != 3 {
		t.Fatalf("Discord should no longer hear about Plex, got %v", got)
	}
	if latest := history(db)[0]; latest.Status != "kept" || len(latest.Channels) != 0 {
		t.Fatalf("an alert no channel takes is still kept, got %+v", latest)
	}
}

func TestRecoveriesPassAnyLevelButOff(t *testing.T) {
	c := &fake{name: "ntfy"}
	hub, _ := newHub(t, notify.Destination{Channel: c, Default: level(notify.Critical)})
	for _, sev := range []discord.Severity{discord.Info, discord.Warning, discord.Critical, discord.OK} {
		hub.Route("uptime").SendEmbed(sev.String(), "", sev, nil, "")
	}
	if got := c.sent(); strings.Join(got, ",") != "critical,ok" {
		t.Fatalf("a critical-only channel hears criticals and recoveries, got %v", got)
	}
}

func TestHubDeliversInParallel(t *testing.T) {
	var dests []notify.Destination
	for _, n := range []string{"a", "b", "c"} {
		dests = append(dests, notify.Destination{Channel: &fake{name: n, delay: 150 * time.Millisecond}})
	}
	hub, _ := newHub(t, dests...)
	start := time.Now()
	if !hub.Route("system").SendEmbed("x", "", discord.Critical, nil, "") {
		t.Fatal("delivery should report success")
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("three 150ms channels took %s; they should run at once", took)
	}
}

func TestDiscordUsesEachAreasWebhook(t *testing.T) {
	main, disk := &fake{name: "Discord"}, &fake{name: "Discord"}
	hub, _ := newHub(t, notify.Destination{Channel: main, ByArea: map[string]notify.Channel{"storage": disk}})
	hub.Route("storage").SendEmbed("Disk Usage Alert", "", discord.Warning, nil, "")
	hub.Route("resources").SendEmbed("Drive failing", "", discord.Critical, nil, "Disk") // a drive alert is storage too
	hub.Route("resources").SendEmbed("CPU Usage Alert", "", discord.Warning, nil, "")
	if len(disk.sent()) != 2 || len(main.sent()) != 1 {
		t.Fatalf("storage alerts go to the disk webhook: disk %v, main %v", disk.sent(), main.sent())
	}
}

func TestFailedDeliveryIsRetriedInPlace(t *testing.T) {
	c := &fake{name: "Discord", fail: true}
	hub, db := newHub(t, notify.Destination{Channel: c})
	r := hub.Route("resources")
	if r.SendEmbed("CPU Usage Alert", "", discord.Warning, nil, "") {
		t.Fatal("a delivery that failed everywhere reports failure, so it is retried")
	}
	h := history(db)
	if len(h) != 1 || h[0].Status != "failed" || !strings.Contains(h[0].Error, "HTTP 500") {
		t.Fatalf("the failure should be kept with its reason, got %+v", h)
	}
	c.fail = false
	if !r.SendEmbed("CPU Usage Alert", "", discord.Warning, nil, "") {
		t.Fatal("the retry should succeed")
	}
	if h := history(db); len(h) != 1 || h[0].Status != "sent" {
		t.Fatalf("the retry should update the same record, got %+v", h)
	}
}

// Only the same alert sent again soon after is a retry: one about something else, or the same
// title long after, is an alert of its own and keeps its own record.
func TestALaterAlertWithTheSameTitleIsNotARetry(t *testing.T) {
	c := &fake{name: "Discord", fail: true}
	hub, db := newHub(t, notify.Destination{Channel: c})
	now := at("10:00")
	hub.Now = func() time.Time { return now }
	r := hub.Route("uptime")
	discord.About(r, "check:plex").SendEmbed("is down", "timed out", discord.Critical, nil, "")
	discord.About(r, "check:sonarr").SendEmbed("is down", "timed out", discord.Critical, nil, "")
	if h := history(db); len(h) != 2 {
		t.Fatalf("an alert about another subject is a new record, got %+v", h)
	}
	c.fail = false
	now = now.Add(notify.RetryWindow + time.Minute)
	discord.About(r, "check:plex").SendEmbed("is down", "timed out", discord.Critical, nil, "")
	h := history(db)
	if len(h) != 3 || h[0].Status != "sent" || h[1].Status != "failed" || h[2].Status != "failed" {
		t.Fatalf("the same alert long after is a new record, and the old failures stay failed, got %+v", h)
	}
}

func TestQuietHoursHoldAlertsForOneSummary(t *testing.T) {
	c := &fake{name: "Discord"}
	hub, db := newHub(t, notify.Destination{Channel: c})
	if err := hub.SetRules(nil, notify.Quiet{Enabled: true, Start: "23:00", End: "07:00", Critical: true}); err != nil {
		t.Fatal(err)
	}
	hub.Now = func() time.Time { return at("02:00") }
	r := hub.Route("services")
	r.SendEmbed("sonarr keeps restarting", "", discord.Warning, nil, "Services")
	r.SendEmbed("radarr is running again", "", discord.OK, nil, "Services")
	r.SendEmbed("nginx failed", "", discord.Critical, nil, "Services")
	if got := c.sent(); strings.Join(got, ",") != "nginx failed" {
		t.Fatalf("only the critical alert goes out during quiet hours, got %v", got)
	}
	if quiet, held := hub.QuietNow(); !quiet || held != 2 {
		t.Fatalf("two alerts should be held, got quiet=%v held=%d", quiet, held)
	}

	hub.Tick()
	if len(c.sent()) != 1 {
		t.Fatal("no summary before quiet hours end")
	}
	hub.Now = func() time.Time { return at("07:01") }
	hub.Tick()
	got := c.sent()
	if len(got) != 2 || got[1] != "Quiet hours summary: 2 alerts" {
		t.Fatalf("one summary should follow quiet hours, got %v", got)
	}
	for _, n := range history(db) {
		if n.Status == "held" {
			t.Fatalf("nothing should stay held, got %+v", n)
		}
	}
	hub.Tick()
	if len(c.sent()) != 2 {
		t.Fatal("the summary is sent once")
	}
}

func TestMutesSilenceOneSubjectOrArea(t *testing.T) {
	c := &fake{name: "Discord"}
	hub, db := newHub(t, notify.Destination{Channel: c})
	now := at("12:00")
	hub.Now = func() time.Time { return now }
	if err := hub.Mute("unit:sonarr.service", "sonarr", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := hub.Mute("area:torrents", "", 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	units := hub.Route("resources")
	discord.About(units, "unit:sonarr.service").SendEmbed("sonarr failed", "", discord.Critical, nil, "Services")
	discord.About(units, "unit:radarr.service").SendEmbed("radarr failed", "", discord.Critical, nil, "Services")
	hub.Route("torrents").SendEmbed("Download Complete", "", discord.OK, nil, "qBittorrent")
	if got := c.sent(); strings.Join(got, ",") != "radarr failed" {
		t.Fatalf("muted alerts should not be sent, got %v", got)
	}
	if h := history(db); h[0].Status != "muted" || h[2].Status != "muted" {
		t.Fatalf("muted alerts are still kept, got %+v", h)
	}

	now = now.Add(61 * time.Minute)
	hub.Tick()
	if m := hub.Mutes(); len(m) != 1 || m[0].Subject != "area:torrents" {
		t.Fatalf("the hour-long mute should have expired, got %+v", m)
	}
	discord.About(units, "unit:sonarr.service").SendEmbed("sonarr failed", "", discord.Critical, nil, "Services")
	if len(c.sent()) != 2 {
		t.Fatal("sonarr should alert again once its mute ends")
	}

	for _, bad := range []string{"plex", "area:nowhere", "unit:"} {
		if hub.Mute(bad, "", time.Hour) == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	if hub.Mute("check:Plex", "", 8*24*time.Hour) == nil {
		t.Fatal("mutes are capped at a week")
	}
}

func TestPrefsSurviveARestart(t *testing.T) {
	c := &fake{name: "ntfy"}
	db := storage.Open(filepath.Join(t.TempDir(), "n.db"))
	defer db.Close()
	hub := notify.NewHub(db, []notify.Destination{{Channel: c, Default: level(notify.Warnings)}})
	if err := hub.SetRules(map[string]map[string]notify.Level{"updates": {"ntfy": notify.All}}, notify.Quiet{Enabled: true, Start: "22:30", End: "06:00"}); err != nil {
		t.Fatal(err)
	}
	again := notify.NewHub(db, []notify.Destination{{Channel: c, Default: level(notify.Warnings)}})
	rules, defaults := again.Rules()
	if rules["updates"]["ntfy"] != notify.All || rules["uptime"]["ntfy"] != notify.Warnings || defaults["updates"]["ntfy"] != notify.Warnings {
		t.Fatalf("rules should be kept and fall back to defaults, got %v / %v", rules, defaults)
	}
	if q := again.Quiet(); !q.Enabled || q.Start != "22:30" {
		t.Fatalf("quiet hours should be kept, got %+v", q)
	}

	for _, bad := range []map[string]map[string]notify.Level{
		{"nowhere": {"ntfy": notify.All}},
		{"updates": {"Pager": notify.All}},
		{"updates": {"ntfy": "loud"}},
	} {
		if hub.SetRules(bad, hub.Quiet()) == nil {
			t.Fatalf("%v should be refused", bad)
		}
	}
	if hub.SetRules(nil, notify.Quiet{Enabled: true, Start: "25:00", End: "06:00"}) == nil {
		t.Fatal("an impossible time should be refused")
	}
}

func TestAQuietHoursSummaryThatFailsIsRetried(t *testing.T) {
	c := &fake{name: "Discord"}
	hub, db := newHub(t, notify.Destination{Channel: c})
	hub.SetRules(nil, notify.Quiet{Enabled: true, Start: "23:00", End: "07:00"})
	hub.Now = func() time.Time { return at("02:00") }
	hub.Route("services").SendEmbed("sonarr keeps restarting", "", discord.Warning, nil, "Services")

	hub.Now = func() time.Time { return at("07:01") }
	c.mu.Lock()
	c.fail = true
	c.mu.Unlock()
	hub.Tick()
	if h := history(db); h[0].Status != "held" {
		t.Fatalf("a summary that did not get through stays held for a retry, got %+v", h[0])
	}
	c.mu.Lock()
	c.fail = false
	c.mu.Unlock()
	hub.Tick()
	if h := history(db); h[0].Status != "digest" {
		t.Fatalf("the retry delivers it, got %+v", h[0])
	}

	// A channel that stays down gives up after a few tries.
	hub.Now = func() time.Time { return at("02:00") }
	hub.Route("services").SendEmbed("radarr keeps restarting", "", discord.Warning, nil, "Services")
	hub.Now = func() time.Time { return at("07:01") }
	c.mu.Lock()
	c.fail = true
	c.mu.Unlock()
	for range 10 {
		hub.Tick()
	}
	if h := history(db); h[0].Status != "failed" {
		t.Fatalf("after the retries it is marked failed, got %+v", h[0])
	}
}

func TestFlappingSubjectIsPausedThenSettles(t *testing.T) {
	c := &fake{name: "Discord"}
	hub, db := newHub(t, notify.Destination{Channel: c})
	now := at("12:00")
	hub.Now = func() time.Time { return now }
	api := discord.About(hub.Route("uptime"), "check:api")
	titles := []string{"Service Down: api", "Service Recovered: api"}
	for i := range 10 {
		api.SendEmbed(titles[i%2], "", discord.Critical, nil, "Uptime")
		now = now.Add(time.Minute)
	}
	got := c.sent()
	if len(got) != 7 || got[6] != "Alerts paused: Service Down: api" {
		t.Fatalf("six alerts, then one saying the rest are paused, got %v", got)
	}
	throttled := 0
	for _, n := range history(db) {
		if n.Status == "throttled" {
			throttled++
		}
	}
	if throttled != 4 {
		t.Fatalf("the four alerts after the sixth are kept but not sent, got %d", throttled)
	}
	discord.About(hub.Route("uptime"), "check:web").SendEmbed("Service Down: web", "", discord.Critical, nil, "Uptime")
	if got := c.sent(); got[len(got)-1] != "Service Down: web" {
		t.Fatal("another check is not held back by api's flapping")
	}

	hub.Tick()
	if len(c.sent()) != 8 {
		t.Fatal("it has not settled yet")
	}
	now = now.Add(15 * time.Minute)
	hub.Tick()
	if got := c.sent(); got[len(got)-1] != "Settled: Service Recovered: api" {
		t.Fatalf("one message should say how it ended up, got %v", got)
	}
	api.SendEmbed("Service Down: api", "", discord.Critical, nil, "Uptime")
	if got := c.sent(); got[len(got)-1] != "Service Down: api" {
		t.Fatal("once settled, its alerts go out again")
	}
}

func TestAlertStormIsHeldForOneSummary(t *testing.T) {
	c := &fake{name: "Discord"}
	hub, db := newHub(t, notify.Destination{Channel: c})
	now := at("12:00")
	hub.Now = func() time.Time { return now }
	for i := range 15 {
		discord.About(hub.Route("servers"), "host:"+string(rune('a'+i))).SendEmbed("Server offline: "+string(rune('a'+i)), "", discord.Critical, nil, "Servers")
	}
	if n := len(c.sent()); n != 10 {
		t.Fatalf("ten alerts a minute go out, got %d", n)
	}
	hub.Tick()
	if n := len(c.sent()); n != 10 {
		t.Fatal("the summary waits, so it cannot become a flood itself")
	}
	now = now.Add(5 * time.Minute)
	hub.Tick()
	got := c.sent()
	if len(got) != 11 || got[10] != "Alert summary: 5 alerts" {
		t.Fatalf("the held alerts go out in one summary, got %v", got)
	}
	for _, n := range history(db) {
		if n.Status == "held" {
			t.Fatalf("nothing should stay held, got %+v", n)
		}
	}
	hub.Tick()
	if len(c.sent()) != 11 {
		t.Fatal("the summary is sent once")
	}
	discord.About(hub.Route("servers"), "host:z").SendEmbed("Server offline: z", "", discord.Critical, nil, "Servers")
	if len(c.sent()) != 12 {
		t.Fatal("once the minute has passed, alerts go out again")
	}
}
