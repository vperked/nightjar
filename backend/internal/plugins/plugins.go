// Package plugins runs the owner's own extensions. A plugin is a folder in the plugins folder with
// a plugin.json describing it and a program that does the work. It can add a check type, a card on
// the overview and an alert channel. Nightjar runs the program with the plugin's settings, reads
// what it prints and kills it if it takes too long. A plugin only runs once it is turned on.
package plugins

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// What a plugin can provide.
const (
	Check  = "check"
	Card   = "card"
	Notify = "notify"
)

// Setting is one value the owner fills in for a plugin.
type Setting struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // text, secret, number, bool or select
	Default     string   `json:"default,omitempty"`
	Help        string   `json:"help,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// CheckInfo describes a plugin's check: what its argument is, if it takes one.
type CheckInfo struct {
	Argument    string `json:"argument,omitempty"` // a label, e.g. "Host name"; empty for none
	Placeholder string `json:"placeholder,omitempty"`
}

// Manifest is plugin.json.
type Manifest struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Author      string     `json:"author,omitempty"`
	Homepage    string     `json:"homepage,omitempty"`
	Provides    []string   `json:"provides"`
	Run         string     `json:"run"`
	Interval    float64    `json:"interval,omitempty"` // how often a card refreshes, in seconds
	Timeout     float64    `json:"timeout,omitempty"`  // how long one run may take, in seconds
	Settings    []Setting  `json:"settings,omitempty"`
	Check       *CheckInfo `json:"check,omitempty"`
}

// Plugin is one plugin found on disk. Problem says why it cannot run, when it cannot.
type Plugin struct {
	Manifest
	Dir     string `json:"dir"`
	Problem string `json:"problem,omitempty"`
}

// State is what the owner chose for a plugin: whether it runs and its settings.
type State struct {
	Enabled  bool              `json:"enabled"`
	Settings map[string]string `json:"settings,omitempty"`
}

// CardItem is one line of a plugin's card.
type CardItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Tone  string `json:"tone,omitempty"` // good, warn or crit
	Hint  string `json:"hint,omitempty"`
}

// CardData is what a plugin's card shows.
type CardData struct {
	Plugin  string     `json:"plugin"`
	Title   string     `json:"title"`
	Text    string     `json:"text,omitempty"`
	Items   []CardItem `json:"items,omitempty"`
	Link    string     `json:"link,omitempty"`
	Updated float64    `json:"updated"`
	Error   string     `json:"error,omitempty"`
}

// LastRun is how the plugin's most recent run went.
type LastRun struct {
	Mode   string  `json:"mode"`
	At     float64 `json:"at"`
	OK     bool    `json:"ok"`
	Output string  `json:"output,omitempty"` // what it said, or why it failed
	MS     float64 `json:"ms"`
}

// Store keeps the plugins' states.
type Store interface {
	Meta(key string) string
	SetMeta(key, value string) error
}

const stateKey = "plugins.state"

var (
	idRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	keyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,39}$`)

	ErrUnknown  = errors.New("no plugin by that name")
	ErrDisabled = errors.New("the plugin is turned off")
)

// Limits on what a plugin may do.
const (
	defaultTimeout  = 20.0
	maxTimeout      = 120.0
	defaultInterval = 300.0
	minInterval     = 30.0
	maxOutput       = 64 << 10
	maxItems        = 12
)

// Manager finds plugins, keeps their states and runs them.
type Manager struct {
	dir   string
	store Store
	now   func() time.Time

	mu      sync.Mutex
	plugins map[string]*Plugin
	state   map[string]State
	cards   map[string]CardData
	last    map[string]LastRun
	due     map[string]time.Time
}

// New is a manager for the plugins in dir. Call Scan to find them.
func New(dir string, store Store) *Manager {
	m := &Manager{dir: dir, store: store, now: time.Now, plugins: map[string]*Plugin{}, state: map[string]State{}, cards: map[string]CardData{}, last: map[string]LastRun{}, due: map[string]time.Time{}}
	if store != nil {
		_ = json.Unmarshal([]byte(store.Meta(stateKey)), &m.state)
	}
	return m
}

// Dir is the plugins folder, absolute when it can be.
func (m *Manager) Dir() string {
	if abs, err := filepath.Abs(m.dir); err == nil {
		return abs
	}
	return m.dir
}

