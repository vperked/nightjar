package updates

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Result is what a check found.
type Result struct {
	Installed string `json:"installed,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	// Pending lists what an update would bring: packages, snaps or commits.
	Pending []string `json:"pending,omitempty"`
}

// key identifies the update on offer, so it is announced and auto-installed once.
func (r Result) key() string {
	if r.Latest != "" {
		return r.Latest
	}
	return strings.Join(r.Pending, "\n")
}

// round is one pass over the targets; work they share (apt's package lists, GitHub answers) happens once.
type round struct {
	aptRefreshed bool
	aptErr       error
	latest       map[string]string
}

var (
	sandboxedRE  = regexp.MustCompile(`setresuid|cannot set user id|Failed to set new user ids`)
	errSandboxed = errors.New("not allowed to switch users here, which apt and git need: the monitor's systemd unit sets NoNewPrivileges with User=root")
)

// run is a command runner with the manager's defaults filled in.
func (m *Manager) run(ctx context.Context, c Command, okCodes ...int) (Output, error) {
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Minute
	}
	out, err := m.opt.Run(ctx, c)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return out, fmt.Errorf("%s timed out after %s", c.Argv[0], c.Timeout)
	case err != nil:
		return out, err
	case out.Code != 0 && !slices.Contains(okCodes, out.Code):
		if sandboxedRE.MatchString(out.Stderr + out.Stdout) {
			return out, errSandboxed
		}
		return out, fmt.Errorf("%s failed (exit %d): %s", strings.Join(c.Argv[:min(2, len(c.Argv))], " "), out.Code, out.Tail(600))
	}
	return out, nil
}

func (m *Manager) probe(ctx context.Context, r *round, s Spec) (Result, error) {
	switch s.Kind {
	case KindSystem:
		return m.probeSystem(ctx, r)
	case KindSnap:
		return m.probeSnap(ctx)
	case KindApt:
		return m.probeApt(ctx, r, s.Package)
	case KindArr:
		return m.probeArr(ctx, s)
	case KindGit:
		return m.probeGit(ctx, s)
	case KindRelease:
		return m.probeRelease(ctx, r, s)
	}
	return Result{}, fmt.Errorf("unknown kind %q", s.Kind)
}

// install applies the update the last check found and returns a short note on what it did.
func (m *Manager) install(ctx context.Context, s Spec, res Result) (string, error) {
	long := m.opt.Timeout
	switch s.Kind {
	case KindSystem:
		pm, ok := findPackageManager()
		if !ok {
			return "", errNoPackageManager
		}
		_, err := m.run(ctx, Command{Argv: pm.upgrade, Env: pm.env, Root: true, Timeout: long})
		return fmt.Sprintf("%d package(s) upgraded with %s", len(res.Pending), pm.name), err
	case KindSnap:
		_, err := m.run(ctx, Command{Argv: []string{"snap", "refresh"}, Root: true, Timeout: long})
		return fmt.Sprintf("%d snap(s) refreshed", len(res.Pending)), err
	case KindApt:
		_, err := m.run(ctx, Command{Argv: append(aptInstall, s.Package), Env: aptEnv, Root: true, Timeout: long})
		return "", err
	case KindArr:
		return "", m.installArr(ctx, s, res)
	case KindGit:
		return m.installGit(ctx, s)
	case KindRelease:
		_, err := m.run(ctx, Command{Argv: shell(s.UpdateCommand), Env: versionEnv(res.Latest), Root: true, Timeout: long})
		return "", err
	}
	return "", fmt.Errorf("unknown kind %q", s.Kind)
}

func versionEnv(latest string) []string {
	return []string{"VERSION=" + ExtractVersion(latest), "TAG=" + latest}
}

// ---------- OS packages ----------

var (
	errNoPackageManager = errors.New("no supported package manager (apt-get, dnf, yum, pacman)")
	aptEnv              = []string{"DEBIAN_FRONTEND=noninteractive"}
	aptKeepConfig       = []string{"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	aptInstall          = slices.Concat([]string{"apt-get", "install", "-y", "--only-upgrade"}, aptKeepConfig)
	aptInstRE           = regexp.MustCompile(`(?m)^Inst (\S+) \[([^\]]+)\] \((\S+)`)
)

type packageManager struct {
	name, binary string
	env          []string
	pending      func(ctx context.Context, m *Manager, r *round) ([]string, error)
	upgrade      []string
}

var packageManagers = []packageManager{
	{
		name: "apt", binary: "apt-get", env: aptEnv,
		pending: func(ctx context.Context, m *Manager, r *round) ([]string, error) {
			if err := m.aptRefresh(ctx, r); err != nil {
				return nil, err
			}
			sim, err := m.run(ctx, Command{Argv: []string{"apt-get", "-s", "upgrade"}, Env: aptEnv})
			if err != nil {
				return nil, err
			}
			var out []string
			for _, mm := range aptInstRE.FindAllStringSubmatch(sim.Stdout, -1) {
				out = append(out, fmt.Sprintf("%s %s → %s", mm[1], mm[2], mm[3]))
			}
			return out, nil
		},
		upgrade: slices.Concat([]string{"apt-get", "-y"}, aptKeepConfig, []string{"upgrade"}),
	},
	rpmManager("dnf"),
	rpmManager("yum"),
	{
		name: "pacman", binary: "pacman",
		pending: func(ctx context.Context, m *Manager, _ *round) ([]string, error) {
			if _, err := m.run(ctx, Command{Argv: []string{"pacman", "-Sy", "--noconfirm"}, Root: true, Timeout: 10 * time.Minute}); err != nil {
				return nil, err
			}
			// -Qu exits 1 when nothing is upgradable.
			qu, err := m.run(ctx, Command{Argv: []string{"pacman", "-Qu"}}, 1)
			return nonEmptyLines(qu.Stdout, ""), err
		},
		upgrade: []string{"pacman", "-Su", "--noconfirm"},
	},
}

// rpmManager is dnf or yum: check-update exits 100 and lists packages when upgrades are pending.
func rpmManager(name string) packageManager {
	return packageManager{
		name: name, binary: name,
		pending: func(ctx context.Context, m *Manager, _ *round) ([]string, error) {
			chk, err := m.run(ctx, Command{Argv: []string{name, "-q", "check-update"}, Root: true, Timeout: 10 * time.Minute}, 100)
			if err != nil || chk.Code == 0 {
				return nil, err
			}
			return nonEmptyLines(chk.Stdout, "Last metadata"), nil
		},
		upgrade: []string{name, "-y", "upgrade"},
	}
}

func findPackageManager() (packageManager, bool) {
	for _, pm := range packageManagers {
		if _, err := exec.LookPath(pm.binary); err == nil {
			return pm, true
		}
	}
	return packageManager{}, false
}

func nonEmptyLines(s, skipPrefix string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && (skipPrefix == "" || !strings.HasPrefix(line, skipPrefix)) {
			out = append(out, strings.Join(strings.Fields(line), " "))
		}
	}
	return out
}

// aptRefresh downloads the package lists once per round.
func (m *Manager) aptRefresh(ctx context.Context, r *round) error {
	if !r.aptRefreshed {
		r.aptRefreshed = true
		_, r.aptErr = m.run(ctx, Command{Argv: []string{"apt-get", "update", "-qq"}, Env: aptEnv, Root: true, Timeout: 10 * time.Minute})
	}
	return r.aptErr
}

func (m *Manager) probeSystem(ctx context.Context, r *round) (Result, error) {
	pm, ok := findPackageManager()
	if !ok {
		return Result{}, errNoPackageManager
	}
	pending, err := pm.pending(ctx, m, r)
	return Result{Pending: pending, Available: len(pending) > 0}, err
}

func (m *Manager) probeSnap(ctx context.Context) (Result, error) {
	out, err := m.run(ctx, Command{Argv: []string{"snap", "refresh", "--list"}, Root: true})
	if err != nil {
		return Result{}, err
	}
	var pending []string
	for i, line := range nonEmptyLines(out.Stdout, "") {
		if f := strings.Fields(line); i > 0 && len(f) >= 2 { // the first line is the header
			pending = append(pending, f[0]+" "+f[1])
		}
	}
	return Result{Pending: pending, Available: len(pending) > 0}, nil
}

func (m *Manager) probeApt(ctx context.Context, r *round, pkg string) (Result, error) {
	if err := m.aptRefresh(ctx, r); err != nil {
		return Result{}, err
	}
	inst, err := m.run(ctx, Command{Argv: []string{"dpkg-query", "-W", "-f=${Version}", pkg}})
	if err != nil {
		return Result{}, fmt.Errorf("%s is not installed", pkg)
	}
	policy, err := m.run(ctx, Command{Argv: []string{"apt-cache", "policy", pkg}})
	if err != nil {
		return Result{}, err
	}
	res := Result{Installed: strings.TrimSpace(inst.Stdout)}
	for _, line := range strings.Split(policy.Stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
			res.Latest = strings.TrimSpace(v)
		}
	}
	if res.Latest == "" || res.Latest == "(none)" {
		res.Latest = res.Installed
	}
	if res.Latest != res.Installed {
		cmp, err := m.run(ctx, Command{Argv: []string{"dpkg", "--compare-versions", res.Latest, "gt", res.Installed}}, 1)
		res.Available = err == nil && cmp.Code == 0
	}
	if !res.Available {
		res.Latest = res.Installed
	}
	return res, nil
}

// ---------- Sonarr, Radarr, Prowlarr… ----------

type arrConfig struct {
	Port        int    `xml:"Port"`
	URLBase     string `xml:"UrlBase"`
	APIKey      string `xml:"ApiKey"`
	BindAddress string `xml:"BindAddress"`
}

// arrEndpoint is the app's API root (…/api/v3 or …/api/v1) and key.
func (m *Manager) arrEndpoint(ctx context.Context, s Spec) (string, string, error) {
	base, key := s.URL, s.APIKey
	if s.ConfigFile != "" && (base == "" || key == "") {
		data, err := os.ReadFile(s.ConfigFile)
		if err != nil {
			return "", "", err
		}
		var c arrConfig
		if err := xml.Unmarshal(data, &c); err != nil {
			return "", "", fmt.Errorf("%s: %v", s.ConfigFile, err)
		}
		if base == "" {
			urlBase := strings.Trim(c.URLBase, "/")
			if urlBase != "" {
				urlBase = "/" + urlBase
			}
			base = fmt.Sprintf("http://127.0.0.1:%d%s", c.Port, urlBase)
		}
		key = cmp.Or(key, c.APIKey)
	}
	if key == "" {
		return "", "", errors.New("no API key")
	}
	m.mu.Lock()
	api := m.arrAPI[s.ID]
	m.mu.Unlock()
	if api != "" {
		return base + api, key, nil
	}
	for _, v := range []string{"/api/v3", "/api/v1"} {
		var status struct{ Version string }
		if err := m.getJSON(ctx, base+v+"/system/status", key, &status); err == nil && status.Version != "" {
			m.mu.Lock()
			m.arrAPI[s.ID] = v
			m.mu.Unlock()
			return base + v, key, nil
		} else if !errors.Is(err, errNotFoundHTTP) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%s does not look like Sonarr, Radarr or Prowlarr", base)
}

type arrUpdate struct {
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	Installable bool   `json:"installable"`
}

func (m *Manager) probeArr(ctx context.Context, s Spec) (Result, error) {
	api, key, err := m.arrEndpoint(ctx, s)
	if err != nil {
		return Result{}, err
	}
	var status struct{ Version string }
	if err := m.getJSON(ctx, api+"/system/status", key, &status); err != nil {
		return Result{}, err
	}
	var list []arrUpdate
	if err := m.getJSON(ctx, api+"/update", key, &list); err != nil {
		return Result{}, err
	}
	res := Result{Installed: status.Version, Latest: status.Version}
	if len(list) > 0 && !list[0].Installed && list[0].Installable && Compare(list[0].Version, status.Version) > 0 {
		res.Latest, res.Available = list[0].Version, true
	}
	return res, nil
}

// installArr asks the app to update itself, then waits for it to come back on the new version.
func (m *Manager) installArr(ctx context.Context, s Spec, res Result) error {
	api, key, err := m.arrEndpoint(ctx, s)
	if err != nil {
		return err
	}
	if err := m.postJSON(ctx, api+"/command", key, map[string]string{"name": "ApplicationUpdate"}); err != nil {
		return err
	}
	deadline := time.Now().Add(min(m.opt.Timeout, 15*time.Minute))
	for time.Now().Before(deadline) {
		if !sleep(ctx, m.opt.Poll) {
			return ctx.Err()
		}
		var status struct{ Version string }
		// The app is unreachable while it restarts.
		if m.getJSON(ctx, api+"/system/status", key, &status) == nil && Compare(status.Version, res.Latest) >= 0 {
			return nil
		}
	}
	return fmt.Errorf("still not on %s after %s; see the app's System → Logs", res.Latest, min(m.opt.Timeout, 15*time.Minute))
}

// ---------- git checkouts ----------

// owner is the account that owns dir, so git runs as it and pulled files keep their owner.
func owner(dir string) string {
	info, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	u, err := user.LookupId(strconv.Itoa(int(st.Uid)))
	if err != nil {
		return ""
	}
	return u.Username
}

// gitUser is who runs git in a checkout: its owner, so pulled files keep their owner, except for
// the monitor's own checkout, which uses the monitor's account (and its GitHub login and toolchain).
func gitUser(s Spec) string {
	if s.Self {
		return ""
	}
	return owner(s.Dir)
}

// gitEnv makes git fail at once instead of waiting for a password nobody will type.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true"}

func (m *Manager) git(ctx context.Context, s Spec, timeout time.Duration, args ...string) (string, error) {
	out, err := m.run(ctx, Command{Argv: append([]string{"git", "-c", "safe.directory=*", "-C", s.Dir}, args...), User: gitUser(s), Env: gitEnv, Timeout: timeout})
	if err != nil && strings.Contains(out.Stderr, "could not read Username") {
		who := cmp.Or(gitUser(s), "the monitor's user")
		return "", fmt.Errorf("the remote needs a login that %s does not have (use an SSH remote, or log in with gh auth login as that user)", who)
	}
	return strings.TrimSpace(out.Stdout), err
}

func (m *Manager) probeGit(ctx context.Context, s Spec) (Result, error) {
	if _, err := m.git(ctx, s, 2*time.Minute, "fetch", "--quiet", "--tags"); err != nil {
		return Result{}, err
	}
	if _, err := m.git(ctx, s, 0, "rev-parse", "--abbrev-ref", "@{u}"); err != nil {
		return Result{}, errors.New("the checkout's branch has no upstream to update from")
	}
	var res Result
	var err error
	if res.Installed, err = m.git(ctx, s, 0, "describe", "--tags", "--always", "HEAD"); err != nil {
		return res, err
	}
	if res.Latest, err = m.git(ctx, s, 0, "describe", "--tags", "--always", "@{u}"); err != nil {
		return res, err
	}
	log, err := m.git(ctx, s, 0, "log", "--format=%h %s", "-n", "20", "HEAD..@{u}")
	if err != nil {
		return res, err
	}
	res.Pending = nonEmptyLines(log, "")
	res.Available = len(res.Pending) > 0
	if !res.Available {
		res.Latest = res.Installed
	}
	return res, nil
}

func (m *Manager) installGit(ctx context.Context, s Spec) (string, error) {
	before, _ := m.git(ctx, s, 0, "rev-parse", "HEAD")
	if _, err := m.git(ctx, s, 5*time.Minute, "pull", "--ff-only", "--quiet"); err != nil {
		return "", err
	}
	if s.UpdateCommand == "" {
		return "", nil
	}
	if _, err := m.run(ctx, Command{Argv: shell(s.UpdateCommand), Dir: s.Dir, User: gitUser(s), Timeout: m.opt.Timeout}); err != nil {
		return "", fmt.Errorf("pulled, but the build failed (to undo: git -C %s reset --hard %.12s): %w", s.Dir, before, err)
	}
	return "", nil
}

// ---------- GitHub releases ----------

func (m *Manager) probeRelease(ctx context.Context, r *round, s Spec) (Result, error) {
	out, err := m.run(ctx, Command{Argv: shell(s.VersionCommand), Root: true, Timeout: 30 * time.Second})
	if err != nil {
		return Result{}, err
	}
	installed := ExtractVersion(out.Stdout + " " + out.Stderr)
	if installed == "" {
		return Result{}, fmt.Errorf("the version command printed no version: %s", out.Tail(200))
	}
	tag, err := m.latestTag(ctx, r, s.Repo, s.TagPrefix)
	if err != nil {
		return Result{Installed: installed}, err
	}
	res := Result{Installed: installed, Latest: installed}
	if Compare(tag, installed) > 0 {
		res.Latest, res.Available = tag, true
	}
	return res, nil
}

// latestTag is the repository's newest release tag, or its highest tag starting with prefix.
func (m *Manager) latestTag(ctx context.Context, r *round, repo, prefix string) (string, error) {
	key := repo + "\x00" + prefix
	if t, ok := r.latest[key]; ok {
		return t, nil
	}
	api := strings.TrimRight(m.opt.GitHubAPI, "/") + "/repos/" + repo
	var tag string
	if prefix == "" {
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := m.getJSON(ctx, api+"/releases/latest", "", &rel); err != nil {
			return "", fmt.Errorf("GitHub %s: %w", repo, err)
		}
		tag = rel.TagName
	} else {
		var tags []struct{ Name string }
		if err := m.getJSON(ctx, api+"/tags?per_page=100", "", &tags); err != nil {
			return "", fmt.Errorf("GitHub %s: %w", repo, err)
		}
		for _, t := range tags {
			rest, ok := strings.CutPrefix(t.Name, prefix)
			if ok && ExtractVersion(rest) != "" && !prereleaseRE.MatchString(rest) && (tag == "" || Compare(rest, strings.TrimPrefix(tag, prefix)) > 0) {
				tag = t.Name
			}
		}
		tag = strings.TrimPrefix(tag, prefix)
	}
	if tag == "" {
		return "", fmt.Errorf("GitHub %s has no releases", repo)
	}
	r.latest[key] = tag
	return tag, nil
}

// ---------- HTTP ----------

var errNotFoundHTTP = errors.New("HTTP 404")

// getJSON fetches url into v. key is an *arr API key; GitHub requests carry the token instead.
func (m *Manager) getJSON(ctx context.Context, url, key string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return m.do(req, key, v)
}

func (m *Manager) postJSON(ctx context.Context, url, key string, body any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return m.do(req, key, nil)
}

func (m *Manager) do(req *http.Request, key string, v any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "servermonitor")
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	} else if m.opt.GitHubToken != "" && strings.HasPrefix(req.URL.String(), m.opt.GitHubAPI) {
		req.Header.Set("Authorization", "Bearer "+m.opt.GitHubToken)
	}
	resp, err := m.opt.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFoundHTTP
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("the API key was refused")
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-Ratelimit-Remaining") == "0":
		return errors.New("GitHub's hourly limit is used up; set GITHUB_TOKEN to raise it")
	case resp.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// ---------- services ----------

// verifyRunning restarts the unit when asked, then waits for systemd to report it active.
func (m *Manager) verifyRunning(ctx context.Context, s Spec) error {
	if s.Unit == "" || s.Self {
		return nil
	}
	if s.Restart && (s.Kind == KindGit || s.Kind == KindRelease) {
		if _, err := m.run(ctx, Command{Argv: []string{"systemctl", "restart", s.Unit}, Root: true}); err != nil {
			return &notRunningError{s.Unit, err.Error()}
		}
	}
	var state string
	// Active on two checks in a row, so a service that crashes on start does not pass.
	for i, streak := 0, 0; i < 30; i++ {
		out, _ := m.opt.Run(ctx, Command{Argv: []string{"systemctl", "is-active", s.Unit}, Timeout: 10 * time.Second})
		if state = strings.TrimSpace(out.Stdout); state == "active" {
			if streak++; streak == 2 {
				return nil
			}
		} else {
			streak = 0
		}
		if !sleep(ctx, m.opt.Poll) {
			return ctx.Err()
		}
	}
	return &notRunningError{s.Unit, "systemd reports it " + cmp.Or(state, "unknown")}
}

type notRunningError struct{ unit, detail string }

func (e *notRunningError) Error() string {
	return e.unit + " is not running after the update: " + e.detail
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
