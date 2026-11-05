package incidents

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/storage"
)

// Feedback is what the owner said about an incident's cause: right or wrong, what it really was
// when they know, and a note. It is kept with the things the incident affected, so it teaches
// Nightjar about them.
type Feedback struct {
	Verdict  string   `json:"verdict"` // right or wrong
	Kind     string   `json:"kind"`    // the cause Nightjar gave
	Actual   string   `json:"actual,omitempty"`
	Note     string   `json:"note,omitempty"`
	Subjects []string `json:"subjects"`
	At       float64  `json:"at"`
}

// Kinds are the causes Nightjar names.
var Kinds = []string{"memory", "disk", "hardware", "restart", "crash", "network", "change", "overload", "check", "first"}

// habit is how a learned cause reads: "plex going down has usually been a full disk".
var habit = map[string]string{
	"memory": "memory running out", "disk": "a full disk", "hardware": "a hardware fault", "restart": "the machine restarting",
	"crash": "a crashed service or container", "network": "the network", "change": "a change made just before", "overload": "an overloaded processor",
}

// Learned is, per subject, how much the owner's feedback counts for each kind of cause.
type Learned map[string]map[string]int

// Learn tallies feedback: a confirmed cause counts two for its subjects, a rejected one two
// against, and the cause the owner named instead two for.
func Learn(all []Feedback) Learned {
	l := Learned{}
	bump := func(subject, kind string, n int) {
		if kind == "" || kind == "first" {
			return
		}
		if l[subject] == nil {
			l[subject] = map[string]int{}
		}
		l[subject][kind] += n
	}
	for _, f := range all {
		for _, s := range f.Subjects {
			if f.Verdict == "right" {
				bump(s, f.Kind, 2)
				continue
			}
			bump(s, f.Kind, -2)
			bump(s, f.Actual, 2)
		}
	}
	return l
}

// Validate tidies feedback from the dashboard.
func (f *Feedback) Validate() error {
	f.Note = strings.TrimSpace(f.Note)
	if f.Verdict != "right" && f.Verdict != "wrong" {
		return fmt.Errorf("the verdict must be right or wrong")
	}
	if f.Actual != "" && !slices.Contains(Kinds, f.Actual) {
		return fmt.Errorf("%q is not a kind of cause", f.Actual)
	}
	if f.Verdict == "right" {
		f.Actual = ""
	}
	if len(f.Note) > 2000 {
		return fmt.Errorf("the note is longer than 2000 characters")
	}
	return nil
}