// Scan reads the plugins folder again. A missing folder is no plugins.
func (m *Manager) Scan() error {
	found := map[string]*Plugin{}
	entries, err := os.ReadDir(m.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := load(filepath.Join(m.dir, e.Name()))
		if other, ok := found[p.ID]; ok {
			p.Problem = "another plugin, in " + other.Dir + ", has the same id"
		}
		found[p.ID] = p
	}
	m.mu.Lock()
	m.plugins = found
	for id := range m.cards {
		if found[id] == nil {
			delete(m.cards, id)
		}
	}
	m.mu.Unlock()
	return nil
}

// load reads one plugin's folder. A plugin that cannot run still loads, with its problem.
func load(dir string) *Plugin {
	p := &Plugin{Dir: dir}
	p.ID = filepath.Base(dir)
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		p.Problem = "it has no plugin.json"
		return p
	}
	if err := json.Unmarshal(raw, &p.Manifest); err != nil {
		p.ID = filepath.Base(dir)
		p.Problem = "plugin.json is not valid JSON: " + err.Error()
		return p
	}
	if p.ID == "" {
		p.ID = filepath.Base(dir)
	}
	p.Name = cmp.Or(strings.TrimSpace(p.Name), p.ID)
	p.Timeout = min(maxTimeout, max(1, cmp.Or(p.Timeout, defaultTimeout)))
	p.Interval = max(minInterval, cmp.Or(p.Interval, defaultInterval))
	p.Problem = p.problem()
	return p
}

