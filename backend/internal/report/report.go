// Package report sums up a week for Discord, on Monday mornings unless set otherwise: uptime,
// the incidents and their causes, what Nightjar repaired, what made the most noise, and what is
// coming (a disk filling, a certificate expiring, a heartbeat that stopped).
package report

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"nightjar/internal/analytics"
	"nightjar/internal/discord"
	"nightjar/internal/incidents"
	"nightjar/internal/logx"
	"nightjar/internal/monitor"
	"nightjar/internal/plexstats"
)

var log = logx.Component("Report")

// Report is one week.
type Report struct {
	From      float64        `json:"from"`
	To        float64        `json:"to"`
	Checks    []CheckLine    `json:"checks"` // worst first
	Overall   *float64       `json:"overall"`
	Incidents int            `json:"incidents"`
	Open      int            `json:"open"`
	Down      float64        `json:"down"` // seconds in incidents
	Causes    []Count        `json:"causes"`
	Top       []IncidentLine `json:"top"`
	Repairs   int            `json:"repairs"`
	Fixed     int            `json:"fixed"`
	Alerts    int            `json:"alerts"`
	Noisiest  []Count        `json:"noisiest"`
	Forecasts []string       `json:"forecasts"`
	// Plex is the week on Plex, when Nightjar watches it.
	Plex *plexstats.Stats `json:"plex,omitempty"`
}

type CheckLine struct {
	Name   string   `json:"name"`
	Uptime *float64 `json:"uptime"`
}

type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

type IncidentLine struct {
	ID      int64   `json:"id"`
	Title   string  `json:"title"`
	Cause   string  `json:"cause"`
	Started float64 `json:"started"`
	Length  float64 `json:"length"`
	Open    bool    `json:"open"`
}

// kindWords names a cause in the report.
var kindWords = map[string]string{
	"memory": "out of memory", "disk": "disk full", "hardware": "hardware", "restart": "machine restarted", "crash": "crashed service",
	"network": "network", "change": "after a change", "overload": "overloaded", "check": "the check's own error",
}

var tlsDaysRE = regexp.MustCompile(`valid for (\d+) day`)

// Make sums up the week before now.
func Make(c *monitor.Context, now time.Time) Report {
	to := float64(now.Unix())
	from := to - 7*86400
	r := Report{From: from, To: to, Checks: []CheckLine{}, Causes: []Count{}, Top: []IncidentLine{}, Noisiest: []Count{}, Forecasts: []string{}}

	if c.Services != nil {
		var sum float64
		var n int
		for _, s := range c.Services.Summaries(to) {
			if s.Status == "paused" {
				continue
			}
			r.Checks = append(r.Checks, CheckLine{Name: s.Name, Uptime: s.Uptime7d})
			if s.Uptime7d != nil {
				sum += *s.Uptime7d
				n++
			}
			switch {
			case s.Type == "tls":
				if m := tlsDaysRE.FindStringSubmatch(s.Detail); m != nil {
					if d, _ := strconv.Atoi(m[1]); d <= 30 {
						r.Forecasts = append(r.Forecasts, fmt.Sprintf("The certificate for %s expires in %d days", s.Name, d))
					}
				}
			case s.Type == "heartbeat" && s.Status == "down":
				r.Forecasts = append(r.Forecasts, fmt.Sprintf("%s is not reporting in: %s", s.Name, s.Detail))
			}
		}
		if n > 0 {
			avg := sum / float64(n)
			r.Overall = &avg
		}
		slices.SortStableFunc(r.Checks, func(a, b CheckLine) int { return cmp.Compare(or100(a.Uptime), or100(b.Uptime)) })
	}

	notes := c.Store.NotificationsBetween(from-incidents.Lead, to, 20000)
	samples := c.Store.SampleBuckets(analytics.Local, from-incidents.Lead-1800, 60)
	causes := map[string]int{}
	for _, inc := range incidents.Build(notes, samples, to, incidents.LearnedFrom(c.Store)) {
		if inc.Started < from {
			continue
		}
		r.Incidents++
		end := to
		if inc.Ended == nil {
			r.Open++
		} else {
			end = *inc.Ended
		}
		r.Down += end - inc.Started
		line := IncidentLine{ID: inc.ID, Title: inc.Title, Started: inc.Started, Length: end - inc.Started, Open: inc.Ended == nil}
		if inc.Cause != nil && inc.Cause.Kind != "first" {
			causes[inc.Cause.Kind]++
			line.Cause = inc.Cause.Text
		}
		r.Top = append(r.Top, line)
	}
	slices.SortStableFunc(r.Top, func(a, b IncidentLine) int { return cmp.Compare(b.Length, a.Length) })
	r.Top = r.Top[:min(3, len(r.Top))]
	r.Causes = counted(causes, func(k string) string { return kindWords[k] })

	noise := map[string]int{}
	for _, n := range notes {
		if n.TS >= from && (n.Severity == "warning" || n.Severity == "critical") {
			r.Alerts++
			label, _ := incidents.Label(n.Subject)
			noise[cmp.Or(label, n.Title)]++
		}
	}
	r.Noisiest = counted(noise, nil)
	r.Noisiest = r.Noisiest[:min(3, len(r.Noisiest))]

	for _, rep := range c.Store.Repairs("", 500) {
		if rep.TS >= from {
			r.Repairs++
			if rep.Fixed {
				r.Fixed++
			}
		}
	}

	if c.Plex != nil {
		st := plexstats.Make(c.Store, c.Plex, 7, now)
		r.Plex = &st
	}

	if f := diskForecast(c, to); f != "" {
		r.Forecasts = append([]string{f}, r.Forecasts...)
	}
	return r
}

