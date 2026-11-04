package setup_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nightjar/internal/setup"
)

// fakeMachine has a Plex token, a qBittorrent.conf and a Sonarr config in a temporary home.
func fakeMachine(t *testing.T, open ...int) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home", "media")
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prefs := filepath.Join(root, "Preferences.xml")
	write(prefs, `<?xml version="1.0"?><Preferences MachineIdentifier="x" PlexOnlineToken="tok-123" FriendlyName="nas"/>`)
	write(filepath.Join(home, ".config/qBittorrent/qBittorrent.conf"), "[Preferences]\nWebUI\\Port=10914\nWebUI\\Username=perked\nWebUI\\LocalHostAuth=false\n")
	write(filepath.Join(home, ".config/Sonarr/config.xml"), "<Config><Port>8989</Port><UrlBase>/sonarr</UrlBase><ApiKey>secret</ApiKey></Config>")

	oldPrefs, oldHomes, oldProbe, oldQB := setup.PlexPreferences, setup.HomeDirs, setup.Probe, setup.IsQBittorrent
	t.Cleanup(func() {
		setup.PlexPreferences, setup.HomeDirs, setup.Probe, setup.IsQBittorrent = oldPrefs, oldHomes, oldProbe, oldQB
	})
	setup.PlexPreferences = []string{filepath.Join(root, "missing.xml"), prefs}
	setup.HomeDirs = func() []string { return []string{filepath.Join(root, "home", "nobody"), home} }
	ports := map[int]bool{}
	for _, p := range open {
		ports[p] = true
	}
	setup.Probe = func(port int) bool { return ports[port] }
	setup.IsQBittorrent = func(port int) bool { return ports[port] && port != 8080 }
	return root
}

func TestDetectsPlexAndItsToken(t *testing.T) {
	fakeMachine(t)
	plexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<MediaContainer size="0" claimed="1" machineIdentifier="x" version="1.43.5.11079"/>`))
	}))
	defer plexSrv.Close()
	old := setup.PlexURL
	setup.PlexURL = plexSrv.URL
	defer func() { setup.PlexURL = old }()

	p := setup.DetectPlex()
	if !p.Running || p.Version != "1.43.5.11079" || !p.TokenFound {
		t.Fatalf("plex: %+v", p)
	}
	if setup.PlexToken() != "tok-123" {
		t.Fatal("the token comes from Preferences.xml")
	}
}

func TestDetectsQBittorrentAndSuggestsChecks(t *testing.T) {
	fakeMachine(t, 10914, 8989, 8181)
	qb := setup.DetectQBittorrent()
	if !qb.Found || qb.Port != 10914 || qb.Username != "perked" || !qb.LocalBypass || !qb.Answering {
		t.Fatalf("qbittorrent: %+v", qb)
	}
	got := map[string]string{}
	for _, s := range setup.SuggestChecks(qb) {
		got[s.Name] = s.Target
	}
	want := map[string]string{
		"Sonarr":      "http://127.0.0.1:8989/sonarr/ping",
		"qBittorrent": "http://127.0.0.1:10914/",
		"Tautulli":    "http://127.0.0.1:8181/status",
	}
	if len(got) != len(want) {
		t.Fatalf("suggestions %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}

// A config without a port means qBittorrent's default 8080, where another program (often this
// monitor) may answer: the user whose Web UI really is qBittorrent wins.
func TestPrefersTheConfigWhoseWebUIIsQBittorrent(t *testing.T) {
	root := fakeMachine(t, 10914, 8080)
	stale := filepath.Join(root, "home", "nobody", ".config/qBittorrent/qBittorrent.conf")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("[Preferences]\nGeneral\\Locale=en\n"), 0o600)
	qb := setup.DetectQBittorrent()
	if qb.Port != 10914 || qb.Username != "perked" {
		t.Fatalf("picked %+v", qb)
	}
	for _, s := range setup.SuggestChecks(setup.QBittorrent{Found: true, Port: 8080}) {
		if s.Name == "qBittorrent" {
			t.Fatal("a port that is not qBittorrent is not suggested as qBittorrent")
		}
	}
}

func TestNothingFound(t *testing.T) {
	root := fakeMachine(t)
	setup.PlexPreferences = []string{filepath.Join(root, "missing.xml")}
	setup.HomeDirs = func() []string { return []string{filepath.Join(root, "empty")} }
	if setup.PlexToken() != "" || setup.DetectQBittorrent().Found || len(setup.SuggestChecks(setup.QBittorrent{})) != 0 {
		t.Fatal("an empty machine has nothing to offer")
	}
}
