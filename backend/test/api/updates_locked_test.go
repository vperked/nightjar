package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
)

// qbtServer is a token-less API whose detection finds a release that installs a checked download.
func qbtServer(t *testing.T) http.Handler {
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	run := func(context.Context, updates.Command) (updates.Output, error) {
		return updates.Output{Stdout: "1.0.0"}, nil
	}
	ctx := &monitor.Context{
		Cfg:     config.LoadFrom(config.MapLookup(map[string]string{"SYSTEM_UPDATE_ENABLED": "true"})),
		Updates: updates.New(updates.Options{Store: store, Run: run, GitHubAPI: "http://127.0.0.1:1"}, nil, false),
	}
	srv := api.New(api.Config{Host: "127.0.0.1", Port: "0", AllowedOrigin: "null", FrontendDir: t.TempDir(), Store: store,
		Detect: func() []updates.Spec {
			return []updates.Spec{{ID: "qbt", Name: "qBittorrent", Kind: updates.KindRelease, Repo: "qbittorrent/qBittorrent", VersionCommand: "qbittorrent-nox -v",
				UpdateCommand: "install-qbt", Asset: "qbittorrent-nox.AppImage", AssetRepo: "userdocs/qbittorrent-nox-static", Unit: "qbittorrent"}}
		}}, ctx)
	return srv.Handler()
}

// Without sign-in, not only the commands are locked: where a release's download comes from, which
// service is restarted as root and where an app's stored API key is sent decide as much.
func TestUpdatesWithoutSignInKeepWhatRunsAsRoot(t *testing.T) {
	h := qbtServer(t)
	if code, body := call(t, h, http.MethodPost, "/api/updates/detect", `{}`); code != http.StatusOK || len(body["added"].([]any)) != 1 {
		t.Fatalf("detect: %d %v", code, body)
	}
	// As the dashboard saves it: without the download, which stays.
	const same = `"id":"qbt","name":"qBittorrent","kind":"release","repo":"qbittorrent/qBittorrent","version_command":"qbittorrent-nox -v","update_command":"install-qbt","unit":"qbittorrent"`
	if code, body := call(t, h, http.MethodPut, "/api/updates/qbt", `{`+same+`,"auto":true}`); code != http.StatusOK {
		t.Fatalf("turning auto-update on: %d %v", code, body)
	}
	for name, change := range map[string]string{
		"the download's repository": `"asset_repo":"evil/release","asset":"qbittorrent-nox.AppImage"`,
		"the service":               `"unit":"ssh","restart":true`,
		"the repository":            `"repo":"evil/qBittorrent"`,
	} {
		spec := `{` + same + `,` + change + `}`
		// A later key wins in JSON, so the change replaces what same says.
		code, body := call(t, h, http.MethodPut, "/api/updates/qbt", spec)
		if code != http.StatusForbidden || !strings.Contains(body["error"].(string), "WEB_AUTH_TOKEN") {
			t.Errorf("changing %s without sign-in: %d %v", name, code, body)
		}
	}
	// An app's address, where its stored API key would go.
	if code, body := call(t, h, http.MethodPost, "/api/updates", `{"name":"Sonarr","kind":"arr","url":"http://evil.example"}`); code != http.StatusForbidden {
		t.Errorf("an app's address without sign-in: %d %v", code, body)
	}
}

// A target named "Settings" gets an id of its own, not the settings route's.
func TestATargetNamedSettingsIsSaved(t *testing.T) {
	h := updatesServer(t, "tok")
	code, body := call(t, h, http.MethodPost, "/api/updates", `{"name":"Settings","kind":"system"}`)
	if code != http.StatusCreated || body["target"].(map[string]any)["id"] != "settings-target" {
		t.Fatalf("add: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPost, "/api/updates", `{"id":"settings","name":"Other","kind":"system"}`); code != http.StatusBadRequest {
		t.Fatalf("an id of settings: %d %v", code, body)
	}

	hosts, id, _ := hostUpdatesServer(t, "tok")
	base := "/api/hosts/" + id + "/updates"
	code, body = call(t, hosts, http.MethodPost, base, `{"name":"Settings","kind":"apt","package":"settings"}`)
	if code != http.StatusCreated || body["target"].(map[string]any)["id"] != "settings-target" {
		t.Fatalf("add on a server: %d %v", code, body)
	}
	if code, body := call(t, hosts, http.MethodPut, base+"/settings-target", `{"id":"settings-target","name":"Settings","kind":"apt","package":"settings","paused":true}`); code != http.StatusOK || body["target"].(map[string]any)["paused"] != true {
		t.Fatalf("edit on a server: %d %v", code, body)
	}
}

func TestServersUpdateServicesNeedSignIn(t *testing.T) {
	h, id, _ := hostUpdatesServer(t, "")
	base := "/api/hosts/" + id + "/updates"
	if code, body := call(t, h, http.MethodPut, base+"/plex", `{"id":"plex","name":"Plex","kind":"apt","package":"plexmediaserver","unit":"ssh"}`); code != http.StatusForbidden {
		t.Fatalf("choosing a service without sign-in: %d %v", code, body)
	}
}
