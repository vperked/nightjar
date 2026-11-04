package notify

import (
	"fmt"
	"time"

	"nightjar/internal/discord"
)

// Throttling keeps a flood of alerts off people's phones. Every alert is still kept in the history;
// only delivery is held back, in two ways:
//
//   - A subject (one check, service, drive, server, or one kind of alert) that sends FlapLimit alerts
//     within FlapWindow is flapping. One message says so; its later alerts are kept but not sent until
//     it has been quiet for SettleAfter, when one message says how it ended up.
//   - Across every subject at most PerMinute alerts go out a minute and PerHour an hour. The rest are
//     held, like quiet hours hold them, for one summary at most every SummaryGap.

// Limits are the hub's throttling thresholds; a zero field turns that limit off.
type Limits struct {
	FlapLimit   int
	FlapWindow  time.Duration
	SettleAfter time.Duration
	PerMinute   int
	PerHour     int
	SummaryGap  time.Duration
}

// DefaultLimits are generous enough that an ordinary outage and recovery always get through.
var DefaultLimits = Limits{
	FlapLimit:   6,
	FlapWindow:  30 * time.Minute,
	SettleAfter: 15 * time.Minute,
	PerMinute:   10,
	PerHour:     60,
	SummaryGap:  5 * time.Minute,
}

// subjectLog is what the hub remembers about one subject's recent alerts.
type subjectLog struct {
	sent     []time.Time // deliveries within FlapWindow
	flapping bool
	held     int // alerts kept back while flapping
	last     Alert
	area     string
	subject  string
	lastSeen time.Time
}

// admission is what the throttle decided for one alert.
type admission int

const (
	admit     admission = iota
	startFlap           // this alert made its subject flapping: keep it and say so once
	flapping            // its subject is flapping: keep it
	overRate            // too many alerts overall: hold it for the summary
)

func subjectKey(area, subject, title string) string {
	if subject != "" {
		return subject
	}
	return area + "\x00" + title
}

// since drops the times before cutoff from ts, which is oldest first.
func since(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// admit decides whether an alert about to be delivered may go out now. h.mu must be held.
func (h *Hub) admit(a Alert, area, subject string, now time.Time) admission {
	l := h.Limits
	key := subjectKey(area, subject, a.Title)
	sl := h.subjects[key]
	if sl == nil {
		sl = &subjectLog{}
		h.subjects[key] = sl
	}
	sl.last, sl.area, sl.subject, sl.lastSeen = a, area, subject, now
	if sl.flapping {
		sl.held++
		return flapping
	}
	sl.sent = since(sl.sent, now.Add(-l.FlapWindow))
	if l.FlapLimit > 0 && len(sl.sent) >= l.FlapLimit {
		sl.flapping, sl.held = true, 1
		return startFlap
	}
	h.recent = since(h.recent, now.Add(-time.Hour))
	inMinute := len(since(h.recent, now.Add(-time.Minute)))
	if (l.PerMinute > 0 && inMinute >= l.PerMinute) || (l.PerHour > 0 && len(h.recent) >= l.PerHour) {
		if !h.rateHeld && h.store.Available() {
			h.rateHeld, h.rateHeldSince = true, now
		}
		return overRate
	}
	sl.sent = append(sl.sent, now)
	h.recent = append(h.recent, now)
	return admit
}

// flapNotice says a subject's alerts are paused.
func (h *Hub) flapNotice(a Alert) Alert {
	l := h.Limits
	return newAlert("Alerts paused: "+a.Title,
		fmt.Sprintf("This sent %d alerts in %s, so its alerts are paused until it has been quiet for %s. "+
			"Then one message will say how it ended up. Every alert is still kept in the dashboard's notifications.",
			l.FlapLimit+1, minutes(l.FlapWindow), minutes(l.SettleAfter)),
		discord.Warning, a.Fields, a.Category)
}

// settled returns the subjects whose flapping has calmed down, as the message for each, and forgets
// subjects with nothing recent.
func (h *Hub) settled(now time.Time) (out []settledSubject) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, sl := range h.subjects {
		quiet := now.Sub(sl.lastSeen)
		switch {
		case sl.flapping && quiet >= h.Limits.SettleAfter:
			a := sl.last
			desc := fmt.Sprintf("Its alerts were paused while it flapped; %d %s kept in the dashboard's notifications. The latest, at %s:",
				sl.held, plural(sl.held, "was", "were"), sl.lastSeen.Format("15:04"))
			if a.Description != "" {
				desc += "\n\n" + a.Description
			}
			n := newAlert("Settled: "+a.Title, desc, a.Severity, a.Fields, a.Category)
			out = append(out, settledSubject{n, sl.area, sl.subject})
			delete(h.subjects, key)
		case !sl.flapping && quiet >= h.Limits.FlapWindow:
			delete(h.subjects, key)
		}
	}
	return out
}

type settledSubject struct {
	alert         Alert
	area, subject string
}

func minutes(d time.Duration) string {
	m := int(d.Minutes())
	return fmt.Sprintf("%d %s", m, plural(m, "minute", "minutes"))
}
