package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/sshd"
)

func TestTheDashboardManagesSSHLoginsOverHTTPSOnly(t *testing.T) {
	const token = "a-token-that-is-long-enough-1234"
	etc := t.TempDir()
	os.WriteFile(filepath.Join(etc, "sshd_config"), []byte("Include sshd_config.d/*.conf\n"), 0o644)
	os.WriteFile(filepath.Join(etc, "passwd"), nil, 0o644)
	var ran []string
	exec := func(_ context.Context, name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return "passwordauthentication yes\n", nil
	}
	s := agent.NewSampler("test")
	s.UseSSHD(&sshd.Manager{Dir: etc, Passwd: filepath.Join(etc, "passwd"), Exec: exec})
	call := func(h http.Handler, method, path, body, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	h := agent.Handler(s, token, nil, true)
	if rec := call(h, "GET", agent.SSHDPath, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", rec.Code)
	}
	rec := call(h, "GET", agent.SSHDPath, "", token)
	var v sshd.Access
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || !v.PasswordLogin || v.Users == nil {
		t.Fatalf("view: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PUT", agent.SSHDPath+"/keys-only", `{}`, token); rec.Code != http.StatusBadRequest {
		t.Fatalf("no body: %d", rec.Code)
	}
	if rec := call(h, "PUT", agent.SSHDPath+"/keys-only", `{"on": true, "user": "root"}`, token); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no account") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	// Over plain HTTP it shows the logins but changes nothing.
	plain := agent.Handler(s, token, nil, false)
	if rec := call(plain, "GET", agent.SSHDPath, "", token); rec.Code != http.StatusOK {
		t.Fatalf("plain view: %d", rec.Code)
	}
	ran = nil
	if rec := call(plain, "PUT", agent.SSHDPath+"/keys-only", `{"on": false}`, token); rec.Code != http.StatusForbidden || len(ran) != 0 {
		t.Fatalf("plain change: %d %v", rec.Code, ran)
	}
	// An agent without SSH logins to manage says so.
	if rec := call(agent.Handler(agent.NewSampler("test"), token, nil, true), "GET", agent.SSHDPath, "", token); rec.Code != http.StatusNotFound {
		t.Fatalf("no manager: %d", rec.Code)
	}
}
