package incidents_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/incidents"
	"nightjar/internal/storage"
)

type note = storage.Notification

var id int64

func n(ts float64, area, sev, title, body, subject string) note {
	id++
	return note{ID: id, TS: ts, Area: area, Severity: sev, Title: title, Body: body, Subject: subject}
}

func f(v float64) *float64 { return &v }

func TestOutOfMemoryExplainsTheChecksThatFollowed(t *testing.T) {
	notes := []note{
		n(1000, "updates", "ok", "Sonarr updated", "Installed and running.", ""),
		n(5000, "system", "critical", "Out of memory", "Killed process 4321 (rsync) total-vm:812345kB", ""),
		n(5040, "uptime", "critical", "nas-backup is down", "connection refused", "check:nas-backup"),
		n(5100, "uptime", "info", "Nightjar repaired nas-backup", "It was down, so Nightjar tried to restart rsyncd.service", "check:nas-backup"),
		n(5160, "uptime", "ok", "nas-backup is back up", "Connected in 3 ms", "check:nas-backup"),
		// Long after: a separate incident, still open.
		n(20000, "services", "critical", "bazarr failed", "Bazarr stopped", "unit:bazarr.service"),
	}
	samples := []storage.Sample{{TS: 4900, Mem: f(99.1), CPU: f(40)}, {TS: 5200, Mem: f(60)}}
	list := incidents.Build(notes, samples, 21000, nil)
	if len(list) != 2 {
		t.Fatalf("want 2 incidents, got %d: %+v", len(list), list)
	}
	latest, oom := list[0], list[1]
	if latest.Title != "bazarr failed" || latest.Ended != nil || latest.Cause == nil || latest.Cause.Kind != "crash" {
		t.Fatalf("an unrecovered service is an open incident: %+v %+v", latest, latest.Cause)
	}
	if oom.Started != 5000 || oom.Ended == nil || *oom.Ended != 5160 || oom.Severity != "critical" {
		t.Fatalf("the memory incident runs from the kernel's alert to the recovery: %+v", oom)
	}
	if oom.Cause == nil || oom.Cause.Kind != "memory" || oom.Cause.Confidence != "likely" || !strings.Contains(oom.Cause.Text, "rsync") || !strings.Contains(oom.Cause.Text, "nas-backup went down") {
		t.Fatalf("cause: %+v", oom.Cause)
	}
	roles := []string{}
	for _, e := range oom.Events {
		roles = append(roles, e.Role)
	}
	if strings.Join(roles, ",") != "problem,problem,action,recovery" {
		t.Fatalf("timeline roles: %v", roles)
	}
	if len(oom.Affected) != 2 || oom.Affected[1].Label != "nas-backup" || oom.Affected[1].Up == nil {
		t.Fatalf("affected: %+v", oom.Affected)
	}
	if oom.Peaks.Mem == nil || *oom.Peaks.Mem != 99.1 || len(oom.Series) != 2 {
		t.Fatalf("readings around it: %+v %+v", oom.Peaks, oom.Series)
	}
}

func TestCausesFromWhatCameBefore(t *testing.T) {
	cases := []struct {
		name  string
		notes []note
		kind  string
	}{
		{"the service behind the check crashed", []note{
			n(100, "services", "critical", "plexmediaserver failed", "", "unit:plexmediaserver.service"),
			n(130, "uptime", "critical", "plex is down", "connection refused", "check:plex"),
		}, "crash"},
		{"many checks at once", []note{
			n(100, "uptime", "critical", "a is down", "i/o timeout", "check:a"),
			n(110, "uptime", "critical", "b is down", "i/o timeout", "check:b"),
			n(140, "uptime", "critical", "c is down", "i/o timeout", "check:c"),
		}, "network"},
		{"an update just before", []note{
			n(100, "updates", "ok", "Radarr updated", "", ""),
			n(400, "uptime", "critical", "radarr-api is down", "HTTP 502", "check:radarr-api"),
		}, "change"},
		{"a full disk", []note{
			n(100, "storage", "critical", "/ is 99% full", "", ""),
			n(150, "uptime", "critical", "db is down", "", "check:db"),
		}, "disk"},
	}
	for _, c := range cases {
		list := incidents.Build(c.notes, nil, 1000, nil)
		if len(list) != 1 || list[0].Cause == nil || list[0].Cause.Kind != c.kind {
			t.Errorf("%s: want %s, got %+v", c.name, c.kind, list)
		}
	}
	// A change only counts as the lead-up when it came first.
	list := incidents.Build([]note{n(100, "updates", "ok", "Radarr updated", "", ""), n(400, "uptime", "critical", "x is down", "", "check:x")}, nil, 1000, nil)
	if list[0].Events[0].Role != "before" {
		t.Fatalf("the update is the lead-up: %+v", list[0].Events)
	}
}

