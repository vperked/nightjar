package notify

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/storage"
)

// The hub sits between the monitors and the channels. Every alert is kept in the notification
// history, then routed by the rules: which channels hear about which area, at what severity. Quiet
// hours hold alerts back for one summary when they end, and a mute silences one check, service,
// drive or area for a while. Throttling (throttle.go) stops a flapping subject or an alert storm
// from flooding the channels.

// Area is a kind of alert, for routing, filtering and muting.
type Area struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Areas lists every area in the order the dashboard shows them.
var Areas = []Area{
	{"uptime", "Uptime checks"},
	{"services", "System services"},
	{"resources", "CPU, memory and network"},
	{"storage", "Disks and drives"},
	{"system", "Monitor and kernel"},
	{"updates", "Updates"},
	{"torrents", "qBittorrent"},
	{"media", "Plex"},
	{"servers", "Other servers"},
	{"attacks", "Attacks"},
}

func knownArea(id string) bool {
	return slices.ContainsFunc(Areas, func(a Area) bool { return a.ID == id })
}

// areaOf is where an alert belongs: its category when that names one, else the area of the route
// it was sent through.
func areaOf(route, category string) string {
	switch category {
	case "Uptime":
		return "uptime"
	case "Services":
		return "services"
	case "Disk", "Storage Monitoring", "Directory Monitoring", "Emergency":
		return "storage"
	case "Kernel", "Kernel Warning", "Kernel Stability", "Watchdog", "Error Alert", "Metrics Report", "Monitor":
		return "system"
	case "Updates":
		return "updates"
	case "qBittorrent":
		return "torrents"
	case "Servers":
		return "servers"
	case "Attacks":
		return "attacks"
	}
	return route
}

// Level is how much of an area a channel hears.
type Level string

const (
	All      Level = "all"
	Warnings Level = "warning" // warnings, criticals and recoveries
	Critical Level = "critical"
	Off      Level = "off"
)

// passes reports whether an alert of sev gets through. Recoveries pass whatever the level short of
// off, so a channel that heard about an outage also hears it is over.
func (l Level) passes(sev discord.Severity) bool {
	switch l {
	case Off:
		return false
	case Warnings:
		return sev == discord.OK || sev >= discord.Warning
	case Critical:
		return sev == discord.OK || sev >= discord.Critical
	}
	return true
}

// LevelFor turns ALERT_CHANNELS_MIN_SEVERITY into a level.
func LevelFor(minSeverity string) Level {
	switch strings.ToUpper(minSeverity) {
	case "WARNING":
		return Warnings
	case "CRITICAL":
		return Critical
	}
	return All
}

// Destination is one channel the hub routes to. Discord can use a different webhook per area.
type Destination struct {
	Channel Channel
	ByArea  map[string]Channel
	// Default is the level for an area that has no rule.
	Default func(area string) Level
}

func (d Destination) name() string {
	if d.Channel != nil {
		return d.Channel.Name()
	}
	for _, c := range d.ByArea {
		return c.Name()
	}
	return ""
}

// channel is where this destination delivers an area's alerts, nil when it has nowhere for them.
func (d Destination) channel(area string) Channel {
	if c := d.ByArea[area]; c != nil {
		return c
	}
	return d.Channel
}

func (d Destination) level(area string) Level {
	if d.Default == nil {
		return All
	}
	return d.Default(area)
}

// Quiet hours hold non-critical alerts back for one summary when they end.
type Quiet struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"` // "23:00", local time
	End     string `json:"end"`
	// Critical alerts are sent straight away when this is set.
	Critical bool `json:"critical"`
}

