package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/remote"
	"nightjar/internal/storage"
)

func TestASavedLoginIsTriedAtOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := storage.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(db.Close)
	refuse := false
	exec := func(_ context.Context, _ []byte, _ []string, name string, args ...string) (string, string, error) {
		switch {
		case name == "ssh-keygen" && len(args) > 0 && args[0] == "-l":
			return "256 SHA256:abc test (ED25519)\n", "", nil
		case name == "ssh-keygen":
			p := args[len(args)-1]
			os.WriteFile(p, []byte("PRIVATE"), 0o600)
			os.WriteFile(p+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644)
		case name == "ssh" && refuse:
			return "", "root@127.0.0.1: Permission denied (publickey).", errors.New("exit status 255")
		case name == "ssh":
			return "root@vps\n up 3 days\n", "", nil
		}
		return "", "", nil
	}
	rm := remote.New(remote.Options{Store: db, SSHDir: filepath.Join(t.TempDir(), "ssh"), Exec: exec})
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Remote: rm}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", FrontendDir: t.TempDir()}, ctx)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
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
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Adding an SSH-only server tries its login straight away.
	body, _ := json.Marshal(map[string]any{"kind": "ssh", "name": "vps", "ssh": map[string]any{"host": "127.0.0.1", "port": port, "key": k.Path}})
	code, out := call("POST", "/api/hosts", string(body))
	test, _ := out["test"].(map[string]any)
	if code != http.StatusOK || test["ok"] != true || !strings.Contains(test["output"].(string), "root@vps") {
		t.Fatalf("add: %d %v", code, out)
	}
	id := out["host"].(map[string]any)["id"].(string)
	if c, _ := out["host"].(map[string]any)["ssh_check"].(map[string]any); c["ok"] != true || c["as"] != "root@vps" {
		t.Fatalf("the server comes back with how the login went: %v", out["host"])
	}

	// So does changing its login; a refused key says why, and the login is still saved.
	refuse = true
	login, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": port, "key": k.Path, "user": "admin"})
	code, out = call("PUT", "/api/hosts/"+id+"/ssh", string(login))
	test, _ = out["test"].(map[string]any)
	if code != http.StatusOK || test["ok"] != false || !strings.Contains(test["error"].(string), "authorized_keys") {
		t.Fatalf("change: %d %v", code, out)
	}
	if c, _ := out["host"].(map[string]any)["ssh_check"].(map[string]any); c["ok"] != false || !strings.Contains(c["error"].(string), "authorized_keys") {
		t.Fatalf("and that it failed: %v", out["host"])
	}
	if got, _ := rm.Get(id); got.SSH == nil || got.SSH.User != "admin" {
		t.Fatalf("the login is saved even when the test fails: %+v", got.SSH)
	}
}
