package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// The dashboard's look is chosen in Settings → Appearance: a style (the dot matrix or rounded
// cards), how charts and meters are drawn, how dense the pages are, and the owner's own CSS on top.
// It is kept with the dashboard's other settings in the database and applies in every browser.

const appearanceKey = "dashboard.appearance"

// Appearance is how the dashboard looks.
type Appearance struct {
	Style   string `json:"style"`   // matrix or cards
	Charts  string `json:"charts"`  // dots or lines; empty follows the style
	Density string `json:"density"` // comfortable or compact
	CSS     string `json:"css"`     // the owner's own, applied over the style
}

var (
	styles    = []string{"matrix", "cards"}
	chartKind = []string{"", "dots", "lines"}
	densities = []string{"comfortable", "compact"}
)

func defaultAppearance() Appearance { return Appearance{Style: "matrix", Density: "comfortable"} }

func (a *Appearance) validate() error {
	a.Style = strings.TrimSpace(a.Style)
	a.Charts = strings.TrimSpace(a.Charts)
	a.Density = strings.TrimSpace(a.Density)
	if a.Style == "" {
		a.Style = "matrix"
	}
	if a.Density == "" {
		a.Density = "comfortable"
	}
	if !slices.Contains(styles, a.Style) {
		return fmt.Errorf("the style must be one of %s", strings.Join(styles, ", "))
	}
	if !slices.Contains(chartKind, a.Charts) {
		return fmt.Errorf("charts must be dots, lines or empty to follow the style")
	}
	if !slices.Contains(densities, a.Density) {
		return fmt.Errorf("the density must be one of %s", strings.Join(densities, ", "))
	}
	// The owner's code keeps its own spacing; only its ends are trimmed.
	a.CSS = strings.TrimSpace(a.CSS)
	if len(a.CSS) > maxCSS {
		return fmt.Errorf("the CSS is longer than %d characters", maxCSS)
	}
	// A closing tag would end the style element the CSS is placed in.
	if strings.Contains(strings.ToLower(a.CSS), "</style") {
		return fmt.Errorf("the CSS cannot contain </style")
	}
	return nil
}

// appearance is the stored look, or the default one.
func (s *Server) appearance() Appearance {
	a := defaultAppearance()
	if raw := s.cfg.Store.Meta(appearanceKey); raw != "" {
		var stored Appearance
		if json.Unmarshal([]byte(raw), &stored) == nil && stored.validate() == nil {
			a = stored
		}
	}
	return a
}

// publicAppearance is the look for the config: without a sign-in, the style but not the owner's CSS.
func (s *Server) publicAppearance(signedIn bool) Appearance {
	a := s.appearance()
	if !signedIn {
		a.CSS = ""
	}
	return a
}

func (s *Server) getAppearance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"appearance": s.appearance()})
}

func (s *Server) saveAppearance(w http.ResponseWriter, r *http.Request) {
	var a Appearance
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected an appearance"))
		return
	}
	if err := a.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	data, _ := json.Marshal(a)
	if err := s.cfg.Store.SetMeta(appearanceKey, string(data)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"appearance": a})
}