func TestExplainForTheAlertBeingSent(t *testing.T) {
	notes := []note{n(100, "system", "critical", "Out of memory", "Killed process 9 (java)", "")}
	c := incidents.Explain(notes, nil, note{TS: 160, Area: "uptime", Severity: "critical", Title: "app is down", Subject: "check:app"}, nil)
	if c == nil || c.Kind != "memory" || !strings.Contains(c.Text, "java") {
		t.Fatalf("why: %+v", c)
	}
	if c := incidents.Explain(nil, nil, note{TS: 160, Area: "uptime", Severity: "critical", Title: "app is down", Subject: "check:app"}, nil); c != nil {
		t.Fatalf("nothing else happened, so there is nothing to add: %+v", c)
	}
}

func TestFeedbackTeaches(t *testing.T) {
	// plex goes down with nothing else on the machine to blame.
	alone := func() []note {
		return []note{n(100, "uptime", "critical", "plex is down", "timed out after 10s", "check:plex")}
	}
	if c := incidents.Build(alone(), nil, 1000, nil)[0].Cause; c.Kind == "disk" {
		t.Fatalf("no signs: %+v", c)
	}
	// Twice the owner said it was really the disk.
	fb := []incidents.Feedback{
		{Verdict: "wrong", Kind: "first", Actual: "disk", Subjects: []string{"check:plex"}},
		{Verdict: "wrong", Kind: "network", Actual: "disk", Subjects: []string{"check:plex"}},
	}
	c := incidents.Build(alone(), nil, 1000, incidents.Learn(fb))[0].Cause
	if c.Kind != "disk" || !c.Learned || !strings.Contains(c.Text, "plex going down has usually been a full disk") {
		t.Fatalf("learned: %+v", c)
	}
	// A cause the owner rejected gives way to the next.
	update := func() []note {
		return []note{n(100, "updates", "ok", "Plex updated", "", ""), n(400, "uptime", "critical", "plex is down", "timed out after 10s", "check:plex")}
	}
	if c := incidents.Build(update(), nil, 1000, nil)[0].Cause; c.Kind != "change" {
		t.Fatalf("before feedback: %+v", c)
	}
	rejected := incidents.Learn([]incidents.Feedback{{Verdict: "wrong", Kind: "change", Subjects: []string{"check:plex"}}, {Verdict: "wrong", Kind: "change", Subjects: []string{"check:plex"}}, {Verdict: "wrong", Kind: "change", Subjects: []string{"check:plex"}}})
	if c := incidents.Build(update(), nil, 1000, rejected)[0].Cause; c.Kind == "change" {
		t.Fatalf("a rejected cause stays first: %+v", c)
	}
}

func TestContainersAndMaintenance(t *testing.T) {
	notes := []note{
		n(100, "uptime", "critical", "sonarr-container is down", "container sonarr stopped 1 min ago: exit code 1", "check:sonarr-container"),
		n(130, "uptime", "critical", "sonarr is down", "connection refused", "check:sonarr"),
	}
	if c := incidents.Build(notes, nil, 1000, nil)[0].Cause; c.Kind != "crash" || !strings.Contains(c.Text, "The sonarr container stopped") || !strings.Contains(c.Text, "the sonarr check went down with it") {
		t.Fatalf("container crash: %+v", c)
	}
	planned := n(100, "uptime", "critical", "nas is down", "", "check:nas")
	planned.Status = "maintenance"
	if list := incidents.Build([]note{planned}, nil, 1000, nil); len(list) != 0 {
		t.Fatalf("an outage during maintenance is no incident: %+v", list)
	}
}

