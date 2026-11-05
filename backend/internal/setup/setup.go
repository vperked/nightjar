// Package setup finds what a fresh monitor can watch on its machine: a local Plex server (and the
// token Plex keeps in its own settings), qBittorrent's Web UI, and local web apps worth an uptime
// check. Nothing here changes any settings; the dashboard offers what it finds.
package setup

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DetectedToken is the value the dashboard sends for PLEX_TOKEN to mean "the token this machine's
// Plex server keeps", so the token itself never travels to the browser.
const DetectedToken = "@detected"

// Roots let tests point detection at a fake machine.
var (
	PlexPreferences = []string{
		"/var/lib/plexmediaserver/Library/Application Support/Plex Media Server/Preferences.xml",
		"/var/snap/plexmediaserver/common/Library/Application Support/Plex Media Server/Preferences.xml",
	}
	PlexURL  = "http://127.0.0.1:32400"
	HomeDirs = func() []string {
		dirs := []string{"/root"}
		if entries, err := os.ReadDir("/home"); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join("/home", e.Name()))
				}
			}
		}
		return dirs
	}
)

var client = &http.Client{Timeout: 2 * time.Second}

// Plex is a Plex server found on this machine.
type Plex struct {
	Running    bool   `json:"running"`
	URL        string `json:"url"`
	Version    string `json:"version,omitempty"`
	TokenFound bool   `json:"token_found"`
}

// DetectPlex asks the local Plex for its identity (no token needed) and looks for its token.
func DetectPlex() Plex {
	p := Plex{URL: PlexURL, TokenFound: PlexToken() != ""}
	resp, err := client.Get(PlexURL + "/identity")
	if err != nil {
		return p
	}
	defer resp.Body.Close()
	var id struct {
		Version string `xml:"version,attr"`
	}
	if resp.StatusCode == http.StatusOK && xml.NewDecoder(resp.Body).Decode(&id) == nil {
		p.Running, p.Version = true, id.Version
	}
	return p
}

var plexTokenRE = regexp.MustCompile(`PlexOnlineToken="([^"]+)"`)

// PlexToken is the token the local Plex server signed in with, from its Preferences.xml.
func PlexToken() string {
	for _, path := range PlexPreferences {
		if data, err := os.ReadFile(path); err == nil {
			if m := plexTokenRE.FindSubmatch(data); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
}

// QBittorrent is a qBittorrent Web UI found in a user's qBittorrent.conf.
type QBittorrent struct {
	Found    bool   `json:"found"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	// LocalBypass means the Web UI lets clients on this machine in without logging in.
	LocalBypass bool   `json:"local_bypass"`
	Answering   bool   `json:"answering"`
	Config      string `json:"config,omitempty"`
}

// DetectQBittorrent reads each user's qBittorrent.conf and prefers a Web UI that answers.
func DetectQBittorrent() QBittorrent {
	var best QBittorrent
	for _, home := range HomeDirs() {
		path := filepath.Join(home, ".config", "qBittorrent", "qBittorrent.conf")
		conf := readINI(path)
		if conf == nil {
			continue
		}
		q := QBittorrent{Found: true, Host: "localhost", Port: 8080, Config: path, Username: conf[`WebUI\Username`]}
		if p, err := strconv.Atoi(conf[`WebUI\Port`]); err == nil && p > 0 {
			q.Port = p
		}
		q.LocalBypass = strings.EqualFold(conf[`WebUI\LocalHostAuth`], "false")
		q.Answering = IsQBittorrent(q.Port)
		if !best.Found || (q.Answering && !best.Answering) {
			best = q
		}
	}
	return best
}

// readINI reads key=value lines, ignoring sections; nil when the file is missing.
func readINI(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// Suggestion is an uptime check worth adding.
type Suggestion struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

// known are local web apps with an endpoint that answers without logging in.
var known = []struct {
	name string
	port int
	path string
}{
	{"Plex", 32400, "/identity"},
	{"Tautulli", 8181, "/status"},
	{"Seerr", 5055, "/api/v1/status"},
	{"Bazarr", 6767, "/"},
	{"Uptime Kuma", 3001, "/"},
}

var arrApps = []string{"Sonarr", "Radarr", "Prowlarr", "Lidarr", "Readarr"}

// SuggestChecks lists local apps that answer on their usual (or configured) port, as HTTP checks.
func SuggestChecks(qb QBittorrent) []Suggestion {
	var out []Suggestion
	seen := map[int]bool{}
	add := func(name, host string, port int, path string) {
		if seen[port] || !Probe(host, port) {
			return
		}
		seen[port] = true
		out = append(out, Suggestion{Name: name, Type: "http", Target: "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path})
	}
	for _, app := range arrApps {
		for _, home := range HomeDirs() {
			if host, port, base, ok := arrConfig(filepath.Join(home, ".config", app, "config.xml")); ok {
				// /ping answers without the API key.
				add(app, host, port, base+"/ping")
				break
			}
		}
	}
	if qb.Answering {
		add("qBittorrent", "127.0.0.1", qb.Port, "/")
	}
	for _, k := range known {
		add(k.name, "127.0.0.1", k.port, k.path)
	}
	return out
}

// arrConfig reads where a *arr app listens from its config.xml: the address, port and URL base.
func arrConfig(path string) (host string, port int, base string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, "", false
	}
	var c struct {
		Port        int    `xml:"Port"`
		URLBase     string `xml:"UrlBase"`
		BindAddress string `xml:"BindAddress"`
	}
	if xml.Unmarshal(data, &c) != nil || c.Port == 0 {
		return "", 0, "", false
	}
	if base = strings.Trim(c.URLBase, "/"); base != "" {
		base = "/" + base
	}
	// An app bound to one address listens only there; bound to all ("*"), on loopback too.
	host = "127.0.0.1"
	if ip := net.ParseIP(strings.TrimSpace(c.BindAddress)); ip != nil && !ip.IsUnspecified() {
		host = ip.String()
	}
	return host, c.Port, base, true
}

// Probe lets tests stand in for the network.
var Probe = func(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// IsQBittorrent reports whether qBittorrent's Web API answers on port: without a login it refuses
// /api/v2/app/version with 403 (or answers it, when localhost needs no login). Another program
// on the port, such as this monitor on qBittorrent's default 8080, answers otherwise.
var IsQBittorrent = func(port int) bool {
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v2/app/version", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body := make([]byte, 16)
	n, _ := resp.Body.Read(body)
	return resp.StatusCode == http.StatusForbidden || (resp.StatusCode == http.StatusOK && strings.HasPrefix(string(body[:n]), "v"))
}
