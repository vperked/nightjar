package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAppearance(t *testing.T) {
	m := newManaged(t)

	// Until it is chosen, the dashboard has the dot matrix at comfortable density.
	code, body := m.send(t, http.MethodGet, "/api/appearance", "")
	if a := body["appearance"].(map[string]any); code != http.StatusOK || a["style"] != "matrix" || a["density"] != "comfortable" || a["charts"] != "" || a["css"] != "" {
		t.Fatalf("default: %d %v", code, body)
	}

	look := `{"style":"cards","charts":"dots","density":"compact","css":"  .readout-value { letter-spacing: 0 }\n"}`
	if code, body = m.send(t, http.MethodPut, "/api/appearance", look); code != http.StatusOK {
		t.Fatalf("save: %d %v", code, body)
	}
	_, cfg := m.send(t, http.MethodGet, "/api/config", "")
	a := cfg["appearance"].(map[string]any)
	if a["style"] != "cards" || a["charts"] != "dots" || a["density"] != "compact" || a["css"] != ".readout-value { letter-spacing: 0 }" {
		t.Fatalf("the config carries the look: %v", a)
	}

	// Before signing in, the sign-in page gets the style but not the owner's CSS.
	_, cfg = m.send(t, http.MethodGet, "/api/config", "", "X-Auth-Token", "")
	if a := cfg["appearance"].(map[string]any); a["style"] != "cards" || a["css"] != "" {
		t.Fatalf("signed out: %v", a)
	}

	for _, bad := range []string{
		`{"style":"neon"}`,
		`{"charts":"bars"}`,
		`{"density":"tiny"}`,
		`{"css":"` + strings.Repeat("x", 50001) + `"}`,
		`{"css":"a{}</style><script>alert(1)</script>"}`,
	} {
		if code, _ := m.send(t, http.MethodPut, "/api/appearance", bad); code != http.StatusBadRequest {
			t.Fatalf("accepted %.60s", bad)
		}
	}
	if code, _ := m.send(t, http.MethodPut, "/api/appearance", `{}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site change: %d", code)
	}
	if code, _ := m.send(t, http.MethodGet, "/api/appearance", "", "X-Auth-Token", ""); code != http.StatusUnauthorized {
		t.Fatalf("read without signing in: %d", code)
	}
}
