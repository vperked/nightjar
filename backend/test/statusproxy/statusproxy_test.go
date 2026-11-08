package statusproxy_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/statusproxy"
)

type fake struct {
	calls  []string
	refuse string // a command line that fails, such as "nginx -t"
}

func (f *fake) run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if f.refuse != "" && strings.HasPrefix(line, f.refuse) {
		return "nginx: [emerg] something is wrong\nnginx: configuration file test failed", errors.New("exit status 1")
	}
	return "", nil
}

func options(t *testing.T, f *fake) (statusproxy.Options, string) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sites-available"), 0o755)
	os.MkdirAll(filepath.Join(dir, "sites-enabled"), 0o755)
	return statusproxy.Options{
		Domain: "status.example.com", Port: 8080, NginxDir: dir, Run: f.run,
		IsRoot:   func() bool { return true },
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}, dir
}

func TestInstallWritesChecksReloadsAndGetsACertificate(t *testing.T) {
	f := &fake{}
	o, _ := options(t, f)
	o.Email = "me@example.com"
	steps, err := statusproxy.Install(context.Background(), o)
	if err != nil {
		t.Fatalf("%v %+v", err, steps)
	}
	got := strings.Join(f.calls, " | ")
	for _, want := range []string{"nginx -t", "systemctl reload nginx", "certbot --nginx -d status.example.com --non-interactive --agree-tos --redirect --keep-until-expiring -m me@example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if st := statusproxy.Inspect(o); !st.Installed {
		t.Fatalf("state: %+v", st)
	}
}

// A site nginx will not take is taken out again, so nginx is never left unable to start.
func TestASiteNginxRefusesIsTakenOutAgain(t *testing.T) {
	f := &fake{refuse: "nginx -t"}
	o, dir := options(t, f)
	steps, err := statusproxy.Install(context.Background(), o)
	if err == nil || !strings.Contains(steps[len(steps)-1].Detail, "configuration file test failed") {
		t.Fatalf("%v %+v", err, steps)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*", "*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if strings.Contains(strings.Join(f.calls, " "), "reload") {
		t.Fatal("nginx was reloaded with a configuration it refused")
	}
}

// A domain another site already serves, and a file Nightjar did not write, are left alone.
func TestSomeoneElsesSiteIsLeftAlone(t *testing.T) {
	f := &fake{}
	o, dir := options(t, f)
	other := filepath.Join(dir, "sites-enabled", "mine")
	os.WriteFile(other, []byte("server {\n    server_name www.example.com status.example.com;\n}\n"), 0o644)
	if _, err := statusproxy.Install(context.Background(), o); err == nil || !strings.Contains(err.Error(), "already serves") {
		t.Fatalf("a domain another site serves: %v", err)
	}
	os.Remove(other)
	file := filepath.Join(dir, "sites-available", "nightjar-status-status.example.com.conf")
	os.WriteFile(file, []byte("# my own\n"), 0o644)
	if _, err := statusproxy.Install(context.Background(), o); err == nil {
		t.Fatal("a file Nightjar did not write was overwritten")
	}
	if _, err := statusproxy.Remove(context.Background(), o); err == nil {
		t.Fatal("a file Nightjar did not write was removed")
	}
	if b, _ := os.ReadFile(file); string(b) != "# my own\n" {
		t.Fatal("the file changed")
	}
}

// Without certbot, or when it cannot get a certificate, the page still works over HTTP.
func TestNoCertificateStillLeavesHTTP(t *testing.T) {
	f := &fake{refuse: "certbot"}
	o, _ := options(t, f)
	steps, err := statusproxy.Install(context.Background(), o)
	if err != nil {
		t.Fatalf("certbot failing is not a failed set-up: %v", err)
	}
	last := steps[len(steps)-1]
	if last.OK || !strings.Contains(last.Detail, "HTTP meanwhile") {
		t.Fatalf("%+v", last)
	}
	if !statusproxy.Inspect(o).Installed {
		t.Fatal("the HTTP site stays")
	}
}

func TestOnlyAPlainDomainIsWritten(t *testing.T) {
	f := &fake{}
	o, _ := options(t, f)
	o.Domain = "status.example.com;\n    return 200"
	if _, err := statusproxy.Install(context.Background(), o); err == nil {
		t.Fatal("a domain with nginx syntax in it was written")
	}
	if len(f.calls) != 0 {
		t.Fatal("commands ran")
	}
}
