package arr

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nightjar/internal/setup"
)

// Sonarr and Radarr keep their port, URL base and API key in a config.xml in their data folder,
// so Nightjar can find them on this machine without being told.

// ConfigPaths are where to look for an app's config.xml: each home's ~/.config/<App>, and the
// data folders the Debian packages and install scripts use. Tests replace it.
var ConfigPaths = func(app string) []string {
	var out []string
	for _, home := range setup.HomeDirs() {
		out = append(out, filepath.Join(home, ".config", app, "config.xml"))
	}
	lower := strings.ToLower(app)
	return append(out, filepath.Join("/var/lib", lower, "config.xml"), filepath.Join("/var/lib", lower, ".config", app, "config.xml"), filepath.Join("/opt", lower, "config.xml"))
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
	for _, path := range ConfigPaths(app) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var c struct {
			Port    int    `xml:"Port"`
			URLBase string `xml:"UrlBase"`
			APIKey  string `xml:"ApiKey"`
		}
		if xml.Unmarshal(data, &c) != nil || c.Port == 0 || c.APIKey == "" {
			continue
		}
		base := strings.Trim(c.URLBase, "/")
		if base != "" {
			base = "/" + base
		}
		return Found{URL: fmt.Sprintf("http://127.0.0.1:%d%s", c.Port, base), Key: c.APIKey, From: path}, true
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
