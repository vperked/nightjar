// Package qbittorrent talks to the qBittorrent Web API (v2) and holds the torrent cleanup policy.
package qbittorrent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/logx"
)

var log = logx.Component("QBittorrent")

var (
	SeedingActiveStates  = set("uploading", "stalledup", "queuedup", "forcedup")
	activeDownloadStates = set("downloading", "stalleddl", "queueddl", "checkingdl", "forceddl", "metadl", "allocating")
	DownloadingStates    = union(activeDownloadStates, set("pauseddl", "stoppeddl"))
	InactiveStates       = set("pausedup", "stoppedup", "pauseddl", "stoppeddl")
)

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

func union(a, b map[string]bool) map[string]bool {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

// Torrent is one torrent from /api/v2/torrents/info. State is lower-cased.
type Torrent struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Progress     float64 `json:"progress"`
	State        string  `json:"state"`
	DLSpeed      int64   `json:"dlspeed"`
	UPSpeed      int64   `json:"upspeed"`
	Ratio        float64 `json:"ratio"`
	ETA          int64   `json:"eta"`
	Seeds        int     `json:"num_seeds"`
	Peers        int     `json:"num_leechs"`
	AddedOn      int64   `json:"added_on"`
	CompletionOn int64   `json:"completion_on"`
	Category     string  `json:"category"`
	SavePath     string  `json:"save_path"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
}

func (t Torrent) ProgressPercent() float64 { return t.Progress * 100 }

// IsDownloadStalled is a stalled download with no speed that has not finished.
func IsDownloadStalled(t Torrent) bool {
	return t.State == "stalleddl" && t.DLSpeed == 0 && t.Progress < 1
}

// RemoveReason returns "inactive", "ratio" or "" for the cleanup policy.
func RemoveReason(t Torrent, minRatio float64) string {
	if InactiveStates[t.State] {
		return "inactive"
	}
	if minRatio > 0 && SeedingActiveStates[t.State] && t.Progress >= 1 && t.Ratio >= minRatio {
		return "ratio"
	}
	return ""
}

// Client is a logged-in Web API session behind a circuit breaker.
type Client struct {
	base               string
	username, password string
	http               *http.Client
	breaker            *discord.CircuitBreaker

	mu            sync.Mutex
	connected     bool
	loggedVersion bool
	version       string
	lastError     string
}

func New(host string, port int, username, password string) (*Client, error) {
	if username == "" {
		return nil, errors.New("qBittorrent username not provided (QBITTORRENT_USERNAME)")
	}
	if password == "" {
		return nil, errors.New("qBittorrent password not provided (QBITTORRENT_PASSWORD); empty passwords are not allowed")
	}
	base := host
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Port() == "" {
		u.Host = fmt.Sprintf("%s:%d", u.Hostname(), port)
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		base:     strings.TrimSuffix(u.String(), "/"),
		username: username, password: password,
		http:    &http.Client{Timeout: 10 * time.Second, Jar: jar},
		breaker: discord.NewCircuitBreaker(5, 60*time.Second, 2),
	}, nil
}

func (c *Client) do(method, path string, form url.Values) ([]byte, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Referer", c.base)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return data, resp.StatusCode, err
}

func (c *Client) login() error {
	data, status, err := c.do(http.MethodPost, "/api/v2/auth/login", url.Values{"username": {c.username}, "password": {c.password}})
	if err != nil {
		return err
	}
	if status != http.StatusOK || strings.TrimSpace(string(data)) == "Fails." {
		return fmt.Errorf("login failed (HTTP %d)", status)
	}
	return nil
}

func (c *Client) ensureConnected() bool {
	if c.breaker.IsOpen() {
		return false
	}
	err := c.breaker.Call(func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.connected {
			if _, status, err := c.do(http.MethodGet, "/api/v2/app/version", nil); err == nil && status == http.StatusOK {
				return nil
			}
			c.connected = false
		}
		if err := c.login(); err != nil {
			return err
		}
		version, status, err := c.do(http.MethodGet, "/api/v2/app/version", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("version check failed (HTTP %d)", status)
		}
		c.version = strings.TrimSpace(string(version))
		if !c.loggedVersion {
			log.Info("qBittorrent connected successfully", "version", c.version)
			c.loggedVersion = true
		}
		c.connected, c.lastError = true, ""
		return nil
	})
	if err != nil {
		c.mu.Lock()
		c.connected = false
		if !errors.Is(err, discord.ErrBreakerOpen) {
			c.lastError = err.Error()
		}
		c.mu.Unlock()
		return false
	}
	return true
}

// URL is the Web UI address.
func (c *Client) URL() string { return c.base }

// Version is qBittorrent's version, once connected.
func (c *Client) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Transfer is qBittorrent's global transfer state (/api/v2/transfer/info): speeds in bytes per second,
// data in bytes this session, limits (0 means none).
type Transfer struct {
	DLSpeed    int64  `json:"dl_info_speed"`
	UPSpeed    int64  `json:"up_info_speed"`
	DLData     int64  `json:"dl_info_data"`
	UPData     int64  `json:"up_info_data"`
	DLLimit    int64  `json:"dl_rate_limit"`
	UPLimit    int64  `json:"up_rate_limit"`
	Connection string `json:"connection_status"`
	DHTNodes   int    `json:"dht_nodes"`
}

// Transfer reads the global speeds and session totals.
func (c *Client) Transfer() (Transfer, error) {
	var t Transfer
	if !c.ensureConnected() {
		return t, errors.New("not connected")
	}
	err := c.breaker.Call(func() error {
		data, status, err := c.do(http.MethodGet, "/api/v2/transfer/info", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("transfer/info HTTP %d", status)
		}
		return json.Unmarshal(data, &t)
	})
	return t, err
}

// Available logs in if needed and reports whether the API answers.
func (c *Client) Available() bool { return c.ensureConnected() }

// LastError is why the most recent connection or request failed.
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastError
}

func (c *Client) fail(err error) error {
	if err != nil && !errors.Is(err, discord.ErrBreakerOpen) {
		c.mu.Lock()
		c.lastError = err.Error()
		c.mu.Unlock()
	}
	return err
}

// List returns every torrent; an error tells a failed fetch apart from an empty list.
func (c *Client) List() ([]Torrent, error) {
	if !c.ensureConnected() {
		return nil, errors.New(cmp.Or(c.LastError(), "not connected"))
	}
	var torrents []Torrent
	err := c.breaker.Call(func() error {
		data, status, err := c.do(http.MethodGet, "/api/v2/torrents/info", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("torrents/info HTTP %d", status)
		}
		return json.Unmarshal(data, &torrents)
	})
	if err != nil {
		return nil, c.fail(err)
	}
	for i := range torrents {
		torrents[i].State = strings.ToLower(torrents[i].State)
	}
	if torrents == nil {
		torrents = []Torrent{}
	}
	return torrents, nil
}

// Torrents is List without the error, for callers that only want what is there.
func (c *Client) Torrents() []Torrent {
	t, _ := c.List()
	return t
}

// post sends a form to the first of paths the server knows (qBittorrent 5 renamed some endpoints).
func (c *Client) post(paths []string, form url.Values) error {
	if !c.ensureConnected() {
		return errors.New(cmp.Or(c.LastError(), "not connected"))
	}
	err := c.breaker.Call(func() error {
		var lastStatus int
		for _, p := range paths {
			data, status, err := c.do(http.MethodPost, p, form)
			if err != nil {
				return err
			}
			if status == http.StatusOK {
				if strings.TrimSpace(string(data)) == "Fails." {
					return errors.New("qBittorrent refused it")
				}
				return nil
			}
			if status != http.StatusNotFound {
				return fmt.Errorf("HTTP %d: %s", status, strings.TrimSpace(string(data)))
			}
			lastStatus = status
		}
		return fmt.Errorf("HTTP %d", lastStatus)
	})
	return c.fail(err)
}

func (c *Client) action(hash string, paths []string, form url.Values) bool {
	if hash == "" {
		return false
	}
	form.Set("hashes", hash)
	return c.post(paths, form) == nil
}

// Recover reannounces and resumes a stalled torrent, returning the actions that succeeded.
func (c *Client) Recover(hash string) []string {
	var actions []string
	if c.action(hash, []string{"/api/v2/torrents/reannounce"}, url.Values{}) {
		actions = append(actions, "reannounce")
	}
	// qBittorrent 5 renamed resume to start.
	if c.action(hash, []string{"/api/v2/torrents/start", "/api/v2/torrents/resume"}, url.Values{}) {
		actions = append(actions, "resume")
	}
	return actions
}

// Delete removes a torrent, optionally with its files.
func (c *Client) Delete(hash string, deleteFiles bool) []string {
	if c.action(hash, []string{"/api/v2/torrents/delete"}, url.Values{"deleteFiles": {fmt.Sprint(deleteFiles)}}) {
		if deleteFiles {
			return []string{"delete_files"}
		}
		return []string{"delete"}
	}
	return nil
}

// Actions the dashboard can apply to torrents, with the endpoints that do them (newest name first).
var actionPaths = map[string][]string{
	"start":      {"/api/v2/torrents/start", "/api/v2/torrents/resume"},
	"stop":       {"/api/v2/torrents/stop", "/api/v2/torrents/pause"},
	"reannounce": {"/api/v2/torrents/reannounce"},
	"recheck":    {"/api/v2/torrents/recheck"},
	"delete":     {"/api/v2/torrents/delete"},
}

// Actions lists what Apply accepts.
var Actions = []string{"start", "stop", "reannounce", "recheck", "delete"}

var hashRE = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// Apply runs an action on the given torrents. Hashes must be real info hashes: qBittorrent
// reads "all" as every torrent, which the dashboard never means by accident.
func (c *Client) Apply(action string, hashes []string, deleteFiles bool) error {
	paths, ok := actionPaths[action]
	if !ok {
		return fmt.Errorf("unknown action %q", action)
	}
	if len(hashes) == 0 {
		return errors.New("no torrents given")
	}
	for _, h := range hashes {
		if !hashRE.MatchString(h) {
			return fmt.Errorf("%q is not a torrent hash", h)
		}
	}
	form := url.Values{"hashes": {strings.Join(hashes, "|")}}
	if action == "delete" {
		form.Set("deleteFiles", fmt.Sprint(deleteFiles))
	}
	return c.post(paths, form)
}

// Add adds torrents by magnet link or URL (one per line), optionally into a category and stopped.
func (c *Client) Add(urls []string, category string, stopped bool) error {
	if len(urls) == 0 {
		return errors.New("give at least one magnet link or URL")
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "magnet:?") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("%.60q is not a magnet link or http(s) URL", u)
		}
	}
	form := url.Values{"urls": {strings.Join(urls, "\n")}}
	if category != "" {
		form.Set("category", category)
	}
	if stopped {
		// qBittorrent 5 reads "stopped"; 4.x reads "paused".
		form.Set("stopped", "true")
		form.Set("paused", "true")
	}
	return c.post([]string{"/api/v2/torrents/add"}, form)
}

// Categories lists the category names, sorted.
func (c *Client) Categories() ([]string, error) {
	if !c.ensureConnected() {
		return nil, errors.New(cmp.Or(c.LastError(), "not connected"))
	}
	var cats map[string]json.RawMessage
	err := c.breaker.Call(func() error {
		data, status, err := c.do(http.MethodGet, "/api/v2/torrents/categories", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("categories HTTP %d", status)
		}
		return json.Unmarshal(data, &cats)
	})
	if err != nil {
		return nil, c.fail(err)
	}
	names := make([]string, 0, len(cats))
	for n := range cats {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}

// Summary counts torrents for the startup banner.
type Summary struct {
	Total, Downloading, Seeding int
}

func Summarize(torrents []Torrent) Summary {
	s := Summary{Total: len(torrents)}
	for _, t := range torrents {
		if DownloadingStates[t.State] {
			s.Downloading++
		} else if SeedingActiveStates[t.State] {
			s.Seeding++
		}
	}
	return s
}
