package arr

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nightjar/internal/logx"
)

var log = logx.Component("Cleanup")

// Sonarr and Radarr keep their port, URL base and API key in a config.xml in their data folder,
// so Nightjar can find them on this machine without being told. What it finds decides which app
// the media cleanup asks to delete things, so only a config.xml that belongs to root or to the
// account the app's service runs as is believed: anyone else's could point it at a fake.

// Candidate is a config.xml to try, and the account that may own it besides root.
type Candidate struct {
	Path string
	User string
}

// ConfigPaths are where to look for an app's config.xml, best first: the data folder its systemd
// service names (or its User's ~/.config/<App>), then the data folders the Debian packages and
// install scripts use. Homes are not searched blindly. Tests replace it.
var ConfigPaths = func(app string) []Candidate {
	var out []Candidate
	lower := strings.ToLower(app)
	owner := ""
	for _, u := range serviceUnits(lower) {
		args := execArgs(u)
		svcUser := systemctlValue("User", u)
		owner = cmp.Or(owner, svcUser)
		dataDir := ""
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "-data="); ok {
				dataDir = v
			} else if v, ok := strings.CutPrefix(a, "--data="); ok {
				dataDir = v
			}
		}
		if dataDir == "" && svcUser != "" {
			if acct, err := user.Lookup(svcUser); err == nil && acct.HomeDir != "" {
				dataDir = filepath.Join(acct.HomeDir, ".config", app)
			}
		}
		if dataDir != "" && filepath.IsAbs(dataDir) {
			out = append(out, Candidate{Path: filepath.Join(dataDir, "config.xml"), User: svcUser})
		}
	}
	for _, p := range []string{filepath.Join("/var/lib", lower, "config.xml"), filepath.Join("/var/lib", lower, ".config", app, "config.xml"), filepath.Join("/opt", lower, "config.xml")} {
		out = append(out, Candidate{Path: p, User: owner})
	}
	return out
}

// systemctl answers a query about units, or "" when it cannot.
var systemctl = func(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func systemctlValue(prop, unit string) string {
	return systemctl("show", "-p", prop, "--value", unit)
}

// serviceUnits are the app's installed services: <app>.service and any <app>@<user> instances.
func serviceUnits(lower string) []string {
	var out []string
	if systemctlValue("LoadState", lower+".service") == "loaded" {
		out = append(out, lower+".service")
	}
	for _, line := range strings.Split(systemctl("list-units", "--all", "--plain", "--no-legend", lower+"@*"), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], lower+"@") {
			out = append(out, f[0])
		}
	}
	return out
}

var argvRE = regexp.MustCompile(`argv\[\]=([^;]*)`)

// execArgs is a unit's ExecStart command line.
func execArgs(unit string) []string {
	m := argvRE.FindStringSubmatch(systemctlValue("ExecStart", unit))
	if m == nil {
		return nil
	}
	return strings.Fields(m[1])
}

// maxConfig is far more than any config.xml holds.
const maxConfig = 1 << 20

// readConfig reads a config.xml when it is a plain file belonging to root, to the account
// Nightjar runs as, or to owner. It never follows a symlink at the end of the path, and never
// waits on a FIFO or device someone put there.
func readConfig(path, owner string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("cannot tell who owns " + path)
	}
	trusted := st.Uid == 0 || int(st.Uid) == os.Geteuid()
	if !trusted && owner != "" {
		if u, err := user.Lookup(owner); err == nil && u.Uid == strconv.FormatUint(uint64(st.Uid), 10) {
			trusted = true
		}
	}
	if !trusted {
		return nil, fmt.Errorf("%s belongs to someone other than root or the app's account", path)
	}
	return io.ReadAll(io.LimitReader(f, maxConfig))
}

// Found is an app found on this machine, and where.
type Found struct {
	URL  string `json:"url"`
	Key  string `json:"-"`
	From string `json:"from"`
}

// Discover finds Sonarr ("sonarr") or Radarr ("radarr") on this machine.
func Discover(kind string) (Found, bool) {
	app := "Sonarr"
	if kind == "radarr" {
		app = "Radarr"
	}
	for _, c := range ConfigPaths(app) {
		data, err := readConfig(c.Path, c.User)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Debug("Not using "+c.Path, "err", err)
			}
			continue
		}
		var cfg struct {
			Port    int    `xml:"Port"`
			URLBase string `xml:"UrlBase"`
			APIKey  string `xml:"ApiKey"`
		}
		if xml.Unmarshal(data, &cfg) != nil || cfg.Port == 0 || cfg.APIKey == "" {
			continue
		}
		base := strings.Trim(cfg.URLBase, "/")
		if base != "" {
			base = "/" + base
		}
		return Found{URL: fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, base), Key: cfg.APIKey, From: c.Path}, true
	}
	return Found{}, false
}

// Connect is a client for the app at url with key, or, with either missing, the one found on
// this machine. It is nil when there is neither.
func Connect(kind, url, key string) *Client {
	if strings.TrimSpace(url) == "" || key == "" {
		if f, ok := Discover(kind); ok {
			url, key = f.URL, f.Key
		}
	}
	return New(kind, url, key)
}

// Queued is a download in the app's queue.
type Queued struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// Queued finds the queued download for a torrent, by its info hash.
func (c *Client) Queued(ctx context.Context, hash string) (Queued, bool, error) {
	var r struct {
		Records []struct {
			ID         int    `json:"id"`
			Title      string `json:"title"`
			DownloadID string `json:"downloadId"`
		} `json:"records"`
	}
	unknown := "includeUnknownMovieItems"
	if c.Kind == "sonarr" {
		unknown = "includeUnknownSeriesItems"
	}
	if err := c.do(ctx, http.MethodGet, "/queue?pageSize=1000&"+unknown+"=true", &r); err != nil {
		return Queued{}, false, err
	}
	for _, q := range r.Records {
		if strings.EqualFold(q.DownloadID, hash) {
			return Queued{q.ID, q.Title}, true, nil
		}
	}
	return Queued{}, false, nil
}

// Replace removes a queued download from the torrent client, blocklists the release so it is
// not grabbed again, and has the app search for another.
func (c *Client) Replace(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/queue/"+strconv.Itoa(id)+"?removeFromClient=true&blocklist=true&skipRedownload=false", nil)
}
