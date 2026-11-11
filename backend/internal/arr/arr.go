// Package arr talks to Sonarr and Radarr for the media cleanup: what they manage, which torrents
// a show or movie was imported from, and deleting it with its files so they do not fetch it again.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is one Sonarr or Radarr. A nil Client manages nothing.
type Client struct {
	Kind string // sonarr or radarr
	base string
	key  string
	http *http.Client
}

// New is a client for the app at base, or nil without a URL or key.
func New(kind, base, key string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || key == "" {
		return nil
	}
	return &Client{Kind: kind, base: base, key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

// Name is how the app reads in messages.
func (c *Client) Name() string {
	if c.Kind == "sonarr" {
		return "Sonarr"
	}
	return "Radarr"
}

// MaxAnswer is the most of an answer read. Sonarr and Radarr are local services Nightjar trusts,
// and a big library's list runs to tens of megabytes. Tests lower it.
var MaxAnswer int64 = 256 << 20

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v3"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %v", c.Name(), err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s refused the API key", c.Name())
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s answered HTTP %d", c.Name(), resp.StatusCode)
	}
	// One byte more than the most it takes tells a big answer from one cut short, which would
	// otherwise read as broken JSON.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswer+1))
	if err != nil {
		return fmt.Errorf("%s: reading its answer: %v", c.Name(), err)
	}
	if int64(len(body)) > MaxAnswer {
		return fmt.Errorf("%s's answer to %s is too large (over %d MB)", c.Name(), path, MaxAnswer>>20)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// Entry is a movie in Radarr or a series in Sonarr.
type Entry struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	Size  int64  `json:"size"`
}

// List is everything the app manages.
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	var raw []struct {
		ID         int    `json:"id"`
		Title      string `json:"title"`
		Path       string `json:"path"`
		TMDBID     int    `json:"tmdbId"`
		TVDBID     int    `json:"tvdbId"`
		IMDBID     string `json:"imdbId"`
		SizeOnDisk int64  `json:"sizeOnDisk"`
		Statistics struct {
			SizeOnDisk int64 `json:"sizeOnDisk"`
		} `json:"statistics"`
	}
	path := "/movie"
	if c.Kind == "sonarr" {
		path = "/series"
	}
	if err := c.do(ctx, http.MethodGet, path, &raw); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(raw))
	for _, r := range raw {
		out = append(out, Entry{ID: r.ID, Title: r.Title, Path: r.Path, TMDB: r.TMDBID, TVDB: r.TVDBID, IMDB: r.IMDBID, Size: max(r.SizeOnDisk, r.Statistics.SizeOnDisk)})
	}
	return out, nil
}

// Downloads are the torrents (by info hash, lower case) the app imported an entry's files from.
func (c *Client) Downloads(ctx context.Context, id int) ([]string, error) {
	var raw []struct {
		DownloadID string `json:"downloadId"`
		EventType  string `json:"eventType"`
	}
	path := "/history/movie?movieId=" + strconv.Itoa(id)
	if c.Kind == "sonarr" {
		path = "/history/series?seriesId=" + strconv.Itoa(id)
	}
	if err := c.do(ctx, http.MethodGet, path, &raw); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range raw {
		h := strings.ToLower(r.DownloadID)
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out, nil
}

// Delete removes an entry and its files, and tells the app not to add it again from its lists.
func (c *Client) Delete(ctx context.Context, id int) error {
	path := "/movie/" + strconv.Itoa(id) + "?deleteFiles=true&addImportExclusion=true"
	if c.Kind == "sonarr" {
		path = "/series/" + strconv.Itoa(id) + "?deleteFiles=true&addImportListExclusion=true"
	}
	return c.do(ctx, http.MethodDelete, path, nil)
}

// Match finds the entry for a Plex item: by its tmdb, tvdb or imdb id, or else by the folder
// its files are in.
func Match(entries []Entry, guids []string, files []string) (Entry, bool) {
	for _, g := range guids {
		scheme, id, ok := strings.Cut(g, "://")
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(id)
		for _, e := range entries {
			if (scheme == "tmdb" && n != 0 && e.TMDB == n) || (scheme == "tvdb" && n != 0 && e.TVDB == n) || (scheme == "imdb" && id != "" && e.IMDB == id) {
				return e, true
			}
		}
	}
	for _, f := range files {
		for _, e := range entries {
			if e.Path != "" && strings.HasPrefix(f, strings.TrimRight(e.Path, "/")+"/") {
				return e, true
			}
		}
	}
	return Entry{}, false
}
