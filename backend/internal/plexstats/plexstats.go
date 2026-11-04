// Package plexstats sums up Plex over some days: what was played and by whom, how long, how many
// at once, how much was transcoded or watched from outside, what Plex's own history says was
// watched, and what was added to the libraries. Nightjar keeps every finished stream it sees;
// Plex's history also covers what played while Nightjar was not running.
package plexstats

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"nightjar/internal/plex"
	"nightjar/internal/storage"
)

// Count is a name and how often, with the hours behind it when known.
type Count struct {
	Name  string  `json:"name"`
	N     int     `json:"n"`
	Hours float64 `json:"hours,omitempty"`
}

// Added is how much a library grew.
type Added struct {
	Library string `json:"library"`
	Items   int    `json:"items"`
	Label   string `json:"label"` // movies, shows, episodes, tracks…
}

// Stats is Plex over some days.
type Stats struct {
	Days  int     `json:"days"`
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Plays int     `json:"plays"`
	Hours float64 `json:"hours"`
	// Users and Titles are the most played, by streams and hours; Players the devices.
	Users   []Count `json:"users"`
	Titles  []Count `json:"titles"`
	Players []Count `json:"players"`
	// Peak is the most streams at once, and when.
	Peak   int     `json:"peak"`
	PeakAt float64 `json:"peak_at"`
	// The shares of streams transcoded, and watched from outside the home network.
	Transcoded *float64 `json:"transcoded"`
	Remote     *float64 `json:"remote"`
	// Why streams were converted, most common first.
	Reasons []Count `json:"reasons"`
	// Days of the week by hours watched, Sunday first.
	Weekdays [7]float64 `json:"weekdays"`
	// From Plex's own history: what was watched to the end, and by whom.
	Watched  int     `json:"watched"`
	Viewers  []Count `json:"viewers"`
	Finished []Count `json:"finished"`
	History  string  `json:"history,omitempty"` // why the history could not be read
	Added    []Added `json:"added"`
}

const librariesKey = "plex.libraries"

// shelf is one day's library sizes.
type shelf struct {
	Day       string         `json:"day"`
	Libraries []plex.Library `json:"libraries"`
}

// RecordLibraries keeps one count of the libraries a day, for about two months, so the
// stats can say what was added.
func RecordLibraries(store *storage.Store, libs []plex.Library, now time.Time) {
	if len(libs) == 0 {
		return
	}
	var all []shelf
	json.Unmarshal([]byte(store.Meta(librariesKey)), &all)
	day := now.Format("2006-01-02")
	if n := len(all); n > 0 && all[n-1].Day == day {
		all[n-1].Libraries = libs
	} else {
		all = append(all, shelf{day, libs})
	}
	if len(all) > 62 {
		all = all[len(all)-62:]
	}
	data, _ := json.Marshal(all)
	store.SetMeta(librariesKey, string(data))
}

// added compares the libraries now with the earliest count since `since`.
func added(store *storage.Store, now []plex.Library, since time.Time) []Added {
	out := []Added{}
	var all []shelf
	json.Unmarshal([]byte(store.Meta(librariesKey)), &all)
	day := since.Format("2006-01-02")
	i := slices.IndexFunc(all, func(s shelf) bool { return s.Day >= day })
	if i < 0 || len(now) == 0 {
		return out
	}
	then := map[string]plex.Library{}
	for _, l := range all[i].Libraries {
		then[l.Key] = l
	}
	labels := map[string]string{"movie": "movies", "show": "shows", "artist": "artists", "photo": "photos"}
	for _, l := range now {
		was, ok := then[l.Key]
		if !ok {
			continue
		}
		// A show library grows by episodes, music by tracks: say the finer count when it has one.
		if l.ChildCount > was.ChildCount && l.ChildLabel != "" {
			out = append(out, Added{l.Title, l.ChildCount - was.ChildCount, l.ChildLabel})
		} else if l.Count > was.Count {
			out = append(out, Added{l.Title, l.Count - was.Count, cmp.Or(labels[l.Type], "items")})
		}
	}
	slices.SortFunc(out, func(a, b Added) int { return cmp.Or(b.Items-a.Items, strings.Compare(a.Library, b.Library)) })
	return out
}