func or100(p *float64) float64 {
	if p == nil {
		return 100
	}
	return *p
}

func counted(m map[string]int, name func(string) string) []Count {
	out := []Count{}
	for k, n := range m {
		if name != nil {
			k = cmp.Or(name(k), k)
		}
		out = append(out, Count{k, n})
	}
	slices.SortFunc(out, func(a, b Count) int { return cmp.Or(b.N-a.N, strings.Compare(a.Name, b.Name)) })
	return out
}

// diskForecast fits a line to the last two weeks of disk use and says when it reaches full, if
// that is within three months.
func diskForecast(c *monitor.Context, to float64) string {
	var xs, ys []float64
	for _, s := range c.Store.SampleBuckets(analytics.Local, to-14*86400, 6*3600) {
		if s.Disk != nil {
			xs = append(xs, (s.TS-to)/86400)
			ys = append(ys, *s.Disk)
		}
	}
	if len(xs) < 8 {
		return ""
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(xs))
	var num, den float64
	for i := range xs {
		num += (xs[i] - mx) * (ys[i] - my)
		den += (xs[i] - mx) * (xs[i] - mx)
	}
	if den == 0 {
		return ""
	}
	slope := num / den // percent a day
	last := ys[len(ys)-1]
	if slope < 0.05 || last >= 100 {
		return ""
	}
	days := (100 - last) / slope
	if days > 90 {
		return ""
	}
	return fmt.Sprintf("The disk is %.0f%% full and growing %.1f%% a day: full in about %d days", last, slope, int(math.Round(days)))
}