// problem is why a plugin cannot run, or "".
func (p *Plugin) problem() string {
	if !idRE.MatchString(p.ID) {
		return "its id must be lowercase letters, digits and dashes"
	}
	if len(p.Provides) == 0 {
		return `"provides" must list check, card or notify`
	}
	for _, what := range p.Provides {
		if what != Check && what != Card && what != Notify {
			return fmt.Sprintf("it provides %q; a plugin can provide check, card or notify", what)
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Settings {
		if !keyRE.MatchString(s.Key) {
			return fmt.Sprintf("setting %q: keys are capital letters, digits and underscores", s.Key)
		}
		if seen[s.Key] {
			return fmt.Sprintf("setting %s is listed twice", s.Key)
		}
		seen[s.Key] = true
		if !slices.Contains([]string{"", "text", "secret", "number", "bool", "select"}, s.Type) {
			return fmt.Sprintf("setting %s has an unknown type %q", s.Key, s.Type)
		}
	}
	if p.Run == "" {
		return `"run" must name the program to run`
	}
	prog, err := p.program()
	if err != nil {
		return err.Error()
	}
	// Like sshd: a plugin others could change is not run, since it runs as Nightjar.
	for _, path := range []string{p.Dir, filepath.Join(p.Dir, "plugin.json"), prog} {
		info, err := os.Stat(path)
		if err != nil {
			return err.Error()
		}
		if info.Mode().Perm()&0o022 != 0 {
			return filepath.Base(path) + " can be changed by other users; run chmod go-w on it"
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
			return filepath.Base(path) + " belongs to another user; it must belong to root or to the user Nightjar runs as"
		}
	}
	if info, _ := os.Stat(prog); info != nil && info.Mode().Perm()&0o111 == 0 {
		return p.Run + " is not executable; run chmod +x on it"
	}
	return ""
}

// program is the plugin's program, which must be inside its folder.
func (p *Plugin) program() (string, error) {
	dir, err := filepath.EvalSymlinks(p.Dir)
	if err != nil {
		return "", err
	}
	prog, err := filepath.EvalSymlinks(filepath.Join(dir, p.Run))
	if err != nil {
		return "", fmt.Errorf("%s is missing", p.Run)
	}
	if rel, err := filepath.Rel(dir, prog); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is outside the plugin's folder", p.Run)
	}
	return prog, nil
}

func (p *Plugin) provides(what string) bool { return slices.Contains(p.Provides, what) }

// View is a plugin as the dashboard sees it. Secret settings never leave; only whether they are set.
type View struct {
	Plugin
	Enabled    bool              `json:"enabled"`
	Values     map[string]string `json:"values"`
	SecretsSet []string          `json:"secrets_set"`
	Missing    []string          `json:"missing,omitempty"` // required settings still empty
	Last       *LastRun          `json:"last,omitempty"`
}

// List is every plugin found, by name.
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []View{}
	for _, p := range m.plugins {
		st := m.state[p.ID]
		v := View{Plugin: *p, Enabled: st.Enabled && p.Problem == "", Values: map[string]string{}, SecretsSet: []string{}, Missing: missing(p, st)}
		for _, s := range p.Settings {
			val, ok := st.Settings[s.Key]
			if s.Type == "secret" {
				if ok && val != "" {
					v.SecretsSet = append(v.SecretsSet, s.Key)
				}
				continue
			}
			if !ok {
				val = s.Default
			}
			v.Values[s.Key] = val
		}
		if l, ok := m.last[p.ID]; ok {
			v.Last = &l
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func missing(p *Plugin, st State) []string {
	var out []string
	for _, s := range p.Settings {
		if s.Required && strings.TrimSpace(cmp.Or(st.Settings[s.Key], s.Default)) == "" {
			out = append(out, s.Label)
		}
	}
	return out
}

// Get is one plugin, if it was found.
func (m *Manager) Get(id string) (Plugin, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	if !ok {
		return Plugin{}, false
	}
	return *p, true
}

// Enabled is whether a plugin is found, can run and is turned on.
func (m *Manager) Enabled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	return ok && p.Problem == "" && m.state[id].Enabled
}

// Providing is the ids of every plugin that provides what, on or off.
func (m *Manager) Providing(what string) []Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Plugin
	for _, p := range m.plugins {
		if p.provides(what) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Change is what the dashboard sends to change a plugin. Settings it leaves out are kept, and so
// are secrets sent empty; Clear empties secrets.
type Change struct {
	Enabled  *bool             `json:"enabled"`
	Settings map[string]string `json:"settings"`
	Clear    []string          `json:"clear"`
}

// Configure applies a change and saves it.
func (m *Manager) Configure(id string, c Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	if !ok {
		return ErrUnknown
	}
	st := m.state[id]
	next := map[string]string{}
	for k, v := range st.Settings {
		next[k] = v
	}
	byKey := map[string]Setting{}
	for _, s := range p.Settings {
		byKey[s.Key] = s
	}
	for k, v := range c.Settings {
		s, ok := byKey[k]
		if !ok {
			return fmt.Errorf("%s has no setting %s", p.Name, k)
		}
		if len(v) > 4000 {
			return fmt.Errorf("%s is too long", s.Label)
		}
		if s.Type == "secret" && v == "" {
			continue
		}
		v = strings.TrimSpace(v)
		switch s.Type {
		case "number":
			if _, err := strconv.ParseFloat(v, 64); v != "" && err != nil {
				return fmt.Errorf("%s must be a number", s.Label)
			}
		case "bool":
			if v != "" && v != "true" && v != "false" {
				return fmt.Errorf("%s must be true or false", s.Label)
			}
		case "select":
			if v != "" && !slices.Contains(s.Options, v) {
				return fmt.Errorf("%s must be one of %s", s.Label, strings.Join(s.Options, ", "))
			}
		}
		next[k] = v
	}
	for _, k := range c.Clear {
		delete(next, k)
	}
	st.Settings = next
	if c.Enabled != nil {
		if *c.Enabled && p.Problem != "" {
			return fmt.Errorf("%s cannot run: %s", p.Name, p.Problem)
		}
		st.Enabled = *c.Enabled
	}
	if miss := missing(p, st); st.Enabled && len(miss) > 0 {
		return fmt.Errorf("%s needs %s first", p.Name, strings.Join(miss, ", "))
	}
	saved := map[string]State{}
	for k, v := range m.state {
		saved[k] = v
	}
	saved[id] = st
	if m.store != nil {
		data, _ := json.Marshal(saved)
		if err := m.store.SetMeta(stateKey, string(data)); err != nil {
			return err
		}
	}
	m.state = saved
	if !st.Enabled {
		delete(m.cards, id)
	}
	m.due[id] = time.Time{} // a changed card refreshes on the next round
	return nil
}

// input is what a plugin reads on its standard input.
type input struct {
	Mode     string            `json:"mode"`
	Settings map[string]string `json:"settings"`
	Argument string            `json:"argument,omitempty"`
	Alert    any               `json:"alert,omitempty"`
}

// try is set on a context to run a plugin that is turned off, when the owner tries it.
type tryKey struct{}

// Trying is ctx for running a plugin once from the dashboard, even while it is turned off.
func Trying(ctx context.Context) context.Context { return context.WithValue(ctx, tryKey{}, true) }

// run runs a plugin's program in mode. It is killed, with everything it started, after its timeout.
func (m *Manager) run(ctx context.Context, id, mode string, in input) ([]byte, error) {
	m.mu.Lock()
	p, ok := m.plugins[id]
	st := m.state[id]
	var plugin Plugin
	if ok {
		plugin = *p
	}
	m.mu.Unlock()
	switch {
	case !ok:
		return nil, ErrUnknown
	case plugin.Problem != "":
		return nil, errors.New(plugin.Problem)
	case !st.Enabled && ctx.Value(tryKey{}) == nil:
		return nil, ErrDisabled
	case !plugin.provides(mode):
		return nil, fmt.Errorf("%s has no %s", plugin.Name, mode)
	}
	prog, err := plugin.program()
	if err != nil {
		return nil, err
	}
	in.Mode = mode
	in.Settings = map[string]string{}
	env := []string{"PATH=" + cmp.Or(os.Getenv("PATH"), "/usr/local/bin:/usr/bin:/bin"), "LANG=C.UTF-8", "HOME=" + plugin.Dir,
		"NIGHTJAR_MODE=" + mode, "NIGHTJAR_PLUGIN_DIR=" + plugin.Dir, "NIGHTJAR_ARGUMENT=" + in.Argument}
	// An alert's main parts are in the environment too, for programs that would rather not read JSON.
	if a, ok := in.Alert.(map[string]any); ok {
		for _, k := range []string{"title", "description", "severity", "category"} {
			if v, ok := a[k].(string); ok {
				env = append(env, "NIGHTJAR_ALERT_"+strings.ToUpper(k)+"="+v)
			}
		}
	}
	for _, s := range plugin.Settings {
		v, ok := st.Settings[s.Key]
		if !ok {
			v = s.Default
		}
		in.Settings[s.Key] = v
		env = append(env, "NIGHTJAR_SETTING_"+s.Key+"="+v)
	}
	body, _ := json.Marshal(in)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(plugin.Timeout*float64(time.Second)))
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, mode)
	cmd.Dir, cmd.Env, cmd.Stdin = plugin.Dir, env, bytes.NewReader(body)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr capped
	stdout.limit, stderr.limit = maxOutput, 4<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := m.now()
	err = cmd.Run()
	ms := float64(time.Since(start).Microseconds()) / 1000
	out := stdout.Bytes()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("it took longer than %.0fs and was stopped", time.Since(start).Seconds())
	} else if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("it exited with status %d%s", exit.ExitCode(), said(stderr.String(), string(out)))
		}
	}
	rec := LastRun{Mode: mode, At: float64(start.UnixMilli()) / 1000, OK: err == nil, MS: ms, Output: lastLine(string(out))}
	if err != nil {
		rec.Output = err.Error()
	}
	m.mu.Lock()
	m.last[id] = rec
	m.mu.Unlock()
	return out, err
}

