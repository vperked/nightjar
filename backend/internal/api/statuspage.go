package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/services"
	"nightjar/internal/statusproxy"
)

// The public status page is designed in the dashboard: a title and intro, an optional
// announcement, and groups of checks shown under names of the owner's choosing. Checks not in
// the design never appear. Without a design, every check is listed, as before. The owner may add
// their own CSS and HTML; the page takes scripts, frames and forms out of the HTML before showing it.

const designKey = "statuspage.design"

// StatusDesign is how the public status page looks.
type StatusDesign struct {
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	Announcement string        `json:"announcement"`
	Tone         string        `json:"tone"` // info, warning or crit
	ShowUptime   bool          `json:"show_uptime"`
	ShowHistory  bool          `json:"show_history"`
	Footer       string        `json:"footer"`
	Groups       []StatusGroup `json:"groups"`
	CSS          string        `json:"css"`         // styles the public page
	HeaderHTML   string        `json:"header_html"` // shown above the title
	FooterHTML   string        `json:"footer_html"` // shown above the footer
}

// How long the owner's own code may be.
const (
	maxCSS  = 50000
	maxHTML = 20000
)

// maxDesignBody is how large a design may be to save: more than the usual limit on a change, since
// the CSS and HTML alone may be 90,000 characters, and more in JSON, plus up to 30 groups of 200 checks.
const maxDesignBody = 2 << 20

// StatusGroup is a heading and the checks under it.
type StatusGroup struct {
	Name  string       `json:"name"`
	Items []StatusItem `json:"items"`
}

// StatusItem is one check, optionally under a public name.
type StatusItem struct {
	Check string `json:"check"`
	Label string `json:"label,omitempty"`
}

func (d *StatusDesign) validate() error {
	trim := func(p *string, limit int, what string) error {
		*p = strings.TrimSpace(*p)
		if len(*p) > limit {
			return fmt.Errorf("%s is longer than %d characters", what, limit)
		}
		return nil
	}
	for _, f := range []struct {
		p     *string
		limit int
		what  string
	}{{&d.Title, 80, "the title"}, {&d.Description, 600, "the intro"}, {&d.Announcement, 600, "the announcement"}, {&d.Footer, 200, "the footer"}} {
		if err := trim(f.p, f.limit, f.what); err != nil {
			return err
		}
	}
	// The owner's code keeps its own spacing; only its ends are trimmed.
	for _, f := range []struct {
		p     *string
		limit int
		what  string
	}{{&d.CSS, maxCSS, "the CSS"}, {&d.HeaderHTML, maxHTML, "the HTML at the top"}, {&d.FooterHTML, maxHTML, "the HTML at the bottom"}} {
		if err := trim(f.p, f.limit, f.what); err != nil {
			return err
		}
	}
	if d.Tone == "" {
		d.Tone = "info"
	}
	if !slices.Contains([]string{"info", "warning", "crit"}, d.Tone) {
		return errors.New("the announcement's tone must be info, warning or crit")
	}
	if len(d.Groups) > 30 {
		return errors.New("at most 30 groups")
	}
	seen := map[string]bool{}
	for gi := range d.Groups {
		g := &d.Groups[gi]
		if err := trim(&g.Name, 60, "a group name"); err != nil {
			return err
		}
		if len(g.Items) > 200 {
			return errors.New("at most 200 checks in a group")
		}
		for ii := range g.Items {
			it := &g.Items[ii]
			if err := trim(&it.Label, 60, "a public name"); err != nil {
				return err
			}
			if it.Check == "" || seen[it.Check] {
				return fmt.Errorf("%q is on the page twice", it.Check)
			}
			seen[it.Check] = true
		}
	}
	return nil
}

// design is the stored design, or nil when the page has never been designed.
func (s *Server) design() *StatusDesign {
	raw := s.cfg.Store.Meta(designKey)
	if raw == "" {
		return nil
	}
	var d StatusDesign
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil
	}
	return &d
}

// renameInDesign keeps the page pointing at a check after it is renamed in the dashboard.
func (s *Server) renameInDesign(oldName, newName string) {
	d := s.design()
	if d == nil || oldName == "" || oldName == newName {
		return
	}
	changed := false
	for gi := range d.Groups {
		for ii := range d.Groups[gi].Items {
			if d.Groups[gi].Items[ii].Check == oldName {
				d.Groups[gi].Items[ii].Check, changed = newName, true
			}
		}
	}
	if changed {
		if data, err := json.Marshal(d); err == nil {
			s.cfg.Store.SetMeta(designKey, string(data))
		}
	}
}