// Send posts the report to Discord (and wherever system alerts go).
func Send(c *monitor.Context, r Report) bool {
	if c.Webhook == nil {
		return false
	}
	day := func(t float64) string { return time.Unix(int64(t), 0).Format("Mon 2 Jan") }
	title := fmt.Sprintf("Your week: %s", plural(r.Incidents, "incident"))
	if r.Overall != nil {
		title += fmt.Sprintf(", %.2f%% uptime", *r.Overall)
	}
	desc := day(r.From) + " to " + day(r.To) + "."
	switch {
	case r.Incidents == 0:
		desc += " Nothing went wrong."
	case r.Open > 0:
		desc += fmt.Sprintf(" %s still open.", plural(r.Open, "incident"))
	}
	var fields []discord.Field
	field := func(name, value string, inline bool) {
		fields = append(fields, discord.Field{Name: name, Value: value, Inline: inline})
	}
	if len(r.Checks) > 0 {
		var lines []string
		for _, ch := range r.Checks[:min(4, len(r.Checks))] {
			if ch.Uptime != nil && *ch.Uptime < 100 {
				lines = append(lines, fmt.Sprintf("%s · %.2f%%", ch.Name, *ch.Uptime))
			}
		}
		if len(lines) == 0 {
			lines = []string{fmt.Sprintf("All %d checks were up all week", len(r.Checks))}
		}
		field("Lowest uptime", discord.List(lines), false)
	}
	if len(r.Top) > 0 {
		var lines []string
		for _, inc := range r.Top {
			line := fmt.Sprintf("**%s**, %s, %s", inc.Title, day(inc.Started), discord.Span(inc.Length))
			if inc.Cause != "" {
				line += ": " + inc.Cause
			}
			lines = append(lines, line)
		}
		field("Longest incidents", discord.List(lines), false)
	}
	if len(r.Causes) > 0 {
		var parts []string
		for _, c := range r.Causes {
			parts = append(parts, fmt.Sprintf("%s ×%d", c.Name, c.N))
		}
		field("Causes", strings.Join(parts, " · "), true)
	}
	if r.Repairs > 0 {
		field("Nightjar repaired", fmt.Sprintf("%d of %s worked", r.Fixed, plural(r.Repairs, "attempt")), true)
	}
	if len(r.Noisiest) > 0 && r.Noisiest[0].N >= 3 {
		field("Most alerts", fmt.Sprintf("%s · %d", r.Noisiest[0].Name, r.Noisiest[0].N), true)
	}
	if r.Plex != nil {
		if line := r.Plex.Line(); line != "" {
			field("Plex", line, false)
		}
	}
	if len(r.Forecasts) > 0 {
		field("Coming up", discord.List(r.Forecasts), false)
	}
	return discord.About(c.Webhook, "report").SendEmbed(title, desc, discord.Info, fields, "Monitor")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

const sentKey = "report.sent"

// When is a weekday and time, "Mon 09:00".
type When struct {
	Day    time.Weekday
	Minute int
}

// ParseWhen reads "Mon 09:00".
func ParseWhen(s string) (When, error) {
	day, clock, ok := strings.Cut(strings.TrimSpace(s), " ")
	days := map[string]time.Weekday{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	d, known := days[strings.ToLower(day[:min(3, len(day))])]
	var h, m int
	if !ok || !known || len(clock) != 5 || clock[2] != ':' {
		return When{}, fmt.Errorf(`expected a day and a time, such as "Mon 09:00"`)
	}
	if _, err := fmt.Sscanf(clock, "%d:%d", &h, &m); err != nil || h > 23 || m > 59 {
		return When{}, fmt.Errorf(`expected a day and a time, such as "Mon 09:00"`)
	}
	return When{d, h*60 + m}, nil
}

// Last is the latest time at or before now that the report is due.
func (w When) Last(now time.Time) time.Time {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	back := (int(now.Weekday()) - int(w.Day) + 7) % 7
	t := midnight.AddDate(0, 0, -back).Add(time.Duration(w.Minute) * time.Minute)
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

// Next is the first time after now that the report is due.
func (w When) Next(now time.Time) time.Time { return w.Last(now).AddDate(0, 0, 7) }

// Schedule sends the report when it is due, until stop closes. A report missed by more than a
// day (the machine was off) is skipped rather than sent late.
func Schedule(stop <-chan struct{}, c *monitor.Context) {
	if c.Store.Meta(sentKey) == "" {
		// A new install starts counting now, so its first report covers a whole week.
		c.Store.SetMeta(sentKey, strconv.FormatInt(time.Now().Unix(), 10))
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		cfg := c.Config()
		if !cfg.WeeklyReport {
			continue
		}
		when, err := ParseWhen(cfg.WeeklyReportAt)
		if err != nil {
			continue
		}
		now := time.Now()
		due := when.Last(now)
		sent, _ := strconv.ParseInt(c.Store.Meta(sentKey), 10, 64)
		if sent >= due.Unix() || now.Sub(due) > 24*time.Hour {
			continue
		}
		c.Store.SetMeta(sentKey, strconv.FormatInt(now.Unix(), 10))
		if Send(c, Make(c, now)) {
			log.Info("Weekly report sent")
		}
	}
}
