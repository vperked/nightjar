package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"servermonitor/internal/services"
)

// The public status page is designed in the dashboard: a title and intro, an optional
// announcement, and groups of checks shown under names of the owner's choosing. Checks not in
// the design never appear. Without a design, every check is listed, as before.

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
}

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
	writeJSON(w, http.StatusOK, map[string]any{"enabled": cfg.StatusPageEnabled, "designed": designed, "design": d, "checks": checks})
}

func (s *Server) saveStatusPageDesign(w http.ResponseWriter, r *http.Request) {
	var d StatusDesign
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
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
	Recent    []publicBeat `json:"recent"`
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
	if d.Announcement != "" {
		body["announcement"] = map[string]string{"text": d.Announcement, "tone": d.Tone}
	}
	writeJSON(w, http.StatusOK, body)
}

// publicRows is every check as the public may see it, by name, and the names in display order.
func (s *Server) publicRows() (map[string]publicService, []string) {
	rows := map[string]publicService{}
	var order []string
	if s.ctx.Services == nil {
		return rows, order
	}
	for _, sum := range s.ctx.Services.Summaries(nowSeconds()) {
		row := publicService{Name: sum.Name, Status: sum.Status, Uptime24h: sum.Uptime24h, Uptime30d: sum.Uptime30d, Recent: []publicBeat{}}
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
				row.Recent = []publicBeat{}
			}
			if !d.ShowUptime {
				row.Uptime24h, row.Uptime30d = nil, nil
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
