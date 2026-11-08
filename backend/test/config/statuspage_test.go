package config_test

import (
	"testing"

	"nightjar/internal/config"
)

func set(t *testing.T, s *config.Settings, key, value string) error {
	t.Helper()
	for _, f := range s.Fields() {
		if f.Key == key {
			return f.Set(value)
		}
	}
	t.Fatalf("no setting %s", key)
	return nil
}

// The status page's domain goes into nginx and certbot as it is, so only a plain host name is taken;
// a pasted address is trimmed to one.
func TestStatusPageDomainTakesOnlyAHostName(t *testing.T) {
	var s config.Settings
	for in, want := range map[string]string{"status.example.com": "status.example.com", "https://Status.Example.com/": "status.example.com", "": ""} {
		if err := set(t, &s, "STATUS_PAGE_DOMAIN", in); err != nil || s.StatusPageDomain != want {
			t.Errorf("%q: got %q, %v", in, s.StatusPageDomain, err)
		}
	}
	for _, bad := range []string{"localhost", "status.example.com:8443", "status.example.com/x", "*.example.com", "a b.com", "status.example.com; return 200", "-bad.example.com", "example.com\nserver_name evil.com"} {
		if err := set(t, &s, "STATUS_PAGE_DOMAIN", bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestStatusPagePathIsOneWordAndNotADashboardPage(t *testing.T) {
	var s config.Settings
	for in, want := range map[string]string{"/uptime": "/uptime", "uptime": "/uptime", "/Status-2/": "/status-2", "": "/status"} {
		if err := set(t, &s, "STATUS_PAGE_PATH", in); err != nil || s.StatusPagePath != want {
			t.Errorf("%q: got %q, %v", in, s.StatusPagePath, err)
		}
	}
	for _, bad := range []string{"/", "/settings", "/api", "/assets", "/a/b", "/up time", "/../etc"} {
		if err := set(t, &s, "STATUS_PAGE_PATH", bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}
