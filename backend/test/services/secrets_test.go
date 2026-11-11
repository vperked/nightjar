package services_test

import (
	"testing"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// What may be shown of a target leaves out what would let someone act as the check: a heartbeat's
// token, and the password or token in an address.
func TestShownTargetLeavesOutSecrets(t *testing.T) {
	for _, c := range []struct{ spec, want string }{
		{`{"name":"a","type":"heartbeat","target":"tok-tok-tok-tok-tok","schedule":"@daily"}`, ""},
		{`{"name":"b","type":"http","target":"https://jay:hunter2@example.com/health"}`, "https://example.com/health"},
		{`{"name":"c","type":"http","target":"https://ghp_abc@example.com/"}`, "https://example.com/"},
		{`{"name":"d","type":"http","target":"https://example.com/x?api_key=s3cret&page=2"}`, "https://example.com/x?api_key=REDACTED&page=2"},
		{`{"name":"e","type":"tcp","target":"jay:hunter2@db.lan:5432"}`, "db.lan:5432"},
		{`{"name":"f","type":"http","target":"https://example.com/"}`, "https://example.com/"},
	} {
		if got := services.ShownTarget(parseSpec(t, c.spec)); got != c.want {
			t.Errorf("%s: shown as %q, want %q", c.spec, got, c.want)
		}
	}
}

// A viewer's copy of a check has no secrets; the admin's keeps its target, and neither has the password.
func TestViewerSpecHasNoSecrets(t *testing.T) {
	hb := parseSpec(t, `{"name":"backup","type":"heartbeat","target":"tok-tok-tok-tok-tok","schedule":"@daily"}`)
	if v := hb.ForViewer(); v.Target != "" {
		t.Fatalf("a viewer gets the heartbeat's token: %+v", v)
	}
	if a := hb.Public(); a.Target != hb.Target {
		t.Fatalf("the admin loses the token: %+v", a)
	}
	redis := parseSpec(t, `{"name":"cache","type":"redis","target":"cache.lan","password":"pw"}`)
	if v := redis.ForViewer(); v.Password != "" || !v.PasswordSet {
		t.Fatalf("viewer redis: %+v", v)
	}
}

// A backup without secrets leaves them out, and restoring it over the same check puts them back.
// A check whose target changed in the meantime is restored as the backup has it.
func TestBackupSecretsComeBackOnlyToTheSameCheck(t *testing.T) {
	here := parseSpec(t, `{"name":"api","type":"http","target":"https://jay:hunter2@example.com/"}`)
	saved := here.WithoutSecrets()
	if saved.Target != "https://example.com/" {
		t.Fatalf("the password went into the backup: %q", saved.Target)
	}
	back := saved
	back.KeepSecrets(here, true)
	if back.Target != here.Target {
		t.Fatalf("restoring loses the password: %q", back.Target)
	}
	moved := saved
	moved.KeepSecrets(parseSpec(t, `{"name":"api","type":"http","target":"https://jay:hunter2@other.example.com/"}`), true)
	if moved.Target != "https://example.com/" {
		t.Fatalf("another address's password went to this one: %q", moved.Target)
	}

	hb := parseSpec(t, `{"name":"backup","type":"heartbeat","target":"tok-tok-tok-tok-tok","schedule":"@daily"}`)
	out := hb.WithoutSecrets()
	if out.Target != "" {
		t.Fatalf("the token went into the backup: %q", out.Target)
	}
	out.KeepSecrets(hb, true)
	if out.Target != hb.Target {
		t.Fatalf("the heartbeat lost its token: %q", out.Target)
	}
	fresh := hb.WithoutSecrets()
	fresh.KeepSecrets(services.Spec{}, false)
	if fresh.Target != "" {
		t.Fatalf("a heartbeat new here got a token: %q", fresh.Target)
	}
}

// The services list is read by viewers too: it never carries a check's secrets.
func TestSummariesCarryNoSecrets(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"backup","type":"heartbeat","target":"tok-tok-tok-tok-tok","schedule":"@daily"}`))
	m.Save("", parseSpec(t, `{"name":"api","type":"http","target":"https://jay:hunter2@example.com/"}`))
	for _, row := range m.Summaries(1000) {
		if want := map[string]string{"backup": "", "api": "https://example.com/"}[row.Name]; row.Target != want {
			t.Errorf("%s: target %q, want %q", row.Name, row.Target, want)
		}
	}
}

// A result for a check paused or deleted while it ran is dropped, and RecordAs says so: its alert
// and repair would act on a check the owner has just taken over.
func TestDroppedResultSaysSo(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","retries":0}`))
	m.Save("", parseSpec(t, `{"name":"web","type":"tcp","target":"127.0.0.1:2","retries":0}`))
	if len(m.Claim(1000)) != 2 {
		t.Fatal("both checks run")
	}
	m.Save("api", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","retries":0,"paused":true}`))
	m.Delete("web")
	for _, name := range []string{"api", "web"} {
		if _, change, recorded := m.RecordAs(name, services.Result{Detail: "refused"}, 1001); recorded || change != "" {
			t.Errorf("%s: recorded %v, change %q", name, recorded, change)
		}
	}
	m.Save("api", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","retries":0}`))
	m.Claim(2000)
	if _, change, recorded := m.RecordAs("api", services.Result{Detail: "refused"}, 2000); !recorded || change != services.StatusDown {
		t.Fatalf("a result for a running check: recorded %v, change %q", recorded, change)
	}
}
