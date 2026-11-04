package diagnose_test

import (
	"strings"
	"testing"

	"nightjar/internal/diagnose"
)

func j(msg string) string { return "2026-11-02T20:54:23+0000 nas app[1234]: " + msg }

func TestRootCauseComesFirst(t *testing.T) {
	lines := []string{
		j("[Info] Bootstrap: Starting Sonarr"),
		j("[Fatal] ConsoleApp: Failed to bind to address http://[::]:8989: address already in use."),
		j("sonarr.service: Main process exited, code=exited, status=1/FAILURE"),
		j("sonarr.service: Start request repeated too quickly."),
	}
	got := diagnose.Lines(lines, diagnose.Context{Result: "start-limit-hit"})
	if len(got) < 2 || got[0].ID != "port_in_use" || got[0].Title != "Port 8989 is already in use" {
		t.Fatalf("the port should be the likely cause: %+v", got)
	}
	if !strings.Contains(got[0].Fix, "sport = :8989") || !got[0].Known {
		t.Errorf("fix should name the port: %q", got[0].Fix)
	}
	if got[1].ID != "start_limit" {
		t.Errorf("systemd giving up is a symptom, second: %+v", got[1])
	}
}

func TestRecognisedCauses(t *testing.T) {
	cases := []struct{ line, id, title string }{
		{"[Error] DiskProvider: Access to the path '/mnt/media/tv' is denied.", "permission", "It can't access /mnt/media/tv"},
		{"listen tcp :80: bind: permission denied", "privileged_port", "Port 80 needs root"},
		{"bazarr.service: Failed to locate executable /opt/bazarr/venv/bin/python3: No such file or directory", "exec_missing", "Its program file is missing: /opt/bazarr/venv/bin/python3"},
		{"ModuleNotFoundError: No module named 'requests'", "py_module", "Python module requests is missing"},
		{"write /var/lib/app/app.db: no space left on device", "disk_full", "The disk holding /var/lib/app/app.db is full"},
		{"dial tcp: lookup indexer.example.org: no such host", "dns", "It can't look up indexer.example.org"},
		{"dial tcp 10.0.0.30:873: connect: connection refused", "refused", "Nothing answers at 10.0.0.30:873"},
		{"Out of memory: Killed process 4242 (Radarr) total-vm:4000000kB", "oom", "It ran out of memory"},
		{"code=dumped, status=11/SEGV", "segfault", "It crashed"},
		{"System.Data.SQLite.SQLiteException: database is locked", "db_locked", "Its database is locked"},
		{"yaml: line 12: did not find expected key while parsing config.yml", "config", "Its settings file has an error"},
		{"x509: certificate has expired or is not yet valid", "tls", "A certificate has expired"},
	}
	for _, c := range cases {
		got := diagnose.Lines([]string{j(c.line)}, diagnose.Context{})
		if len(got) == 0 || got[0].ID != c.id || got[0].Title != c.title {
			t.Errorf("%q: got %+v, want %s %q", c.line, got, c.id, c.title)
		}
	}
}

func TestRepeatedErrorsGroupByShape(t *testing.T) {
	lines := []string{
		j("[Warn] Indexer 12 returned error 429 for query 'severance s02e03'"),
		j("[Info] Grabbed release"),
		j("[Warn] Indexer 7 returned error 429 for query 'andor'"),
		j("[Warn] Indexer 9 returned error 429 for query 'heat 1995'"),
	}
	got := diagnose.Lines(lines, diagnose.Context{})
	if len(got) != 1 || got[0].Known || got[0].Count != 3 || !strings.Contains(got[0].Line, "Indexer 9") {
		t.Fatalf("three occurrences, one group, latest line: %+v", got)
	}
	if !strings.Contains(got[0].Explain, "3 times") {
		t.Errorf("explain %q", got[0].Explain)
	}
}

func TestExitCodesExplainThemselves(t *testing.T) {
	got := diagnose.Lines(nil, diagnose.Context{Result: "exit-code", ExitStatus: 217})
	if len(got) != 1 || got[0].ID != "user_missing" {
		t.Fatalf("217 is a missing user: %+v", got)
	}
	if got := diagnose.Lines(nil, diagnose.Context{Result: "oom-kill"}); len(got) != 1 || got[0].ID != "oom" {
		t.Fatalf("oom-kill: %+v", got)
	}
	if got := diagnose.Lines([]string{j("Main process exited")}, diagnose.Context{Result: "exit-code", ExitStatus: 1}); len(got) != 0 {
		t.Fatalf("an ordinary exit with nothing in the logs says nothing: %+v", got)
	}
}

func TestText(t *testing.T) {
	if f, ok := diagnose.Text("timed out after 10s"); !ok || f.ID != "timeout" {
		t.Errorf("timeout: %+v", f)
	}
	if f, ok := diagnose.Text("HTTP 502"); !ok || f.Title != "The proxy can't reach the app (HTTP 502)" {
		t.Errorf("502: %+v", f)
	}
	if _, ok := diagnose.Text("HTTP 200"); ok {
		t.Error("a healthy answer is not a problem")
	}
}

func TestStrip(t *testing.T) {
	if got := diagnose.Strip("2026-11-02T20:54:23+0000 nas systemd[1]: Started Sonarr."); got != "Started Sonarr." {
		t.Errorf("strip %q", got)
	}
}