// said is the last thing a failed program wrote, for its error.
func said(stderr, stdout string) string {
	if l := lastLine(cmp.Or(strings.TrimSpace(stderr), strings.TrimSpace(stdout))); l != "" {
		return ": " + l
	}
	return ""
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300] + "…"
	}
	return l
}

// capped keeps the first limit bytes written and drops the rest.
type capped struct {
	bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// CheckResult is one run of a plugin's check.
type CheckResult struct {
	Up        bool     `json:"up"`
	Detail    string   `json:"detail"`
	LatencyMS *float64 `json:"latency_ms"`
}

// RunCheck runs a plugin's check with its argument. The program prints {"up", "detail",
// "latency_ms"}, or anything at all and exits 0 when up and non-zero when down.
func (m *Manager) RunCheck(ctx context.Context, id, argument string) CheckResult {
	start := m.now()
	out, err := m.run(ctx, id, Check, input{Argument: argument})
	ms := float64(time.Since(start).Microseconds()) / 1000
	if errors.Is(err, ErrUnknown) {
		return CheckResult{Detail: fmt.Sprintf("no plugin %q is installed", id)}
	}
	if errors.Is(err, ErrDisabled) {
		return CheckResult{Detail: fmt.Sprintf("the %s plugin is turned off", id)}
	}
	var printed struct {
		Up        *bool    `json:"up"`
		Detail    string   `json:"detail"`
		LatencyMS *float64 `json:"latency_ms"`
	}
	var r CheckResult
	if json.Unmarshal(bytes.TrimSpace(out), &printed) == nil && printed.Up != nil {
		r = CheckResult{Up: *printed.Up && err == nil, Detail: printed.Detail, LatencyMS: printed.LatencyMS}
		if err != nil && r.Detail == "" {
			r.Detail = "plugin: " + err.Error()
		}
	} else {
		r = CheckResult{Up: err == nil, Detail: lastLine(string(out))}
		if err != nil {
			r.Detail = "plugin: " + err.Error()
		}
	}
	if r.LatencyMS == nil {
		r.LatencyMS = &ms
	}
	if len(r.Detail) > 300 {
		r.Detail = r.Detail[:300] + "…"
	}
	m.said(id, r.Detail)
	return r
}

// said puts what a run came to, rather than what it printed, in its last run.
func (m *Manager) said(id, what string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.last[id]; ok {
		l.Output = what
		m.last[id] = l
	}
}

// RunCard runs a plugin's card now and keeps what it shows.
func (m *Manager) RunCard(ctx context.Context, id string) CardData {
	p, _ := m.Get(id)
	out, err := m.run(ctx, id, Card, input{})
	c := CardData{Plugin: id, Title: p.Name, Updated: float64(m.now().UnixMilli()) / 1000}
	if err == nil {
		var got CardData
		if jerr := json.Unmarshal(bytes.TrimSpace(out), &got); jerr != nil {
			got = CardData{Text: strings.TrimSpace(string(out))}
		}
		c.Title = cmp.Or(clip(got.Title, 60), p.Name)
		c.Text, c.Link = clip(got.Text, 600), got.Link
		if !strings.HasPrefix(c.Link, "http://") && !strings.HasPrefix(c.Link, "https://") {
			c.Link = ""
		}
		for _, it := range got.Items[:min(len(got.Items), maxItems)] {
			if it.Tone != "good" && it.Tone != "warn" && it.Tone != "crit" {
				it.Tone = ""
			}
			c.Items = append(c.Items, CardItem{Label: clip(it.Label, 60), Value: clip(it.Value, 80), Tone: it.Tone, Hint: clip(it.Hint, 120)})
		}
		m.said(id, fmt.Sprintf("%d items", len(c.Items)))
		if len(c.Items) == 0 {
			m.said(id, clip(c.Text, 120))
		}
	} else {
		c.Error = err.Error()
	}
	m.mu.Lock()
	if m.state[id].Enabled {
		m.cards[id] = c
	}
	m.mu.Unlock()
	return c
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Cards is every turned-on card's latest, by plugin name.
func (m *Manager) Cards() []CardData {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []CardData{}
	for id, c := range m.cards {
		if p, ok := m.plugins[id]; ok && m.state[id].Enabled && p.Problem == "" {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out
}

// Refresh runs every turned-on card that is due.
func (m *Manager) Refresh(ctx context.Context) {
	now := m.now()
	for _, p := range m.Providing(Card) {
		if !m.Enabled(p.ID) {
			continue
		}
		m.mu.Lock()
		due := m.due[p.ID]
		if now.Before(due) {
			m.mu.Unlock()
			continue
		}
		m.due[p.ID] = now.Add(time.Duration(p.Interval * float64(time.Second)))
		m.mu.Unlock()
		m.RunCard(ctx, p.ID)
	}
}

// Loop refreshes cards until ctx ends.
func (m *Manager) Loop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		m.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Send hands an alert to a plugin's notify program, which exits 0 once it is delivered.
func (m *Manager) Send(ctx context.Context, id string, alert any) error {
	_, err := m.run(ctx, id, Notify, input{Alert: alert})
	return err
}