// statusPageDesign serves the design for the editor, with the checks there are to choose from.
func (s *Server) statusPageDesign(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	d := s.design()
	designed := d != nil
	if d == nil {
		d = &StatusDesign{Title: cfg.StatusPageTitle, ShowUptime: true, ShowHistory: true, Tone: "info", Groups: []StatusGroup{{Name: "", Items: []StatusItem{}}}}
		if s.ctx.Services != nil {
			for _, spec := range s.ctx.Services.Specs() {
				d.Groups[0].Items = append(d.Groups[0].Items, StatusItem{Check: spec.Name})
			}
		}
	}
	checks := []string{}
	if s.ctx.Services != nil {
		for _, spec := range s.ctx.Services.Specs() {
			checks = append(checks, spec.Name)
		}
	}
	// The days let the designer preview the page's history bars as visitors will see them.
	writeJSON(w, http.StatusOK, map[string]any{"enabled": cfg.StatusPageEnabled, "designed": designed, "design": d, "checks": checks, "days": s.checkDays()})
}

func (s *Server) saveStatusPageDesign(w http.ResponseWriter, r *http.Request) {
	var d StatusDesign
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody(fmt.Sprintf("The design is larger than %d MB; shorten its CSS, HTML or groups", maxDesignBody>>20)))
			return
		}
		writeJSON(w, http.StatusBadRequest, errBody("Expected a status page design"))
		return
	}
	if err := d.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	data, _ := json.Marshal(d)
	if err := s.cfg.Store.SetMeta(designKey, string(data)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"design": d})
}

// publicService is what the public status page may show: no targets, details or latency.
type publicService struct {
	Name      string       `json:"name"`
	Status    string       `json:"status"`
	Uptime24h *float64     `json:"uptime_24h"`
	Uptime30d *float64     `json:"uptime_30d"`
	Uptime90d *float64     `json:"uptime_90d"`
	Recent    []publicBeat `json:"recent"`
	Days      []publicDay  `json:"days"`
}

// publicDay is one day of a check: the share of its checks that passed (nil without any), and
// about how long it was down.
type publicDay struct {
	T      float64  `json:"t"` // local midnight
	Uptime *float64 `json:"uptime"`
	DownS  float64  `json:"down_s"`
	checks int      // how many checks ran, to weigh the day by
}

// statusDays is how many days the page shows.
const statusDays = 90

// dayCache keeps each check's days for a few minutes: visitors poll the page every 30 seconds, and
// days change slowly.
type dayCache struct {
	mu   sync.Mutex
	at   time.Time
	days map[string][]publicDay
}

const dayCacheTTL = 5 * time.Minute

// checkDays is every check's last statusDays days, by name, oldest first and with every day present.
func (s *Server) checkDays() map[string][]publicDay {
	c := &s.days
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.days != nil && time.Since(c.at) < dayCacheTTL {
		return c.days
	}
	out := map[string][]publicDay{}
	if s.ctx.Services != nil {
		_, offset := time.Now().Zone()
		today := (time.Now().Unix() + int64(offset)) / 86400
		for _, spec := range s.ctx.Services.Specs() {
			byDay := map[float64]publicDay{}
			for _, d := range s.cfg.Store.Days(spec.Name, statusDays, offset) {
				u := 100 * float64(d.Ups) / float64(d.Checks)
				byDay[d.Start] = publicDay{T: d.Start, Uptime: &u, DownS: float64(d.Checks-d.Ups) * spec.Interval, checks: d.Checks}
			}
			days := make([]publicDay, 0, statusDays)
			for i := int64(statusDays - 1); i >= 0; i-- {
				t := float64((today-i)*86400 - int64(offset))
				if d, ok := byDay[t]; ok {
					days = append(days, d)
				} else {
					days = append(days, publicDay{T: t})
				}
			}
			out[spec.Name] = days
		}
	}
	c.days, c.at = out, time.Now()
	return out
}

// uptimeOver is the share of all the days' checks that passed.
func uptimeOver(days []publicDay) *float64 {
	var up, all float64
	for _, d := range days {
		if d.Uptime == nil {
			continue
		}
		up += *d.Uptime * float64(d.checks)
		all += float64(d.checks)
	}
	if all == 0 {
		return nil
	}
	v := up / all
	return &v
}

type publicBeat struct {
	TS float64 `json:"ts"`
	Up bool    `json:"up"`
}

type publicGroup struct {
	Name     string          `json:"name"`
	Services []publicService `json:"services"`
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	if !cfg.StatusPageEnabled {
		writeJSON(w, http.StatusNotFound, errBody("Status page disabled"))
		return
	}
	rows, order := s.publicRows()
	d := s.design()
	if d == nil {
		d = everyCheck(order)
	}
	groups, all := d.lay(rows)
	body := map[string]any{
		"title": cmp.Or(d.Title, cfg.StatusPageTitle), "overall": overall(all), "updated": nowSeconds(),
		"description": d.Description, "footer": d.Footer, "show_uptime": d.ShowUptime, "show_history": d.ShowHistory,
		"groups": groups, "services": all,
	}
	for k, v := range map[string]string{"css": d.CSS, "header_html": d.HeaderHTML, "footer_html": d.FooterHTML} {
		if v != "" {
			body[k] = v
		}
	}
	if d.Announcement != "" {
		body["announcement"] = map[string]string{"text": d.Announcement, "tone": d.Tone}
	}
	// Planned maintenance, and the incidents the owner chose to tell visitors about.
	body["maintenance"], body["incidents"] = s.publicMaintenance(), s.publicNotices()
	writeJSON(w, http.StatusOK, body)
}