func clock(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// Active reports whether now falls in quiet hours. A window may run past midnight.
func (q Quiet) Active(now time.Time) bool {
	start, ok1 := clock(q.Start)
	end, ok2 := clock(q.End)
	if !q.Enabled || !ok1 || !ok2 || start == end {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

func (q Quiet) holds(now time.Time, sev discord.Severity) bool {
	return q.Active(now) && !(q.Critical && sev == discord.Critical)
}

// Mute silences one subject ("check:Plex", "unit:sonarr.service", "drive:S4EV…", "host:<id>", "area:torrents")
// until a time.
type Mute struct {
	Subject string  `json:"subject"`
	Label   string  `json:"label"`
	Until   float64 `json:"until"`
}

// Prefs are the hub's settings, kept in the database and changed from the dashboard without a restart.
type Prefs struct {
	// Rules maps an area to each channel's level; a missing entry uses the channel's default.
	Rules map[string]map[string]Level `json:"rules"`
	Quiet Quiet                       `json:"quiet"`
	Mutes []Mute                      `json:"mutes"`
}

const prefsKey = "notify.prefs"

// Keep is how long the notification history goes back.
const Keep = 90 * 24 * time.Hour

// Hub keeps, routes and delivers every alert. Its methods are safe for concurrent use.
type Hub struct {
	store *storage.Store
	dests []Destination
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Limits throttle delivery; tests may change them before the first alert.
	Limits Limits
	// Maintenance, when set, says whether planned downtime holds back an alert about subject:
	// such alerts are kept, marked maintenance, and sent nowhere.
	Maintenance func(subject string, now time.Time) bool
	// Waiting, when set, is called every few seconds while an alert waits on slow channels. The
	// monitoring loop sends alerts itself, so this tells its watchdog the loop is busy, not stuck: a
	// plugin or webhook that takes its full time must not look like a hung monitor.
	Waiting func()

	mu     sync.Mutex
	prefs  Prefs
	failed map[string]failure // an alert whose delivery failed everywhere → its record, so a retry updates it
	pruned time.Time
	// summaryTries counts quiet hours summaries that did not get through, which are retried a few times.
	summaryTries int

	subjects map[string]*subjectLog
	recent   []time.Time // deliveries in the last hour, for the rate limit
	// rateHeld is set while alerts are held for the rate limit rather than quiet hours.
	rateHeld      bool
	rateHeldSince time.Time
	lastSummary   time.Time
}

// NewHub routes to dests and keeps history in store (which may be unavailable: alerts are then
// delivered without being kept, and quiet hours send them straight away).
func NewHub(store *storage.Store, dests []Destination) *Hub {
	h := &Hub{store: store, dests: dests, Now: time.Now, Limits: DefaultLimits, failed: map[string]failure{}, subjects: map[string]*subjectLog{}}
	h.prefs = Prefs{Quiet: Quiet{Start: "23:00", End: "07:00", Critical: true}}
	if raw := store.Meta(prefsKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), &h.prefs); err != nil {
			log.Warn("Notification settings unreadable, using defaults", "err", err)
		}
	}
	return h
}

// Route is a sender for one area's alerts.
func (h *Hub) Route(area string) discord.Sender { return route{hub: h, area: area} }

type route struct {
	hub           *Hub
	area, subject string
}

func (r route) SendEmbed(title, description string, sev discord.Severity, fields []discord.Field, category string) bool {
	return r.hub.send(newAlert(title, description, sev, fields, category), areaOf(r.area, category), r.subject, true)
}

func (r route) About(subject string) discord.Sender {
	r.subject = subject
	return r
}

type target struct {
	name    string
	channel Channel
}

// targets are the channels an alert in area of sev goes to under prefs.
func (h *Hub) targets(p Prefs, area string, sev discord.Severity) []target {
	var out []target
	for _, d := range h.dests {
		c := d.channel(area)
		if c == nil {
			continue
		}
		level, ok := p.Rules[area][c.Name()]
		if !ok {
			level = d.level(area)
		}
		if level.passes(sev) {
			out = append(out, target{c.Name(), c})
		}
	}
	return out
}

// send keeps an alert and routes it; throttle applies the limits, which the hub's own messages skip.
func (h *Hub) send(a Alert, area, subject string, throttle bool) bool {
	fields, _ := json.Marshal(nonNilFields(a.Fields))
	rec := storage.Notification{
		TS: float64(a.Time.UnixMilli()) / 1000, Area: area, Severity: a.Severity.String(),
		Title: a.Title, Body: a.Description, Fields: fields, Subject: subject,
	}
	now := h.Now()
	h.mu.Lock()
	p := h.prefs
	muted := mutedBy(p.Mutes, now, subject, "area:"+area) != ""
	h.mu.Unlock()

	switch targets := h.targets(p, area, a.Severity); {
	case h.Maintenance != nil && h.Maintenance(subject, now):
		rec.Status = "maintenance"
	case muted:
		rec.Status = "muted"
	case len(targets) == 0:
		rec.Status = "kept"
	case p.Quiet.holds(now, a.Severity) && h.store.Available():
		rec.Status = "held"
		for _, t := range targets {
			rec.Channels = append(rec.Channels, t.name)
		}
	case !throttle:
		return h.deliver(rec, targets, a)
	default:
		h.mu.Lock()
		verdict := h.admit(a, area, subject, now)
		h.mu.Unlock()
		switch verdict {
		case admit:
			return h.deliver(rec, targets, a)
		case overRate:
			if h.store.Available() {
				rec.Status = "held"
				for _, t := range targets {
					rec.Channels = append(rec.Channels, t.name)
				}
				break
			}
			// Nowhere to hold it for the summary: keep it, unsent.
			rec.Status, rec.Error = "throttled", "too many alerts at once"
		case startFlap:
			rec.Status, rec.Error = "throttled", "paused while this flaps"
			h.store.AddNotification(rec)
			h.send(h.flapNotice(a), area, subject, false)
			return true
		case flapping:
			rec.Status, rec.Error = "throttled", "paused while this flaps"
		}
	}
	h.mu.Lock()
	h.forgetFailedLocked(rec, now)
	h.mu.Unlock()
	h.store.AddNotification(rec)
	return true
}

// RetryWindow is how long after a delivery that failed everywhere the same alert sent again counts
// as a retry of it, updating its record rather than adding one.
const RetryWindow = time.Hour

// failure is the record of a delivery that failed everywhere, and when.
type failure struct {
	id int64
	at time.Time
}

// deliver sends to every target at once and keeps the outcome. It succeeds if any delivery did; when
// none did, a caller that sends the same alert again within RetryWindow updates the same record.
func (h *Hub) deliver(rec storage.Notification, targets []target, a Alert) bool {
	ok := make([]bool, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A channel that can act on what an alert is about (the Discord bot's buttons) is told.
			if about, can := t.channel.(interface {
				SendAbout(subject, title, description string, sev discord.Severity, fields []discord.Field, footer string) bool
			}); can {
				ok[i] = about.SendAbout(rec.Subject, a.Title, a.Description, a.Severity, a.Fields, a.Category)
			} else {
				ok[i] = t.channel.SendEmbed(a.Title, a.Description, a.Severity, a.Fields, a.Category)
			}
		}()
	}
	h.wait(&wg)
	var problems []string
	for i, t := range targets {
		if ok[i] {
			rec.Channels = append(rec.Channels, t.name)
		} else {
			problems = append(problems, t.name+": "+cmp.Or(t.channel.LastError(), "not delivered"))
		}
	}
	rec.Status, rec.Error = "sent", strings.Join(problems, "; ")
	if len(rec.Channels) == 0 {
		rec.Status = "failed"
	}

	// Only the same alert sent again soon after is a retry; anything else is a new record.
	key := failedKey(rec)
	now := h.Now()
	h.mu.Lock()
	f, retry := h.failed[key]
	h.forgetFailedLocked(rec, now)
	h.mu.Unlock()
	id := f.id
	if retry && now.Sub(f.at) <= RetryWindow {
		h.store.SetNotificationStatus(id, rec.Status, rec.Channels, rec.Error)
	} else {
		id = h.store.AddNotification(rec)
	}
	if rec.Status == "failed" && id > 0 {
		h.mu.Lock()
		h.failed[key] = failure{id: id, at: now}
		h.mu.Unlock()
	}
	return rec.Status == "sent"
}

// failedKey identifies an alert for the retry link: the same area, subject, title and body.
func failedKey(rec storage.Notification) string {
	return strings.Join([]string{rec.Area, rec.Subject, rec.Title, rec.Body}, "\x00")
}

// forgetFailedLocked ends the retry link of every failed delivery that rec follows: its own, any
// other for the same subject (a recovery, or a different alert, means the next one is news, not a
// retry), and any older than RetryWindow. Without it a second outage within the hour would only
// update the first one's record and vanish from history.
func (h *Hub) forgetFailedLocked(rec storage.Notification, now time.Time) {
	prefix := rec.Area + "\x00" + rec.Subject + "\x00"
	for k, f := range h.failed {
		if now.Sub(f.at) > RetryWindow || (rec.Subject != "" && strings.HasPrefix(k, prefix)) || k == failedKey(rec) {
			delete(h.failed, k)
		}
	}
}

// WaitBeat is how often Waiting is called while an alert waits on its channels; tests shorten it.
var WaitBeat = 5 * time.Second

// wait waits for wg, calling Waiting every WaitBeat meanwhile.
func (h *Hub) wait(wg *sync.WaitGroup) {
	if h.Waiting == nil {
		wg.Wait()
		return
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	beat := time.NewTicker(WaitBeat)
	defer beat.Stop()
	for {
		select {
		case <-done:
			return
		case <-beat.C:
			h.Waiting()
		}
	}
}

// mutedBy returns the first of subjects muted at now, or "".
func mutedBy(mutes []Mute, now time.Time, subjects ...string) string {
	t := float64(now.Unix())
	for _, m := range mutes {
		if m.Until > t && slices.Contains(subjects, m.Subject) {
			return m.Subject
		}
	}
	return ""
}

// Tick sends the summary of held alerts once quiet hours are over, drops expired mutes and prunes
// old history. The monitoring loop calls it every cycle.
func (h *Hub) Tick() {
	now := h.Now()
	h.mu.Lock()
	p := h.prefs
	live := slices.DeleteFunc(slices.Clone(p.Mutes), func(m Mute) bool { return m.Until <= float64(now.Unix()) })
	expired := len(live) != len(p.Mutes)
	if expired {
		h.prefs.Mutes = live
	}
	prune := now.Sub(h.pruned) > time.Hour
	if prune {
		h.pruned = now
	}
	h.mu.Unlock()
	if expired {
		h.save()
	}
	if prune {
		h.store.PruneNotifications(float64(now.Add(-Keep).Unix()))
	}
	for _, s := range h.settled(now) {
		h.send(s.alert, s.area, s.subject, false)
	}
	if !p.Quiet.Active(now) && h.summaryDue(now) {
		h.sendSummary(p.Quiet)
	}
}

// summaryDue holds back the summary of alerts held for the rate limit until SummaryGap has passed
// since the hold began and since the last summary, so the summary cannot become a flood itself.
// Without a rate hold, quiet hours' summary goes as soon as they end.
func (h *Hub) summaryDue(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.rateHeld {
		return true
	}
	gap := h.Limits.SummaryGap
	return now.Sub(h.rateHeldSince) >= gap && now.Sub(h.lastSummary) >= gap
}

// sendSummary delivers what quiet hours held, as one message per channel.
func (h *Hub) sendSummary(q Quiet) {
	held := h.store.HeldNotifications()
	if len(held) == 0 {
		return
	}
	// Discord may have a webhook per area, so group by the channel each alert would have used.
	type batch struct {
		channel Channel
		alerts  []storage.Notification
	}
	var batches []*batch
	find := func(c Channel) *batch {
		for _, b := range batches {
			if b.channel == c {
				return b
			}
		}
		b := &batch{channel: c}
		batches = append(batches, b)
		return b
	}
	for _, n := range held {
		for _, d := range h.dests {
			if c := d.channel(n.Area); c != nil && slices.Contains(n.Channels, c.Name()) {
				find(c).alerts = append(find(c).alerts, n)
			}
		}
	}
	h.mu.Lock()
	rate := h.rateHeld
	h.lastSummary = h.Now()
	h.mu.Unlock()
	delivered := map[int64][]string{}
	for _, b := range batches {
		title, desc, sev := summary(b.alerts, q)
		if rate {
			title, desc, sev = h.rateSummary(b.alerts)
		}
		if b.channel.SendEmbed(title, desc, sev, nil, "Notifications") {
			for _, n := range b.alerts {
				delivered[n.ID] = append(delivered[n.ID], b.channel.Name())
			}
		}
	}
	// A channel that is down for a moment keeps its alerts held for the next tick, a few times over.
	h.mu.Lock()
	retry := len(delivered) < len(held) && h.summaryTries < summaryRetries
	if retry {
		h.summaryTries++
	} else {
		h.summaryTries, h.rateHeld = 0, false
	}
	h.mu.Unlock()
	for _, n := range held {
		if got := delivered[n.ID]; len(got) > 0 {
			h.store.SetNotificationStatus(n.ID, "digest", got, "")
		} else if !retry {
			h.store.SetNotificationStatus(n.ID, "failed", nil, "the quiet hours summary was not delivered")
		}
	}
}

// summaryRetries is how many more ticks try a quiet hours summary that was not delivered.
const summaryRetries = 5

// summary writes the quiet hours summary for alerts, oldest first.
func summary(alerts []storage.Notification, q Quiet) (string, string, discord.Severity) {
	const shown = 20
	sev := discord.Info
	var b strings.Builder
	fmt.Fprintf(&b, "Held from %s to %s.", q.Start, q.End)
	if q.Critical {
		b.WriteString(" Critical alerts were sent straight away.")
	}
	b.WriteString("\n")
	for i, n := range alerts {
		s := severityNamed(n.Severity)
		sev = max(sev, s)
		if i < shown {
			fmt.Fprintf(&b, "\n%s `%s` %s", s.Emoji(), time.Unix(int64(n.TS), 0).Format("15:04"), n.Title)
		}
	}
	if len(alerts) > shown {
		fmt.Fprintf(&b, "\n…and %d more in the dashboard's notifications.", len(alerts)-shown)
	}
	return fmt.Sprintf("Quiet hours summary: %d %s", len(alerts), plural(len(alerts), "alert", "alerts")), b.String(), sev
}

// rateSummary writes the summary of alerts held because too many arrived at once.
func (h *Hub) rateSummary(alerts []storage.Notification) (string, string, discord.Severity) {
	title, desc, sev := summary(alerts, Quiet{})
	_, list, _ := strings.Cut(desc, "\n")
	title = strings.Replace(title, "Quiet hours summary", "Alert summary", 1)
	return title, fmt.Sprintf("Held because more than %d alerts arrived in a minute, or %d in an hour.\n%s",
		h.Limits.PerMinute, h.Limits.PerHour, list), sev
}

func severityNamed(name string) discord.Severity {
	for _, s := range []discord.Severity{discord.Info, discord.OK, discord.Warning, discord.Critical} {
		if s.String() == name {
			return s
		}
	}
	return discord.Info
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func nonNilFields(f []discord.Field) []discord.Field {
	if f == nil {
		return []discord.Field{}
	}
	return f
}

// ---------- settings ----------

// Channels names every destination, in order.
func (h *Hub) Channels() []string {
	out := []string{}
	for _, d := range h.dests {
		if n := d.name(); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Rules returns the level of every area for every channel, and the defaults they start from.
// An area a channel has nowhere to send (Discord without a webhook for it) is left out.
func (h *Hub) Rules() (rules, defaults map[string]map[string]Level) {
	h.mu.Lock()
	p := h.prefs
	h.mu.Unlock()
	rules, defaults = map[string]map[string]Level{}, map[string]map[string]Level{}
	for _, a := range Areas {
		rules[a.ID], defaults[a.ID] = map[string]Level{}, map[string]Level{}
		for _, d := range h.dests {
			c := d.channel(a.ID)
			if c == nil {
				continue
			}
			defaults[a.ID][c.Name()] = d.level(a.ID)
			rules[a.ID][c.Name()] = cmp.Or(p.Rules[a.ID][c.Name()], d.level(a.ID))
		}
	}
	return rules, defaults
}

// Quiet returns the quiet hours settings.
func (h *Hub) Quiet() Quiet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prefs.Quiet
}

// QuietNow reports whether quiet hours are on right now, and how many alerts they hold.
func (h *Hub) QuietNow() (bool, int) {
	if !h.Quiet().Active(h.Now()) {
		return false, 0
	}
	return true, len(h.store.HeldNotifications())
}

// Mutes returns the mutes still in force.
func (h *Hub) Mutes() []Mute {
	now := float64(h.Now().Unix())
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Mute{}
	for _, m := range h.prefs.Mutes {
		if m.Until > now {
			out = append(out, m)
		}
	}
	return out
}

// SetRules replaces the routing rules and quiet hours.
func (h *Hub) SetRules(rules map[string]map[string]Level, q Quiet) error {
	for area, levels := range rules {
		if !knownArea(area) {
			return fmt.Errorf("unknown alert area %q", area)
		}
		for ch, l := range levels {
			if !slices.Contains(h.Channels(), ch) {
				return fmt.Errorf("unknown channel %q", ch)
			}
			if !slices.Contains([]Level{All, Warnings, Critical, Off}, l) {
				return fmt.Errorf("%s for %s must be all, warning, critical or off", ch, area)
			}
		}
	}
	if _, ok := clock(q.Start); !ok {
		return errors.New("quiet hours start must be a time like 23:00")
	}
	if _, ok := clock(q.End); !ok {
		return errors.New("quiet hours end must be a time like 07:00")
	}
	if q.Enabled && q.Start == q.End {
		return errors.New("quiet hours must start and end at different times")
	}
	h.mu.Lock()
	h.prefs.Rules, h.prefs.Quiet = rules, q
	h.mu.Unlock()
	return h.save()
}

// MaxMute is the longest a mute may last.
const MaxMute = 7 * 24 * time.Hour

// Mute silences a subject for d, or unmutes it when d is zero.
func (h *Hub) Mute(subject, label string, d time.Duration) error {
	kind, name, _ := strings.Cut(subject, ":")
	switch {
	case name == "" || len(subject) > 300 || !slices.Contains([]string{"check", "unit", "drive", "host", "area"}, kind):
		return errors.New("mute a check:, unit:, drive:, host: or area: subject")
	case kind == "area" && !knownArea(name):
		return fmt.Errorf("unknown alert area %q", name)
	case d < 0 || d > MaxMute:
		return errors.New("mute for up to a week")
	}
	h.mu.Lock()
	mutes := slices.DeleteFunc(slices.Clone(h.prefs.Mutes), func(m Mute) bool { return m.Subject == subject })
	if d > 0 {
		mutes = append(mutes, Mute{Subject: subject, Label: cmp.Or(strings.TrimSpace(label), name), Until: float64(h.Now().Add(d).Unix())})
	}
	h.prefs.Mutes = mutes
	h.mu.Unlock()
	return h.save()
}

func (h *Hub) save() error {
	h.mu.Lock()
	raw, err := json.Marshal(h.prefs)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	return h.store.SetMeta(prefsKey, string(raw))
}
