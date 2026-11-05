package updates

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Detect finds what this machine has to keep updated: the OS packages, snaps, the monitor's own
// checkout and well-known self-hosted apps. Nothing it finds updates automatically except as
// configured afterwards.
func Detect(run Runner) []Spec {
	d := detector{run: run}
	var out []Spec
	add := func(s Spec, ok bool) {
		if ok {
			out = append(out, s)
		}
	}
	if pm, ok := findPackageManager(); ok {
		add(Spec{ID: "os", Name: "OS packages (" + pm.name + ")", Kind: KindSystem}, true)
	}
	if _, err := exec.LookPath("snap"); err == nil && d.snapsInstalled() {
		add(Spec{ID: "snaps", Name: "Snaps", Kind: KindSnap}, true)
	}
	add(d.self())
	add(d.apt("plex", "Plex Media Server", "plexmediaserver", "plexmediaserver"))
	for _, app := range []string{"Sonarr", "Radarr", "Prowlarr", "Lidarr", "Readarr", "Whisparr"} {
		add(d.arr(app))
	}
	add(d.adguard())
	add(d.autobrr())
	add(d.release("bazarr", "Bazarr", "morpheus65535/bazarr", "/opt/bazarr/VERSION", "cat /opt/bazarr/VERSION", "bazarr"))
	add(d.seerr())
	if qb := d.firstUnit("qbittorrent@*", "qbittorrent", "qbittorrent-nox"); qb != "" && d.dpkgHas("qbittorrent-nox") {
		// swizzin builds qBittorrent from source, interactively, and only up to 5.1. The official
		// static build replaces its program instead: one file, any version, in seconds.
		add(Spec{ID: "qbittorrent", Name: "qBittorrent", Kind: KindRelease, Repo: "qbittorrent/qBittorrent", TagPrefix: "release-",
			VersionCommand: qbVersion, UpdateCommand: qbStaticUpdate, Unit: qb, Restart: true}, true)
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, dir := range []string{"/opt/uptime-kuma", filepath.Join(home, "uptime-kuma")} {
			if s, ok := d.release("uptime-kuma", "Uptime Kuma", "louislam/uptime-kuma", dir+"/package.json", `grep -m1 '"version"' `+shellQuote(dir+"/package.json"), ""); ok {
				add(s, true)
				break
			}
		}
	}
	if isDir("/opt/tautulli/.git") {
		add(Spec{ID: "tautulli", Name: "Tautulli", Kind: KindGit, Dir: "/opt/tautulli", Unit: d.firstUnit("tautulli"), Restart: true}, true)
	}
	return out
}

type detector struct{ run Runner }

func (d detector) out(argv ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, err := d.run(ctx, Command{Argv: argv})
	if err != nil || o.Code != 0 {
		return ""
	}
	return strings.TrimSpace(o.Stdout)
}

func (d detector) snapsInstalled() bool {
	// Base snaps (core, snapd) alone are not worth a row.
	for i, line := range strings.Split(d.out("snap", "list"), "\n") {
		if f := strings.Fields(line); i > 0 && len(f) > 0 && !strings.HasPrefix(f[0], "core") && f[0] != "snapd" && f[0] != "bare" {
			return true
		}
	}
	return false
}

// dpkgHas reports whether a package is installed, held or not.
func (d detector) dpkgHas(pkg string) bool {
	return strings.HasSuffix(d.out("dpkg-query", "-W", "-f=${Status}", pkg), "ok installed")
}

// firstUnit returns the first systemd unit (or instance, for a pattern) that is installed.
func (d detector) firstUnit(names ...string) string {
	for _, n := range names {
		if strings.Contains(n, "*") {
			for _, line := range strings.Split(d.out("systemctl", "list-units", "--all", "--plain", "--no-legend", n), "\n") {
				if f := strings.Fields(line); len(f) > 0 {
					return strings.TrimSuffix(f[0], ".service")
				}
			}
			continue
		}
		if d.out("systemctl", "show", "-p", "LoadState", "--value", n) == "loaded" {
			return n
		}
	}
	return ""
}

