package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/config"
)

func TestDotenvParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte(`# comment
PLAIN=value
export EXPORTED=yes
QUOTED="has # hash"
SINGLE='single'
TRAILING=kept # dropped
SPACED = padded
NOEQUALS
ALREADY=from-file
`), 0o600)
	t.Setenv("ALREADY", "from-env")
	for _, k := range []string{"PLAIN", "EXPORTED", "QUOTED", "SINGLE", "TRAILING", "SPACED"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := config.LoadDotenv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN": "value", "EXPORTED": "yes", "QUOTED": "has # hash", "SINGLE": "single",
		"TRAILING": "kept", "SPACED": "padded", "ALREADY": "from-env",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if err := config.LoadDotenv(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file should error")
	}
}

func load(values map[string]string) config.Settings { return config.LoadFrom(config.MapLookup(values)) }

func TestParsingFallsBackToDefaults(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{{"", 80}, {"70.5", 70.5}, {" 60 ", 60}, {"abc", 80}, {"-1", 80}, {"101", 80}, {"100", 100}}
	for _, c := range cases {
		if got := load(map[string]string{"CPU_THRESHOLD": c.raw}).CPUThreshold; got != c.want {
			t.Errorf("CPU_THRESHOLD=%q: got %v, want %v", c.raw, got, c.want)
		}
	}
	for raw, want := range map[string]int{"": 2, "9": 9, "x": 2, "-1": 2, "21": 2} {
		if got := load(map[string]string{"PLEX_RETRIES": raw}).PlexRetries; got != want {
			t.Errorf("PLEX_RETRIES=%q: got %v, want %v", raw, got, want)
		}
	}
	for raw, want := range map[string]bool{"": true, "YES": true, "on": true, "0": false, "Off": false, "maybe": true} {
		if got := load(map[string]string{"KERNEL_MONITOR_ENABLED": raw}).KernelMonitorEnabled; got != want {
			t.Errorf("KERNEL_MONITOR_ENABLED=%q: got %v, want %v", raw, got, want)
		}
	}
	for raw, want := range map[string]string{"": "INFO", "debug": "DEBUG", "WARN": "WARNING", "loud": "INFO"} {
		if got := load(map[string]string{"LOG_LEVEL": raw}).LogLevel; got != want {
			t.Errorf("LOG_LEVEL=%q: got %v, want %v", raw, got, want)
		}
	}
	for raw, want := range map[string]string{"": "text", "json": "json", "JSON": "json", " Text ": "text", "xml": "text"} {
		if got := load(map[string]string{"LOG_FORMAT": raw}).LogFormat; got != want {
			t.Errorf("LOG_FORMAT=%q: got %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity"} {
		if got := load(map[string]string{"CHECK_INTERVAL": raw, "ALERT_COOLDOWN": raw, "CPU_THRESHOLD": raw}); got.CPUThreshold != 80 || got.CheckInterval != load(nil).CheckInterval || got.AlertCooldown != load(nil).AlertCooldown {
			t.Errorf("%q should fall back to the defaults: %+v", raw, got)
		}
	}
	if got := load(map[string]string{"STATUS_PAGE_TITLE": strings.Repeat("x", 10_001)}).StatusPageTitle; got != "Service Status" {
		t.Error("over-long values should fall back to the default")
	}
	t.Setenv("T_BOOL", "nope")
	if !config.Bool("T_BOOL", true) {
		t.Error("an invalid install switch falls back to its default")
	}
}

func TestDashboardSettingsOverrideEnvironment(t *testing.T) {
	env := config.MapLookup(map[string]string{"CPU_THRESHOLD": "70", "MEMORY_THRESHOLD": "60"})
	saved := config.MapLookup(map[string]string{"CPU_THRESHOLD": "90", "MEMORY_THRESHOLD": " "})
	s := config.LoadFrom(config.Layered(saved, env))
	if s.CPUThreshold != 90 || s.MemoryThreshold != 60 {
		t.Fatalf("saved values win and blank ones fall through: cpu %v memory %v", s.CPUThreshold, s.MemoryThreshold)
	}
}

func TestValidate(t *testing.T) {
	problems := config.Validate(map[string]string{
		"CPU_THRESHOLD": "abc", "WEB_HOST": "0.0.0.0", "NOPE": "1", "MEMORY_THRESHOLD": "", "PLEX_TOKEN": "secret",
		"CHECK_INTERVAL": "NaN", "ALERT_COOLDOWN": "+Inf",
	})
	for _, key := range []string{"CPU_THRESHOLD", "WEB_HOST", "NOPE", "CHECK_INTERVAL", "ALERT_COOLDOWN"} {
		if problems[key] == "" {
			t.Errorf("%s should be rejected", key)
		}
	}
	for _, key := range []string{"MEMORY_THRESHOLD", "PLEX_TOKEN"} {
		if p, bad := problems[key]; bad {
			t.Errorf("%s should be accepted: %s", key, p)
		}
	}
}

func TestEveryFieldIsDescribed(t *testing.T) {
	var s config.Settings
	seen := map[string]bool{}
	for _, f := range s.Fields() {
		if seen[f.Key] {
			t.Errorf("%s declared twice", f.Key)
		}
		seen[f.Key] = true
		if f.Group == "" || f.Label == "" {
			t.Errorf("%s needs a group and a label", f.Key)
		}
		if f.Kind == config.KindSecret && f.Value() != "" {
			t.Errorf("%s must never reveal its value", f.Key)
		}
	}
}

func TestLoadDefaultsAreConsistent(t *testing.T) {
	s := config.LoadFrom(os.LookupEnv)
	// The documented defaults must survive cross-validation unchanged.
	if s.DiskThreshold != 80 || s.DiskEarlyWarningThreshold != 75 || s.DiskCleanupThreshold != 85 ||
		s.DiskAggressiveCleanupThreshold != 92 || s.DiskEmergencyThreshold != 95 {
		t.Fatalf("disk thresholds changed: %+v", s)
	}
	if s.WebHost != "127.0.0.1" || s.WebPort != 8080 || s.CheckInterval != 5 || s.PlexCheckInterval != 15 {
		t.Fatalf("unexpected defaults: host=%s port=%d interval=%v", s.WebHost, s.WebPort, s.CheckInterval)
	}
	if s.MediaDirectoryThresholdBytes != 500*config.BytesPerGB {
		t.Fatalf("media threshold bytes %d", s.MediaDirectoryThresholdBytes)
	}
}

func TestLoadCrossValidation(t *testing.T) {
	t.Setenv("DISK_THRESHOLD", "90")
	t.Setenv("DISK_EARLY_WARNING_THRESHOLD", "95")
	t.Setenv("DISK_CLEANUP_THRESHOLD", "88")
	t.Setenv("DISK_AGGRESSIVE_CLEANUP_THRESHOLD", "80")
	t.Setenv("DISK_EMERGENCY_THRESHOLD", "50")
	t.Setenv("CPU_THRESHOLD", "150")
	t.Setenv("CHECK_INTERVAL", "0")
	t.Setenv("TEMP_DIRS", "/tmp/a, /var/tmp/b")
	t.Setenv("MEDIA_DIRECTORY", "/proc/self")
	t.Setenv("LOG_SERVICES_TO_RESTART", " rsyslog, ,nginx ")
	t.Setenv("PLEX_CHECK_INTERVAL", "1")
	s := config.LoadFrom(os.LookupEnv)
	checks := []struct {
		name      string
		got, want float64
	}{
		{"early warning below threshold", s.DiskEarlyWarningThreshold, 85},
		{"cleanup kept", s.DiskCleanupThreshold, 88},
		{"aggressive raised to cleanup", s.DiskAggressiveCleanupThreshold, 88},
		{"emergency out of range uses default", s.DiskEmergencyThreshold, 95},
		{"cpu out of range uses default", s.CPUThreshold, 80},
		{"check interval out of range uses default", s.CheckInterval, 5},
		{"plex interval out of range uses default", s.PlexCheckInterval, 15},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}
	if s.MediaDirectory != "/home/media" {
		t.Errorf("suspicious media directory kept: %s", s.MediaDirectory)
	}
	if len(s.TempDirs) != 2 || s.TempDirs[1] != "/var/tmp/b" {
		t.Errorf("temp dirs: %q", s.TempDirs)
	}
	if len(s.LogServicesToRestart) != 2 || s.LogServicesToRestart[1] != "nginx" {
		t.Errorf("list parsing: %q", s.LogServicesToRestart)
	}
}

func TestLiveAndEssentialFlags(t *testing.T) {
	var s config.Settings
	live, advanced, keys, liveKeys := 0, 0, map[string]bool{}, map[string]bool{}
	for _, f := range s.Fields() {
		keys[f.Key] = true
		liveKeys[f.Key] = f.Live
		if f.Live {
			live++
			if f.Startup {
				t.Errorf("%s is read at startup, so it cannot be live", f.Key)
			}
		}
		if f.Advanced {
			advanced++
		}
	}
	// The lists are by key; a typo would silently leave a setting needing a restart or folded away.
	for _, k := range []string{"CPU_THRESHOLD", "SEEDING_RATIO_LIMIT", "STATUS_PAGE_TITLE", "UPDATES_WINDOW_END"} {
		if !liveKeys[k] {
			t.Errorf("%s should be a live setting", k)
		}
	}
	if liveKeys["PLEX_URL"] || liveKeys["WEBHOOK"] {
		t.Error("connections are built at startup and need a restart")
	}
	if live < 40 || advanced < 50 || advanced > len(keys)-30 {
		t.Errorf("live %d, advanced %d of %d", live, advanced, len(keys))
	}
}

func TestEnvironLeavesOutSecrets(t *testing.T) {
	t.Setenv("WEB_AUTH_TOKEN", "t0ken")
	t.Setenv("SMTP_PASSWORD", "pw")
	t.Setenv("CPU_THRESHOLD", "70")
	env := strings.Join(config.Environ(), "\n")
	if strings.Contains(env, "WEB_AUTH_TOKEN=") || strings.Contains(env, "SMTP_PASSWORD=") || !strings.Contains(env, "CPU_THRESHOLD=70") {
		t.Fatalf("environment for scripts: %s", env)
	}
}
