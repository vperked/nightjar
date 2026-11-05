package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/remote"
	"nightjar/internal/storage"
)

func TestAnUnconfirmedHostKeyIsShownAndCanBeTrusted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := storage.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(db.Close)
	var mu sync.Mutex
	logins := 0
	exec := func(_ context.Context, stdin []byte, _ []string, name string, a ...string) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case name == "ssh-keygen" && slices.Contains(a, "-F"):
			b, _ := os.ReadFile(a[slices.Index(a, "-f")+1])
			if !strings.Contains(string(b), "AAAAed") {
				return "", "", errors.New("exit status 1")
			}
			return "# Host found: line 1\nvps.example.net ED25519 SHA256:ed\n", "", nil
		case name == "ssh-keygen" && slices.Contains(a, "-"):
			return "256 SHA256:ed vps.example.net (ED25519)\n", "", nil
		case name == "ssh-keygen" && slices.Contains(a, "-l"):
			return "256 SHA256:abc nightjar@test (ED25519)\n", "", nil
		case name == "ssh-keygen":
			p := a[len(a)-1]
			os.WriteFile(p, []byte("PRIVATE"), 0o600)
			os.WriteFile(p+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644)
		case name == "ssh-keyscan":
			return "vps.example.net ssh-ed25519 AAAAed\n", "", nil
		case name == "ssh":
			logins++
		}
		return "", "", nil
	}
	rm := remote.New(remote.Options{Store: db, SSHDir: filepath.Join(t.TempDir(), "ssh"), Exec: exec, AskPass: "/usr/bin/nightjar"})
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Remote: rm}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", FrontendDir: t.TempDir()}, ctx)
	call := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	k, err := rm.NewSSHKey(context.Background(), "nightjar")
	if err != nil {
		t.Fatal(err)
	}
	login := map[string]any{"host": "vps.example.net", "key": k.Path}

	// The first time, the password goes nowhere: the browser gets the key to check.
	code, out := call("/api/ssh/authorize", map[string]any{"login": login, "password": "hunter2"})
	if code != http.StatusUnprocessableEntity || out["host_key"] != "ED25519 SHA256:ed" || logins != 0 {
		t.Fatalf("authorize before trusting: %d %v, %d logins", code, out, logins)
	}
	hostKey := out["host_key"]

	// A key the server does not present is refused.
	if code, out := call("/api/ssh/trust", map[string]any{"login": login, "host_key": "ED25519 SHA256:other"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("trust the wrong key: %d %v", code, out)
	}
	if code, out := call("/api/ssh/trust", map[string]any{"id": "nope", "login": login, "host_key": "ED25519 SHA256:ed"}); code != http.StatusNotFound {
		t.Fatalf("trust for a missing server: %d %v", code, out)
	}
	if code, out := call("/api/ssh/trust", map[string]any{"login": login, "host_key": hostKey}); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("trust: %d %v", code, out)
	}

	// Once confirmed, the password goes.
	code, out = call("/api/ssh/authorize", map[string]any{"login": login, "password": "hunter2"})
	if code != http.StatusOK || out["ok"] != true || logins != 1 {
		t.Fatalf("authorize after trusting: %d %v, %d logins", code, out, logins)
	}
}