var (
	argvRE      = regexp.MustCompile(`argv\[\]=([^;]*)`)
	plainWordRE = regexp.MustCompile(`^[\w@%+=:,./-]+$`)
)

// execArgs is a unit's ExecStart command line.
func (d detector) execArgs(unit string) []string {
	m := argvRE.FindStringSubmatch(d.out("systemctl", "show", "-p", "ExecStart", "--value", unit))
	if m == nil {
		return nil
	}
	return strings.Fields(m[1])
}

func (d detector) apt(id, name, pkg, unit string) (Spec, bool) {
	if !d.dpkgHas(pkg) {
		return Spec{}, false
	}
	return Spec{ID: id, Name: name, Kind: KindApt, Package: pkg, Unit: d.firstUnit(unit)}, true
}

// arr finds a Sonarr-style app by its service and reads its URL and API key from its config.xml when checking.
func (d detector) arr(app string) (Spec, bool) {
	lower := strings.ToLower(app)
	unit := d.firstUnit(lower, lower+"@*")
	if unit == "" {
		return Spec{}, false
	}
	var dataDir string
	for _, a := range d.execArgs(unit) {
		if v, ok := strings.CutPrefix(a, "-data="); ok {
			dataDir = v
		} else if v, ok := strings.CutPrefix(a, "--data="); ok {
			dataDir = v
		}
	}
	if dataDir == "" {
		if u := d.out("systemctl", "show", "-p", "User", "--value", unit); u != "" {
			if home := homeOf(u); home != "" {
				dataDir = filepath.Join(home, ".config", app)
			}
		}
	}
	config := filepath.Join(dataDir, "config.xml")
	if dataDir == "" || !isFile(config) {
		return Spec{}, false
	}
	return Spec{ID: lower, Name: app, Kind: KindArr, ConfigFile: filepath.Clean(config), Unit: unit}, true
}

func (d detector) adguard() (Spec, bool) {
	unit := d.firstUnit("AdGuardHome")
	args := d.execArgs(unit)
	if unit == "" || len(args) == 0 || !isFile(args[0]) {
		return Spec{}, false
	}
	bin := shellQuote(args[0])
	// --update replaces the binary and restarts the service itself.
	return Spec{ID: "adguard-home", Name: "AdGuard Home", Kind: KindRelease, Repo: "AdguardTeam/AdGuardHome",
		VersionCommand: bin + " --version", UpdateCommand: bin + " --update", Unit: unit}, true
}

func (d detector) autobrr() (Spec, bool) {
	ctl, err := exec.LookPath("autobrrctl")
	unit := d.firstUnit("autobrr@*", "autobrr")
	if err != nil || unit == "" {
		return Spec{}, false
	}
	s := Spec{ID: "autobrr", Name: "autobrr", Kind: KindRelease, Repo: "autobrr/autobrr", VersionCommand: shellQuote(ctl) + " version", Unit: unit}
	if box := swizzinBox(); box != "" {
		// swizzin's upgrade downloads the latest release and restarts every autobrr instance.
		s.UpdateCommand = "PATH=" + filepath.Dir(box) + ":$PATH " + shellQuote(box) + " upgrade autobrr"
	}
	return s, true
}

func (d detector) release(id, name, repo, marker, versionCommand, unit string) (Spec, bool) {
	if !isFile(marker) {
		return Spec{}, false
	}
	s := Spec{ID: id, Name: name, Kind: KindRelease, Repo: repo, VersionCommand: versionCommand}
	if unit != "" {
		s.Unit = d.firstUnit(unit)
	}
	return s, true
}

// qbVersion asks the program itself, which is right whichever build is installed.
const qbVersion = "/usr/bin/qbittorrent-nox --version"

// qbStaticUpdate puts the newest official static build in place of /usr/bin/qbittorrent-nox: it
// downloads it, checks it runs, then swaps it in with a rename, which works while the old one runs.
const qbStaticUpdate = `set -e
case "$(uname -m)" in x86_64) arch=x86_64 ;; aarch64) arch=aarch64 ;; *) echo "no static qBittorrent build for $(uname -m)"; exit 1 ;; esac
tmp=$(mktemp /usr/bin/.qbittorrent-nox.XXXXXX); trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp" "https://github.com/userdocs/qbittorrent-nox-static/releases/latest/download/${arch}-qbittorrent-nox"
chmod 755 "$tmp"
"$tmp" --version
mv -f "$tmp" /usr/bin/qbittorrent-nox
trap - EXIT`

