package plugins_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/plugins"
)

type memStore map[string]string

func (m memStore) Meta(k string) string      { return m[k] }
func (m memStore) SetMeta(k, v string) error { m[k] = v; return nil }

// plugin writes a plugin's folder: its plugin.json and a shell program.
func plugin(t *testing.T, root, id, manifest, script string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func setup(t *testing.T) (*plugins.Manager, string, memStore) {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	store := memStore{}
	return plugins.New(root, store), root, store
}

func on(t *testing.T, m *plugins.Manager, id string, settings map[string]string) {
	t.Helper()
	yes := true
	if err := m.Configure(id, plugins.Change{Enabled: &yes, Settings: settings}); err != nil {
		t.Fatalf("turn on %s: %v", id, err)
	}
}

func find(m *plugins.Manager, id string) plugins.View {
	for _, v := range m.List() {
		if v.ID == id {
			return v
		}
	}
	return plugins.View{}
}

func TestScanSaysWhyAPluginCannotRun(t *testing.T) {
	m, root, _ := setup(t)
	plugin(t, root, "good", `{"id":"good","name":"Good","provides":["check"],"run":"run.sh"}`, "echo ok\n")
	plugin(t, root, "noprog", `{"id":"noprog","provides":["card"],"run":"missing.sh"}`, "")
	plugin(t, root, "badkind", `{"id":"badkind","provides":["dance"],"run":"run.sh"}`, "true\n")
	plugin(t, root, "notjson", `{"id":`, "")
	open := plugin(t, root, "open", `{"id":"open","provides":["check"],"run":"run.sh"}`, "true\n")
	os.Chmod(filepath.Join(open, "run.sh"), 0o777)
	escape := plugin(t, root, "escape", `{"id":"escape","provides":["check"],"run":"run.sh"}`, "")
	os.Symlink("/bin/true", filepath.Join(escape, "run.sh"))
	os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)

	if err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"good":    "",
		"noprog":  "missing.sh is missing",
		"badkind": `it provides "dance"`,
		"notjson": "not valid JSON",
		"open":    "can be changed by other users",
		"escape":  "outside the plugin's folder",
	}
	list := m.List()
	if len(list) != len(want) {
		t.Fatalf("found %d plugins: %+v", len(list), list)
	}
	for id, problem := range want {
		p := find(m, id)
		if problem == "" && p.Problem != "" || problem != "" && !strings.Contains(p.Problem, problem) {
			t.Errorf("%s: problem %q, want %q", id, p.Problem, problem)
		}
	}
	yes := true
	if err := m.Configure("open", plugins.Change{Enabled: &yes}); err == nil {
		t.Fatal("a plugin that cannot run was turned on")
	}
}

func TestSettingsAndSecrets(t *testing.T) {
	m, root, store := setup(t)
	plugin(t, root, "svc", `{"id":"svc","provides":["check"],"run":"run.sh","settings":[
		{"key":"URL","label":"Address","required":true},
		{"key":"TOKEN","label":"Token","type":"secret"},
		{"key":"MODE","label":"Mode","type":"select","options":["fast","slow"],"default":"fast"}]}`,
		`printf '{"up":true,"detail":"%s %s %s"}' "$NIGHTJAR_SETTING_URL" "$NIGHTJAR_SETTING_TOKEN" "$NIGHTJAR_SETTING_MODE"`)
	m.Scan()

	yes := true
	if err := m.Configure("svc", plugins.Change{Enabled: &yes}); err == nil || !strings.Contains(err.Error(), "Address") {
		t.Fatalf("turned on without its required address: %v", err)
	}
	if err := m.Configure("svc", plugins.Change{Settings: map[string]string{"MODE": "medium"}}); err == nil {
		t.Fatal("accepted a choice that is not an option")
	}
	if err := m.Configure("nope", plugins.Change{}); err != plugins.ErrUnknown {
		t.Fatalf("unknown plugin: %v", err)
	}
	on(t, m, "svc", map[string]string{"URL": " http://x ", "TOKEN": "s3cret"})

	v := find(m, "svc")
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "s3cret") || len(v.SecretsSet) != 1 || v.Values["URL"] != "http://x" || v.Values["MODE"] != "fast" {
		t.Fatalf("view: %s", raw)
	}
	// An empty secret keeps the one saved.
	m.Configure("svc", plugins.Change{Settings: map[string]string{"TOKEN": ""}})
	if r := m.RunCheck(context.Background(), "svc", ""); !r.Up || r.Detail != "http://x s3cret fast" {
		t.Fatalf("settings reach the program: %+v", r)
	}
	m.Configure("svc", plugins.Change{Clear: []string{"TOKEN"}})
	if v := find(m, "svc"); len(v.SecretsSet) != 0 {
		t.Fatal("secret not cleared")
	}

	// The choices survive a restart.
	again := plugins.New(root, store)
	again.Scan()
	if !again.Enabled("svc") {
		t.Fatal("state not kept")
	}
}