func TestDraftForVisitors(t *testing.T) {
	notes := []note{
		n(1000, "storage", "critical", "/ is 99% full", "", ""),
		n(1100, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(1150, "uptime", "info", "Nightjar repaired plex", "restarted plexmediaserver.service", "check:plex"),
		n(1520, "uptime", "ok", "plex is back up", "", "check:plex"),
	}
	inc := incidents.Build(notes, nil, 2000, nil)[0]
	d, ok := incidents.Draft(inc, map[string]string{"plex": "Movies & TV"}, 2000)
	if !ok || d.Title != "Movies & TV outage" || !strings.Contains(d.Text, "Movies & TV was unavailable for 9 minutes on") ||
		!strings.Contains(d.Text, "The cause was a disk on the server filled up.") || !strings.Contains(d.Text, "fixed automatically") {
		t.Fatalf("draft: %v %+v", ok, d)
	}
	if strings.Contains(d.Text, "plexmediaserver") || strings.Contains(d.Text, "99%") {
		t.Fatalf("nothing internal reaches visitors: %s", d.Text)
	}
	if _, ok := incidents.Draft(inc, map[string]string{"website": "Website"}, 2000); ok {
		t.Fatal("nothing on the page was affected, so there is nothing to tell")
	}
}

// A check that stays down past Join is re-alerted with growing gaps; its incident stays one and
// closes when it recovers, rather than leaving the first half open for ever.
func TestALongOutageClosesWhenItRecovers(t *testing.T) {
	notes := []note{
		n(0, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(300, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(900, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(2100, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(3000, "uptime", "ok", "plex is back up", "", "check:plex"),
	}
	list := incidents.Build(notes, nil, 10000, nil)
	if len(list) != 1 || list[0].Ended == nil || *list[0].Ended != 3000 || list[0].Started != 0 {
		t.Fatalf("one incident from 0 to the recovery at 3000, got %+v", list)
	}

	// Something else going wrong long after is its own incident; the first still ends when its
	// check recovers, even though that comes during the second.
	notes = []note{
		n(0, "uptime", "critical", "plex is down", "timed out", "check:plex"),
		n(5000, "services", "critical", "bazarr failed", "", "unit:bazarr.service"),
		n(5100, "uptime", "ok", "plex is back up", "", "check:plex"),
		n(5200, "services", "ok", "bazarr recovered", "", "unit:bazarr.service"),
	}
	list = incidents.Build(notes, nil, 10000, nil)
	if len(list) != 2 {
		t.Fatalf("want 2 incidents, got %+v", list)
	}
	bazarr, plex := list[0], list[1]
	if plex.Ended == nil || *plex.Ended != 5100 || bazarr.Ended == nil || *bazarr.Ended != 5200 {
		t.Fatalf("both incidents end with their recoveries: plex %+v bazarr %+v", plex.Ended, bazarr.Ended)
	}
	if roles := len(bazarr.Events); roles != 2 {
		t.Fatalf("plex's recovery belongs to plex's incident, bazarr's events: %+v", bazarr.Events)
	}
}

// Alerts with no recovery to follow (a drive warning, a check deleted while down) must not leave
// their incident ongoing for ever, nor pull in everything that happens afterwards.
func TestAnIncidentNothingRecoversFromClosesOnceQuiet(t *testing.T) {
	notes := []note{
		n(0, "storage", "warning", "sda is wearing out", "", "drive:sda"),
		n(600, "storage", "warning", "sda is wearing out", "", "drive:sda"),
		n(3*3600, "system", "info", "Updates installed", "", ""),
		n(incidents.Quiet+2*3600, "system", "info", "Nightjar restarted", "", ""),
	}
	list := incidents.Build(notes, nil, incidents.Quiet+3*3600, nil)
	if len(list) != 1 || list[0].Ended == nil || *list[0].Ended != 600 {
		t.Fatalf("one incident ending at its last warning, got %+v", list)
	}
	for _, e := range list[0].Events {
		if e.TS > 600+incidents.Join {
			t.Fatalf("a note long after it went quiet was taken in: %+v", list[0].Events)
		}
	}

	// Still quiet for less than Quiet at now: it stays ongoing.
	list = incidents.Build(notes[:2], nil, 600+incidents.Quiet-1, nil)
	if len(list) != 1 || list[0].Ended != nil {
		t.Fatalf("still within Quiet, want ongoing: %+v", list)
	}

	// A problem about it after a long silence starts a new incident instead of joining the old one.
	notes = append(notes[:2], n(600+incidents.Quiet+60, "storage", "warning", "sda is wearing out", "", "drive:sda"))
	list = incidents.Build(notes, nil, 600+incidents.Quiet+120, nil)
	if len(list) != 2 || list[1].Ended == nil || *list[1].Ended != 600 || list[0].Ended != nil {
		t.Fatalf("want the old incident closed and a new one ongoing, got %+v", list)
	}
}

func TestSavingOneEntryNeverLosesTheOthers(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "i.db"))
	t.Cleanup(db.Close)

	// Saves at the same moment each keep their own entry.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := incidents.SetNotice(db, int64(i), &incidents.Notice{ID: int64(i), Title: "x"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := len(incidents.AllNotices(db)); got != 20 {
		t.Fatalf("every concurrent save should be kept, got %d of 20", got)
	}

	// What cannot be read is not overwritten with the one entry being saved.
	db.SetMeta("incidents.feedback", "{not json")
	if err := incidents.SetFeedback(db, 1, &incidents.Feedback{Verdict: "right"}); err == nil {
		t.Fatal("saving over unreadable feedback should fail")
	}
	if raw := db.Meta("incidents.feedback"); raw != "{not json" {
		t.Fatalf("the stored feedback should be left alone, got %q", raw)
	}
}
