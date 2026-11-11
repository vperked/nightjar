package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/remote"
	"nightjar/internal/sshd"
	"nightjar/internal/storage"
)

const sshdTestKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFloufjWe/SlT1cjuMRtrfu1swKcgY5YDp53eKuIR9oL test@nightjar"

// sshdMachine is a pretend machine for SSH logins: sshd's folder, one account (alice, given to
// whoever runs the test unless that is root) and a fake sshd and systemctl.
func sshdMachine(t *testing.T) *sshd.Manager {
	t.Helper()
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	if uid < 1000 {
		t.Skip("needs to run as root or as a user with uid 1000 or more")
	}
	root := t.TempDir()
	etc := filepath.Join(root, "ssh")
	os.MkdirAll(filepath.Join(etc, "sshd_config.d"), 0o755)
	os.WriteFile(filepath.Join(etc, "sshd_config"), []byte("Port 2200\nInclude sshd_config.d/*.conf\n"), 0o644)
	os.Mkdir(filepath.Join(root, "alice"), 0o755)
	os.WriteFile(filepath.Join(root, "passwd"), []byte("alice:x:"+strconv.Itoa(uid)+":"+strconv.Itoa(gid)+"::"+filepath.Join(root, "alice")+":/bin/bash\n"), 0o644)
	exec := func(_ context.Context, name string, args ...string) (string, error) {
		if name+" "+strings.Join(args, " ") == "sshd -T" {
			if _, err := os.Stat(filepath.Join(etc, "sshd_config.d", sshd.DropIn)); err == nil {
				return "passwordauthentication no\n", nil
			}
			return "passwordauthentication yes\n", nil
		}
		return "", nil
	}
	return &sshd.Manager{Dir: etc, Passwd: filepath.Join(root, "passwd"), Exec: exec}
}

func TestSSHLoginsAreManagedHereAndOnServers(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	t.Cleanup(db.Close)
	const agentToken = "0123456789abcdef0123456789abcdef"
	// A server whose agent manages its own SSH logins.
	s0 := agent.NewSampler("test")
	s0.UseSSHD(sshdMachine(t))
	s0.Sample(context.Background())
	srv := httptest.NewServer(agent.Handler(s0, agentToken, nil, true))
	defer srv.Close()
	rm := remote.New(remote.Options{Store: db})
	host, err := rm.Add(context.Background(), "box", srv.URL, agentToken)
	if err != nil {
		t.Fatal(err)
	}

	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, SSHD: sshdMachine(t), Remote: rm}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "8080", AuthToken: "tok", Store: db, FrontendDir: t.TempDir()}, ctx)
	type view struct {
		sshd.Access
		Error string `json:"error"`
	}
	call := func(method, path, body string) (int, view) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out view
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	for _, base := range []string{"/api/sshd", "/api/hosts/" + host.ID + "/sshd"} {
		code, v := call("GET", base, "")
		if code != http.StatusOK || !v.PasswordLogin || v.KeysOnly || len(v.Ports) != 1 || v.Ports[0] != 2200 || len(v.Users) != 1 || v.Users[0].Name != "alice" {
			t.Fatalf("%s: %d %+v", base, code, v)
		}
		if code, _ := call("POST", base+"/keys", `{"user": "alice"}`); code != http.StatusBadRequest {
			t.Fatalf("%s: no key: %d", base, code)
		}
		if code, v := call("POST", base+"/keys", `{"user": "alice", "key": "-----BEGIN OPENSSH PRIVATE KEY-----"}`); code != 422 || !strings.Contains(v.Error, "private key") {
			t.Fatalf("%s: a private key: %d %+v", base, code, v)
		}
		if code, v := call("PUT", base+"/keys-only", `{"on": true, "user": "alice"}`); code != 422 || !strings.Contains(v.Error, "add a key for alice first") {
			t.Fatalf("%s: no key yet: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/keys", `{"user": "alice", "key": "`+sshdTestKey+`"}`); code != http.StatusOK || v.Users[0].Keys != 1 || !strings.HasPrefix(v.Users[0].Fingerprints[0], "SHA256:") {
			t.Fatalf("%s: add key: %d %+v", base, code, v)
		}
		if code, v = call("PUT", base+"/keys-only", `{"on": true, "user": "alice"}`); code != http.StatusOK || !v.KeysOnly || v.PasswordLogin || v.Pending == nil {
			t.Fatalf("%s: keys only: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/keys-only/keep", `{"keep": false}`); code != http.StatusOK || v.KeysOnly || v.Pending != nil || !v.PasswordLogin {
			t.Fatalf("%s: undo: %d %+v", base, code, v)
		}
		if code, _ := call("POST", base+"/keys-only/keep", `{"keep": true}`); code != 422 {
			t.Fatalf("%s: nothing to keep: %d", base, code)
		}
	}
	if code, _ := call("GET", "/api/hosts/nope/sshd", ""); code != http.StatusNotFound {
		t.Fatalf("no such server: %d", code)
	}
}