// Make sums up the last days.
func Make(store *storage.Store, mon *plex.Monitor, days int, now time.Time) Stats {
	to := float64(now.Unix())
	from := to - float64(days)*86400
	st := Stats{Days: days, From: from, To: to, Users: []Count{}, Titles: []Count{}, Players: []Count{}, Reasons: []Count{}, Viewers: []Count{}, Finished: []Count{}, Added: []Added{}}

	plays := store.PlexPlays(from)
	// What is playing now counts too, up to now.
	if mon != nil {
		for _, s := range mon.Active() {
			plays = append(plays, storage.PlexPlay{Started: s.StartedAt, Ended: to, User: s.User, Kind: s.Kind, Title: s.Title, Subtitle: s.Subtitle,
				Player: s.Player, Decision: s.Decision, Local: s.Local, DurationMS: s.DurationMS, Reason: s.Reason})
		}
	}
	users, titles, players, reasons := map[string]*Count{}, map[string]*Count{}, map[string]*Count{}, map[string]*Count{}
	tally := func(m map[string]*Count, name string, hours float64) {
		if name == "" {
			return
		}
		c := m[name]
		if c == nil {
			c = &Count{Name: name}
			m[name] = c
		}
		c.N++
		c.Hours += hours
	}
	var transcoded, remote int
	type edge struct {
		t     float64
		delta int
	}
	var edges []edge
	for _, p := range plays {
		length := p.Ended - p.Started
		// A stream left paused for hours is not hours of watching: count no more than its running time.
		if p.DurationMS > 0 {
			length = min(length, float64(p.DurationMS)/1000)
		}
		if length < 60 {
			continue
		}
		hours := length / 3600
		st.Plays++
		st.Hours += hours
		tally(users, p.User, hours)
		// An episode's title is its show's, so a show counts as one.
		tally(titles, p.Title, hours)
		tally(players, p.Player, hours)
		if p.Decision == plex.Transcode {
			transcoded++
		}
		if p.Reason != "" {
			tally(reasons, p.Reason, hours)
		}
		if !p.Local {
			remote++
		}
		st.Weekdays[time.Unix(int64(p.Started), 0).Weekday()] += hours
		edges = append(edges, edge{p.Started, 1}, edge{p.Ended, -1})
	}
	if st.Plays > 0 {
		t, r := float64(transcoded)/float64(st.Plays)*100, float64(remote)/float64(st.Plays)*100
		st.Transcoded, st.Remote = &t, &r
	}
	// The most at once: walk the starts and ends in order, ends first at the same moment.
	slices.SortFunc(edges, func(a, b edge) int { return cmp.Or(cmp.Compare(a.t, b.t), cmp.Compare(a.delta, b.delta)) })
	n := 0
	for _, e := range edges {
		n += e.delta
		if n > st.Peak {
			st.Peak, st.PeakAt = n, e.t
		}
	}
	st.Users, st.Titles, st.Players, st.Reasons = top(users, 5), top(titles, 5), top(players, 3), top(reasons, 4)

	if mon != nil {
		viewed, err := mon.History(from)
		if err != nil {
			st.History = err.Error()
		}
		viewers, finished := map[string]*Count{}, map[string]*Count{}
		for _, v := range viewed {
			st.Watched++
			tally(viewers, v.User, 0)
			tally(finished, cmp.Or(v.Show, v.Title), 0)
		}
		st.Viewers, st.Finished = top(viewers, 5), top(finished, 5)
		st.Added = added(store, mon.Libraries(), now.AddDate(0, 0, -days))
	}
	return st
}

func top(m map[string]*Count, n int) []Count {
	out := make([]Count, 0, len(m))
	for _, c := range m {
		c.Hours = float64(int(c.Hours*10+0.5)) / 10
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Count) int {
		return cmp.Or(cmp.Compare(b.Hours, a.Hours), b.N-a.N, strings.Compare(a.Name, b.Name))
	})
	return out[:min(n, len(out))]
}

// Line is the stats in one line for the weekly report: "42 streams, 31 hours · mostly sam ·
// 4 at once on Sat · 12% transcoded · added 14 episodes".
func (st Stats) Line() string {
	if st.Plays == 0 && st.Watched == 0 {
		return ""
	}
	var parts []string
	if st.Plays > 0 {
		parts = append(parts, fmt.Sprintf("%d %s, %.0f hours", st.Plays, plural(st.Plays, "stream"), st.Hours))
	} else {
		parts = append(parts, fmt.Sprintf("%d watched", st.Watched))
	}
	if len(st.Users) > 1 {
		parts = append(parts, "mostly "+st.Users[0].Name)
	}
	if len(st.Titles) > 0 {
		parts = append(parts, "top: "+st.Titles[0].Name)
	}
	if st.Peak > 1 {
		parts = append(parts, fmt.Sprintf("%d at once on %s", st.Peak, time.Unix(int64(st.PeakAt), 0).Format("Mon 15:04")))
	}
	if st.Transcoded != nil && *st.Transcoded >= 1 {
		parts = append(parts, fmt.Sprintf("%.0f%% transcoded", *st.Transcoded))
	}
	var add []string
	for _, a := range st.Added {
		add = append(add, fmt.Sprintf("%d %s", a.Items, a.Label))
	}
	if len(add) > 0 {
		parts = append(parts, "added "+strings.Join(add, ", "))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}
