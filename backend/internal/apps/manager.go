package apps

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"servermonitor/internal/logx"
)

var log = logx.Component("Apps")

var (
	ErrBusy      = errors.New("another install is running; wait for it to finish")
	ErrUnknown   = errors.New("no such app in the store")
	ErrInstalled = errors.New("already installed by another installer; manage it there")
	ErrNotOurs   = errors.New("the store did not install this app, so it will not remove it")
)

// Status is an app and its state on this machine.
type Status struct {
	App
	Installed bool `json:"installed"`
	// By is who installed it: "store", "swizzin" or "system" (a package or a hand-made unit).
	By      string `json:"by,omitempty"`
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	// Unit is its systemd service, when known.
	Unit string `json:"unit,omitempty"`
	// Available says whether the store can install it on this machine (its architecture has a build).
	Available bool `json:"available"`
}

// Job is one install or removal and its log.
type Job struct {
	ID       int      `json:"id"`
	App      string   `json:"app"`
	Action   string   `json:"action"` // install or remove
	Step     string   `json:"step"`
	Started  float64  `json:"started"`
	Finished float64  `json:"finished,omitempty"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Lines    []string `json:"lines"`
}

// Options set the manager up; zero values suit a real machine.
type Options struct {
	// StateDir holds the scripts the jobs run.
	StateDir string
	// GitHubToken raises GitHub's rate limit for finding releases.
	GitHubToken string
	// Run runs a script, sending each line of output to line. Tests replace it.
	Run func(ctx context.Context, name, script string, line func(string)) error
	// Systemctl runs systemctl and returns its output. Tests replace it.
	Systemctl func(args ...string) string
	// Processes lists the names of running programs (as /proc/<pid>/comm has them). Tests replace it.
	Processes func() []string
	// ManifestDir and LockDir are where store manifests and swizzin locks live.
	ManifestDir, LockDir string
	HTTP                 *http.Client
	Arch                 string
	// GitHubAPI is GitHub's API address; tests point it at a fake.
	GitHubAPI string
}

// Manager installs and removes store apps, one at a time. Its methods are safe for concurrent use.
type Manager struct {
	opt Options

	mu      sync.Mutex
	job     *Job
	running bool
	nextID  int
}

func New(opt Options) *Manager {
	opt.StateDir = cmp.Or(opt.StateDir, os.TempDir())
	if opt.Run == nil {
		dir := opt.StateDir
		opt.Run = func(ctx context.Context, name, script string, line func(string)) error {
			return runScript(ctx, dir, name, script, line)
		}
	}
	if opt.Systemctl == nil {
		opt.Systemctl = func(args ...string) string {
			out, _ := exec.Command("systemctl", args...).Output()
			return string(out)
		}
	}
	if opt.Processes == nil {
		opt.Processes = processNames
	}
	opt.ManifestDir = cmp.Or(opt.ManifestDir, ManifestDir)
	opt.LockDir = cmp.Or(opt.LockDir, "/install")
	opt.Arch = cmp.Or(opt.Arch, runtime.GOARCH)
	opt.GitHubAPI = cmp.Or(opt.GitHubAPI, "https://api.github.com")
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	return &Manager{opt: opt}
}

// ---------- status ----------

type manifest struct {
	Version string `json:"version"`
	Port    int    `json:"port"`
}

// Statuses reports every app in the catalog.
func (m *Manager) Statuses() []Status {
	files := m.opt.Systemctl("list-unit-files", "--no-legend", "--plain", "--type=service")
	active := m.opt.Systemctl("list-units", "--no-legend", "--plain", "--type=service", "--state=active")
	procs := m.opt.Processes()
	out := make([]Status, 0, len(Catalog))
	for _, a := range Catalog {
		out = append(out, m.status(a, unitNames(files), unitNames(active), procs))
	}
	return out
}

func (m *Manager) status(a App, files, active, procs []string) Status {
	s := Status{App: a, Available: a.source.urls[m.opt.Arch] != "" || a.source.assets[m.opt.Arch] != ""}
	if raw, err := os.ReadFile(filepath.Join(m.opt.ManifestDir, a.ID+".json")); err == nil {
		var mf manifest
		json.Unmarshal(raw, &mf)
		s.Installed, s.By, s.Version, s.Unit = true, "store", mf.Version, a.ID+".service"
		if mf.Port > 0 {
			s.Port = mf.Port
		}
		s.Running = slices.Contains(active, s.Unit)
		return s
	}
	// Another installer: a swizzin lock, or one of the app's usual unit files ("name@.service"
	// templates run as "name@user.service").
	if a.lock != "" {
		if _, err := os.Stat(filepath.Join(m.opt.LockDir, "."+a.lock+".lock")); err == nil {
			s.Installed, s.By = true, "swizzin"
		}
	}
	for _, u := range a.units {
		if slices.Contains(files, u) {
			s.Installed = true
			s.By = cmp.Or(s.By, "system")
		}
		prefix := strings.TrimSuffix(u, ".service")
		for _, name := range active {
			if name == u || (strings.HasSuffix(prefix, "@") && strings.HasPrefix(name, prefix)) {
				s.Installed, s.Running, s.Unit = true, true, name
				s.By = cmp.Or(s.By, "system")
			}
		}
	}
	// Last, its program running under some other name: Docker, a hand-made unit, a terminal.
	if !s.Running && slices.Contains(procs, comm(a.binary)) {
		s.Installed, s.Running = true, true
		s.By = cmp.Or(s.By, "system")
	}
	return s
}

// comm is a program name as the kernel keeps it, cut to 15 bytes.
func comm(name string) string { return name[:min(len(name), 15)] }

// processNames reads the name of every running process.
func processNames() []string {
	dirs, _ := os.ReadDir("/proc")
	var out []string
	for _, d := range dirs {
		if d.Name()[0] < '0' || d.Name()[0] > '9' {
			continue
		}
		if raw, err := os.ReadFile("/proc/" + d.Name() + "/comm"); err == nil {
			out = append(out, strings.TrimSpace(string(raw)))
		}
	}
	return out
}

// unitNames takes the first column of systemctl's plain listings.
func unitNames(listing string) []string {
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// ---------- jobs ----------

// Job returns the current or last job, with its log lines after the first `after`.
func (m *Manager) Job(after int) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	j := *m.job
	after = min(max(after, 0), len(j.Lines))
	j.Lines = append([]string{}, j.Lines[after:]...)
	return &j
}

// Install installs a store app on port (0 for its default) as user, in the background. then runs
// after a successful install, e.g. to add an uptime check.
func (m *Manager) Install(id string, port int, user string, then func(a App, port int)) (*Job, error) {
	a, ok := Find(id)
	if !ok {
		return nil, ErrUnknown
	}
	st := m.status(a, unitNames(m.opt.Systemctl("list-unit-files", "--no-legend", "--plain", "--type=service")), nil, m.opt.Processes())
	if st.Installed && st.By != "store" {
		return nil, ErrInstalled
	}
	if !st.Available {
		return nil, fmt.Errorf("%s has no build for %s", a.Name, m.opt.Arch)
	}
	if a.Port > 0 {
		if port == 0 {
			port = a.Port
		}
		if port < 1024 || port > 65535 {
			return nil, errors.New("choose a port between 1024 and 65535")
		}
		// Reinstalling keeps the port the app already has: its config was written with it.
		if st.Installed {
			port = st.Port
		} else if !portFree(port) {
			return nil, fmt.Errorf("port %d is in use; choose another", port)
		}
	} else {
		port = 0
	}
	if !userRE.MatchString(user) {
		return nil, fmt.Errorf("cannot run apps as %q", user)
	}
	return m.start(a, "install", func(ctx context.Context, j *jobLog) error {
		j.step("Finding the latest release")
		url, version, err := m.resolve(ctx, a)
		if err != nil {
			return err
		}
		p := plan{app: a, url: url, version: version, port: port, user: user}
		if err := m.opt.Run(ctx, a.ID, p.installScript(), j.line); err != nil {
			return err
		}
		if then != nil {
			then(a, port)
		}
		return nil
	})
}

// Remove uninstalls an app the store installed; purge also deletes its settings and data.
func (m *Manager) Remove(id string, purge bool) (*Job, error) {
	a, ok := Find(id)
	if !ok {
		return nil, ErrUnknown
	}
	if _, err := os.Stat(filepath.Join(m.opt.ManifestDir, a.ID+".json")); err != nil {
		return nil, ErrNotOurs
	}
	return m.start(a, "remove", func(ctx context.Context, j *jobLog) error {
		return m.opt.Run(ctx, a.ID, removeScript(a, purge), j.line)
	})
}

var userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func portFree(port int) bool {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// jobLog appends to the running job under the manager's lock.
type jobLog struct {
	m *Manager
	j *Job
}

// maxLines keeps a chatty install from growing without bound.
const maxLines = 4000

func (l *jobLog) line(s string) {
	s = logx.Redact(strings.TrimRight(s, "\r"))
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	if step, ok := strings.CutPrefix(s, "::step:: "); ok {
		l.j.Step = step
	}
	if len(l.j.Lines) < maxLines {
		l.j.Lines = append(l.j.Lines, s)
	}
}

func (l *jobLog) step(s string) { l.line("::step:: " + s) }

func (m *Manager) start(a App, action string, work func(context.Context, *jobLog) error) (*Job, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	m.nextID++
	j := &Job{ID: m.nextID, App: a.ID, Action: action, Started: float64(time.Now().Unix()), Lines: []string{}}
	m.job, m.running = j, true
	m.mu.Unlock()
	log.Info("App "+action+" started", "app", a.ID)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := work(ctx, &jobLog{m: m, j: j})
		m.mu.Lock()
		j.Finished, j.OK = float64(time.Now().Unix()), err == nil
		if err != nil {
			j.Error = err.Error()
		}
		m.running = false
		m.mu.Unlock()
		if err != nil {
			log.Warn("App "+action+" failed", "app", a.ID, "err", err)
		} else {
			log.Info("App "+action+" finished", "app", a.ID)
		}
	}()
	return m.Job(0), nil
}

// Wait blocks until the running job finishes or the timeout passes.
func (m *Manager) Wait(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------- releases ----------

var versionRE = regexp.MustCompile(`v?(\d+(?:\.\d+)+)`)

// resolve finds the download URL and version of an app's latest release for this machine.
func (m *Manager) resolve(ctx context.Context, a App) (string, string, error) {
	if u := a.source.urls[m.opt.Arch]; u != "" {
		// The update service answers with a redirect to the release; its file name has the version.
		client := *m.opt.HTTP
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := client.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("finding the latest %s: %w", a.Name, err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", "", fmt.Errorf("finding the latest %s: HTTP %d", a.Name, resp.StatusCode)
		}
		if !safeURL(loc) {
			return "", "", fmt.Errorf("finding the latest %s: unexpected download address %q", a.Name, loc)
		}
		return loc, safeVersion(versionRE.FindStringSubmatch(filepath.Base(loc))), nil
	}
	pattern := a.source.assets[m.opt.Arch]
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.opt.GitHubAPI+"/repos/"+a.source.github+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if m.opt.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.opt.GitHubToken)
	}
	resp, err := m.opt.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("finding the latest %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", "", fmt.Errorf("finding the latest %s: GitHub HTTP %d %s", a.Name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("reading %s's releases: %w", a.Name, err)
	}
	re := regexp.MustCompile(pattern)
	for _, as := range rel.Assets {
		if re.MatchString(as.Name) {
			if !safeURL(as.URL) {
				return "", "", fmt.Errorf("%s's release has an unexpected download address %q", a.Name, as.URL)
			}
			return as.URL, safeVersion(versionRE.FindStringSubmatch(rel.Tag)), nil
		}
	}
	return "", "", fmt.Errorf("%s's latest release (%s) has no build for %s", a.Name, rel.Tag, m.opt.Arch)
}

// safeURL accepts only plain https addresses, so a download address can be quoted into the install
// script without any chance of it running as shell.
func safeURL(u string) bool {
	return strings.HasPrefix(u, "https://") && urlRE.MatchString(u)
}

var urlRE = regexp.MustCompile(`^[A-Za-z0-9._~:/?#\[\]@!&()*+,;=%-]+$`)

// safeVersion keeps the version from a match, or "" when there is none. Only digits and dots get
// through, so a release name can never reach the install script as anything else.
func safeVersion(match []string) string {
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// ---------- running scripts ----------

// runScript runs a job's script as a transient systemd unit, so it runs with full privileges even
// when the monitor's own service is locked down, and outside the monitor's process tree. Without
// systemd it runs bash directly.
func runScript(ctx context.Context, dir, name, script string, line func(string)) error {
	f, err := os.CreateTemp(dir, "servermonitor-"+name+"-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		return err
	}
	f.Close()

	var cmd *exec.Cmd
	if _, err := exec.LookPath("systemd-run"); err == nil && os.Getenv("SERVERMONITOR_APPS_NO_SYSTEMD") == "" {
		cmd = exec.CommandContext(ctx, "systemd-run", "--quiet", "--collect", "--wait", "--pipe",
			"--unit=servermonitor-app-"+name+"-"+strconv.FormatInt(time.Now().Unix(), 10),
			"--description=Server monitor app store: "+name, "/bin/bash", f.Name())
	} else {
		cmd = exec.CommandContext(ctx, "/bin/bash", f.Name())
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("the %s script failed (%w); the log above says why", name, err)
	}
	return nil
}

// RunAs is who store apps run as: swizzin's main user when there is one, so the apps can read and
// write the same downloads as the rest, else DefaultUser.
func RunAs() string {
	raw, err := os.ReadFile("/root/.master.info")
	if err == nil {
		if name, _, _ := strings.Cut(strings.TrimSpace(string(raw)), ":"); userRE.MatchString(name) {
			return name
		}
	}
	return DefaultUser
}