// publicRows is every check as the public may see it, by name, and the names in display order.
func (s *Server) publicRows() (map[string]publicService, []string) {
	rows := map[string]publicService{}
	var order []string
	if s.ctx.Services == nil {
		return rows, order
	}
	days := s.checkDays()
	for _, sum := range s.ctx.Services.Summaries(nowSeconds()) {
		row := publicService{Name: sum.Name, Status: sum.Status, Uptime24h: sum.Uptime24h, Uptime30d: sum.Uptime30d, Recent: []publicBeat{}, Days: days[sum.Name]}
		if row.Days == nil {
			row.Days = []publicDay{}
		}
		row.Uptime90d = uptimeOver(row.Days)
		for _, b := range sum.Recent {
			row.Recent = append(row.Recent, publicBeat{b.TS, b.Up})
		}
		rows[sum.Name], order = row, append(order, sum.Name)
	}
	return rows, order
}

// everyCheck is the page before it has been designed: every check, uptime and history shown.
func everyCheck(order []string) *StatusDesign {
	d := &StatusDesign{ShowUptime: true, ShowHistory: true, Groups: []StatusGroup{{}}}
	for _, name := range order {
		d.Groups[0].Items = append(d.Groups[0].Items, StatusItem{Check: name})
	}
	return d
}

// lay puts the checks into the design's groups under their public names, hiding what it says to
// hide. Checks deleted since the page was designed are skipped, and so are empty groups.
func (d *StatusDesign) lay(rows map[string]publicService) (groups []publicGroup, all []publicService) {
	groups, all = []publicGroup{}, []publicService{}
	for _, g := range d.Groups {
		pg := publicGroup{Name: g.Name, Services: []publicService{}}
		for _, it := range g.Items {
			row, ok := rows[it.Check]
			if !ok {
				continue
			}
			row.Name = cmp.Or(it.Label, row.Name)
			if !d.ShowHistory {
				row.Recent, row.Days = []publicBeat{}, []publicDay{}
			}
			if !d.ShowUptime {
				row.Uptime24h, row.Uptime30d, row.Uptime90d = nil, nil, nil
			}
			pg.Services = append(pg.Services, row)
			all = append(all, row)
		}
		if len(pg.Services) > 0 {
			groups = append(groups, pg)
		}
	}
	return groups, all
}

// overall is the page's verdict. Paused checks say nothing about how things are going.
func overall(rows []publicService) string {
	watched, down := 0, 0
	for _, r := range rows {
		if r.Status == services.StatusPaused {
			continue
		}
		watched++
		if r.Status == services.StatusDown {
			down++
		}
	}
	switch {
	case down > 0 && down == watched:
		return "outage"
	case down > 0:
		return "degraded"
	}
	return "operational"
}

// statusProxyOptions are the nginx set-up for the status page's domain as saved in the settings.
func (s *Server) statusProxyOptions(email string) statusproxy.Options {
	cfg := s.ctx.Config()
	o := statusproxy.Options{Domain: cfg.StatusPageDomain, Port: cfg.WebPort, Email: email}
	if s.cfg.StatusProxy != nil {
		s.cfg.StatusProxy(&o)
	}
	return o
}

// statusProxy says what is set up in nginx for the status page's domain, with the server block to
// use by hand instead.
func (s *Server) statusProxy(w http.ResponseWriter, r *http.Request) {
	o := s.statusProxyOptions("")
	if o.Domain == "" {
		writeJSON(w, http.StatusOK, map[string]any{"domain": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domain": o.Domain, "state": statusproxy.Inspect(o)})
}

// setUpStatusProxy puts the status page's domain into nginx and gets it a certificate.
func (s *Server) setUpStatusProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body"))
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email != "" && (len(body.Email) > 254 || !strings.Contains(body.Email, "@") || strings.ContainsAny(body.Email, " \t\n'\"")) {
		writeJSON(w, http.StatusBadRequest, errBody("that is not an email address"))
		return
	}
	o := s.statusProxyOptions(body.Email)
	if o.Domain == "" {
		writeJSON(w, http.StatusBadRequest, errBody("set the status page's domain first"))
		return
	}
	steps, err := statusproxy.Install(context.WithoutCancel(r.Context()), o)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "steps": steps, "state": statusproxy.Inspect(o)})
}

// removeStatusProxy takes the status page's site out of nginx.
func (s *Server) removeStatusProxy(w http.ResponseWriter, r *http.Request) {
	o := s.statusProxyOptions("")
	if o.Domain == "" {
		writeJSON(w, http.StatusBadRequest, errBody("no status page domain is set"))
		return
	}
	steps, err := statusproxy.Remove(context.WithoutCancel(r.Context()), o)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "steps": steps, "state": statusproxy.Inspect(o)})
}