// seerrFetch gets the one release tag it installs. Fetching every tag fails once upstream has moved
// any of them (Seerr moves its preview tags), and git says so only without --quiet.
const seerrFetch = `git fetch --force origin "+refs/tags/$TAG:refs/tags/$TAG"`

// superseded are recipe lines replaced by better ones: a saved command made from the older recipe,
// and otherwise untouched, gets the new line.
var superseded = [][2]string{
	{"git fetch --tags --quiet origin", seerrFetch},
}

// seerr is Seerr in a git checkout: an update checks out the new tag and rebuilds it with the Node
// its service runs, and the restart puts the new build live.
func (d detector) seerr() (Spec, bool) {
	s, ok := d.release("seerr", "Seerr", "seerr-team/seerr", "/opt/seerr/package.json", `grep -m1 '"version"' /opt/seerr/package.json`, "seerr")
	if !ok || !isDir("/opt/seerr/.git") || !isFile("/opt/seerr/pnpm-lock.yaml") || s.Unit == "" {
		return s, ok
	}
	path := ""
	if args := d.execArgs(s.Unit); len(args) > 0 && filepath.Base(args[0]) == "node" {
		path = "export PATH=" + shellQuote(filepath.Dir(args[0])) + ":$PATH\n"
	}
	s.UpdateCommand = "set -e\n" + path + `cd /opt/seerr
` + seerrFetch + `
git -c advice.detachedHead=false checkout --quiet "$TAG"
CYPRESS_INSTALL_BINARY=0 pnpm install --frozen-lockfile
pnpm build`
	s.Restart = true
	return s, ok
}

// self is the monitor's own checkout, rebuilt with make after pulling.
func (d detector) self() (Spec, bool) {
	exe, err := os.Executable()
	if err != nil {
		return Spec{}, false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if isDir(filepath.Join(dir, ".git")) {
			if !isFile(filepath.Join(dir, "Makefile")) {
				return Spec{}, false
			}
			return Spec{ID: "nightjar", Name: "Nightjar", Kind: KindGit, Dir: dir, Self: true, UpdateCommand: buildCommand()}, true
		}
	}
	return Spec{}, false
}

// buildCommand runs make with Go and npm on the PATH, wherever they were installed: services start
// with a bare PATH that rarely includes ~/sdk or ~/.nvm.
func buildCommand() string {
	var dirs []string
	home, _ := os.UserHomeDir()
	for _, tool := range []struct {
		name  string
		globs []string
	}{
		{"go", []string{"/usr/local/go/bin/go", home + "/sdk/go*/bin/go", home + "/go/bin/go"}},
		{"npm", []string{home + "/.nvm/versions/node/*/bin/npm", "/usr/local/bin/npm"}},
	} {
		path, err := exec.LookPath(tool.name)
		if err != nil {
			for _, g := range tool.globs {
				if m, _ := filepath.Glob(g); len(m) > 0 {
					slices.SortFunc(m, func(a, b string) int { return Compare(a, b) })
					path = m[len(m)-1]
					break
				}
			}
		}
		if path != "" && !slices.Contains(dirs, filepath.Dir(path)) {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	if len(dirs) == 0 {
		return "make build"
	}
	return "PATH=" + strings.Join(dirs, ":") + ":$PATH make build"
}

func swizzinBox() string {
	for _, p := range []string{"/usr/local/bin/swizzin/box", "/usr/local/bin/box"} {
		if isFile(p) {
			return p
		}
	}
	return ""
}

func homeOf(username string) string {
	if u, err := user.Lookup(username); err == nil {
		return u.HomeDir
	}
	return ""
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

// shellQuote quotes a path for sh when it needs it.
func shellQuote(s string) string {
	if plainWordRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