func TestChecks(t *testing.T) {
	m, root, _ := setup(t)
	plugin(t, root, "json", `{"id":"json","provides":["check"],"run":"run.sh"}`,
		`[ "$NIGHTJAR_ARGUMENT" = up ] && echo '{"up":true,"detail":"fine","latency_ms":12}' || echo '{"up":false,"detail":"broken"}'`+"\n")
	plugin(t, root, "exit", `{"id":"exit","provides":["check"],"run":"run.sh"}`, "echo starting\necho 'disk is fine'\n[ \"$1\" = check ]\n")
	plugin(t, root, "fails", `{"id":"fails","provides":["check"],"run":"run.sh"}`, "echo 'cannot reach it' >&2\nexit 3\n")
	plugin(t, root, "slow", `{"id":"slow","provides":["check"],"run":"run.sh","timeout":1}`, "sleep 30 &\nsleep 30\n")
	plugin(t, root, "off", `{"id":"off","provides":["check"],"run":"run.sh"}`, "echo hi\n")
	m.Scan()
	for _, id := range []string{"json", "exit", "fails", "slow"} {
		on(t, m, id, nil)
	}
	ctx := context.Background()

	if r := m.RunCheck(ctx, "json", "up"); !r.Up || r.Detail != "fine" || r.LatencyMS == nil || *r.LatencyMS != 12 {
		t.Fatalf("json up: %+v", r)
	}
	if r := m.RunCheck(ctx, "json", "down"); r.Up || r.Detail != "broken" {
		t.Fatalf("json down: %+v", r)
	}
	if r := m.RunCheck(ctx, "exit", ""); !r.Up || r.Detail != "disk is fine" {
		t.Fatalf("exit code: %+v", r)
	}
	if r := m.RunCheck(ctx, "fails", ""); r.Up || !strings.Contains(r.Detail, "status 3: cannot reach it") {
		t.Fatalf("failure: %+v", r)
	}
	start := time.Now()
	if r := m.RunCheck(ctx, "slow", ""); r.Up || !strings.Contains(r.Detail, "was stopped") || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %+v after %s", r, time.Since(start))
	}
	if r := m.RunCheck(ctx, "off", ""); r.Up || !strings.Contains(r.Detail, "turned off") {
		t.Fatalf("off: %+v", r)
	}
	if r := m.RunCheck(plugins.Trying(ctx), "off", ""); !r.Up {
		t.Fatalf("trying a plugin that is off runs it: %+v", r)
	}
	if r := m.RunCheck(ctx, "gone", ""); r.Up || !strings.Contains(r.Detail, "no plugin") {
		t.Fatalf("missing: %+v", r)
	}
	if last := find(m, "fails").Last; last == nil || last.OK || last.Mode != "check" {
		t.Fatalf("last run: %+v", last)
	}
}

func TestCards(t *testing.T) {
	m, root, _ := setup(t)
	plugin(t, root, "card", `{"id":"card","name":"Speed","provides":["card"],"run":"run.sh"}`,
		`echo '{"title":"Internet","link":"javascript:alert(1)","items":[{"label":"Down","value":"930 Mbps","tone":"good"},{"label":"Ping","value":"9 ms","tone":"purple"}]}'`+"\n")
	plugin(t, root, "text", `{"id":"text","provides":["card"],"run":"run.sh"}`, "echo 'Backups ran at 3:00'\n")
	plugin(t, root, "broken", `{"id":"broken","provides":["card"],"run":"run.sh"}`, "exit 1\n")
	m.Scan()
	for _, id := range []string{"card", "text", "broken"} {
		on(t, m, id, nil)
	}
	m.Refresh(context.Background())
	cards := m.Cards()
	if len(cards) != 3 {
		t.Fatalf("cards: %+v", cards)
	}
	byID := map[string]plugins.CardData{}
	for _, c := range cards {
		byID[c.Plugin] = c
	}
	c := byID["card"]
	if c.Title != "Internet" || c.Link != "" || len(c.Items) != 2 || c.Items[0].Tone != "good" || c.Items[1].Tone != "" {
		t.Fatalf("card: %+v", c)
	}
	if byID["text"].Text != "Backups ran at 3:00" || byID["broken"].Error == "" {
		t.Fatalf("text and broken: %+v", byID)
	}
	no := false
	m.Configure("card", plugins.Change{Enabled: &no})
	if len(m.Cards()) != 2 {
		t.Fatal("a card turned off still shows")
	}
}

func TestNotify(t *testing.T) {
	m, root, _ := setup(t)
	dir := plugin(t, root, "log", `{"id":"log","provides":["notify"],"run":"run.sh"}`, "cat > got.json\necho \"$NIGHTJAR_ALERT_SEVERITY $NIGHTJAR_ALERT_TITLE\" > env.txt\n")
	m.Scan()
	if err := m.Send(context.Background(), "log", map[string]any{"title": "Disk full"}); err != plugins.ErrDisabled {
		t.Fatalf("sent while off: %v", err)
	}
	on(t, m, "log", nil)
	if err := m.Send(context.Background(), "log", map[string]any{"title": "Disk full", "severity": "critical"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Mode  string         `json:"mode"`
		Alert map[string]any `json:"alert"`
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "got.json"))
	env, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	if json.Unmarshal(raw, &got) != nil || got.Mode != "notify" || got.Alert["title"] != "Disk full" || strings.TrimSpace(string(env)) != "critical Disk full" {
		t.Fatalf("alert on stdin %s, env %s", raw, env)
	}
}

// The examples Nightjar ships load and run as they are.
func TestExamples(t *testing.T) {
	m := plugins.New("../../../examples/plugins", nil)
	if err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	for _, v := range m.List() {
		if v.Problem != "" {
			t.Fatalf("%s: %s", v.ID, v.Problem)
		}
	}
	ctx := plugins.Trying(context.Background())
	if r := m.RunCheck(ctx, "dns-resolve", "localhost"); !r.Up || !strings.HasPrefix(r.Detail, "localhost is ") {
		t.Fatalf("dns-resolve: %+v", r)
	}
	if r := m.RunCheck(ctx, "dns-resolve", "nothing.invalid"); r.Up {
		t.Fatalf("dns-resolve of a name that cannot resolve: %+v", r)
	}
	if c := m.RunCard(ctx, "load-card"); c.Error != "" || len(c.Items) != 5 {
		t.Fatalf("load-card: %+v", c)
	}
}