// Subjects is what an incident affected, as feedback keeps it.
func (inc Incident) Subjects() []string {
	out := []string{}
	for _, a := range inc.Affected {
		if s := cmp.Or(a.Subject, a.Label); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Notice is an incident as visitors to the status page read it.
type Notice struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
	Started   float64  `json:"started"`
	Ended     *float64 `json:"ended"`
	Published bool     `json:"published"`
	Updated   float64  `json:"updated,omitempty"`
}

// Validate tidies a notice from the dashboard.
func (n *Notice) Validate() error {
	n.Title, n.Text = strings.TrimSpace(n.Title), strings.TrimSpace(n.Text)
	if n.Title == "" || len(n.Title) > 120 {
		return fmt.Errorf("a notice needs a title of up to 120 characters")
	}
	if len(n.Text) > 1000 {
		return fmt.Errorf("the text is longer than 1000 characters")
	}
	return nil
}

// plain is a cause in words for visitors, without anything internal.
var plain = map[string]string{
	"memory": "the server ran out of memory", "disk": "a disk on the server filled up", "hardware": "a hardware fault",
	"restart": "the server restarted", "crash": "a service on the server crashed", "network": "a network problem",
	"change": "a change made to the server", "overload": "the server was overloaded",
}

// Draft words an incident for the status page: the public names of the checks it affected that
// are on the page, how long, the cause in plain terms, and whether it is over. It is false when
// nothing on the page was affected.
func Draft(inc Incident, public map[string]string, now float64) (Notice, bool) {
	var names []string
	for _, a := range inc.Affected {
		if a.Kind != "check" {
			continue
		}
		if p, ok := public[a.Label]; ok && !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return Notice{}, false
	}
	who := names[0]
	switch {
	case len(names) == 2:
		who = names[0] + " and " + names[1]
	case len(names) > 2:
		who = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	verb := "was"
	if len(names) > 1 {
		verb = "were"
	}
	end := now
	if inc.Ended != nil {
		end = *inc.Ended
	}
	day := time.Unix(int64(inc.Started), 0).Format("Monday")
	n := Notice{ID: inc.ID, Started: inc.Started, Ended: inc.Ended}
	if inc.Ended == nil {
		n.Title = who + " unavailable"
		n.Text = fmt.Sprintf("%s %s unavailable from %s.", who, verb, time.Unix(int64(inc.Started), 0).Format("15:04"))
	} else {
		n.Title = who + " outage"
		n.Text = fmt.Sprintf("%s %s unavailable for %s on %s.", who, verb, words(end-inc.Started), day)
	}
	if inc.Cause != nil && plain[inc.Cause.Kind] != "" {
		n.Text += " The cause was " + plain[inc.Cause.Kind] + "."
	}
	repaired := slices.ContainsFunc(inc.Events, func(e Event) bool { return e.Role == "action" && e.Severity != "warning" })
	switch {
	case inc.Ended == nil:
		n.Text += " We are working on it."
	case repaired:
		n.Text += " It was fixed automatically."
	default:
		n.Text += " It is resolved."
	}
	return n, true
}

// words is a length of time for visitors: "7 minutes", "1 hour and 12 minutes".
func words(seconds float64) string {
	m := int(math.Round(seconds / 60))
	plural := func(n int, w string) string {
		if n == 1 {
			return "1 " + w
		}
		return fmt.Sprintf("%d %ss", n, w)
	}
	switch {
	case m < 1:
		return "under a minute"
	case m < 60:
		return plural(m, "minute")
	case m%60 == 0:
		return plural(m/60, "hour")
	}
	return plural(m/60, "hour") + " and " + plural(m%60, "minute")
}

// Feedback and notices are kept in the store's meta table, keyed by incident.
const (
	feedbackKey = "incidents.feedback"
	noticesKey  = "incidents.notices"
)

func load[T any](store *storage.Store, key string) (map[int64]T, error) {
	out := map[int64]T{}
	// A read that failed is not an empty list: keep would write back only its own entry.
	raw, err := store.MetaValue(key)
	if err != nil {
		return out, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return out, fmt.Errorf("the stored %s are unreadable: %w", strings.TrimPrefix(key, "incidents."), err)
		}
	}
	return out, nil
}

// keeping serializes keep: each save reads every entry and writes them all back, so two at once
// would lose one of them.
var keeping sync.Mutex

func keep[T any](store *storage.Store, key string, id int64, v *T) error {
	keeping.Lock()
	defer keeping.Unlock()
	all, err := load[T](store, key)
	if err != nil {
		// Writing back what could be read would wipe every other entry.
		return err
	}
	if v == nil {
		delete(all, id)
	} else {
		all[id] = *v
	}
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return store.SetMeta(key, string(data))
}

// AllFeedback is every incident's feedback, by incident.
func AllFeedback(store *storage.Store) map[int64]Feedback {
	all, _ := load[Feedback](store, feedbackKey)
	return all
}

// SetFeedback keeps (or, with nil, drops) an incident's feedback.
func SetFeedback(store *storage.Store, id int64, f *Feedback) error {
	return keep(store, feedbackKey, id, f)
}

// LearnedFrom is what the stored feedback teaches.
func LearnedFrom(store *storage.Store) Learned {
	return Learn(slices.Collect(maps.Values(AllFeedback(store))))
}

// AllNotices is every incident's status page notice, by incident.
func AllNotices(store *storage.Store) map[int64]Notice {
	all, _ := load[Notice](store, noticesKey)
	return all
}

// SetNotice keeps (or, with nil, drops) an incident's notice.
func SetNotice(store *storage.Store, id int64, n *Notice) error {
	return keep(store, noticesKey, id, n)
}
