// Package plex polls a Plex Media Server: reachability, active streams, libraries and updates.
package plex

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the PMS HTTP API. The token goes in a header, never the URL, so errors cannot leak it.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func New(baseURL, token string, verifyTLS bool, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("PLEX_URL must be an http(s) URL, got %q", baseURL)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if !verifyTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{base: u, token: token, http: &http.Client{Timeout: timeout, Transport: tr}}, nil
}

// URL is the server address, for alerts and the dashboard.
func (c *Client) URL() string { return c.base.String() }

// ErrUnauthorized means the server answered but rejected the token.
var ErrUnauthorized = errors.New("unauthorized (check PLEX_TOKEN)")

func (c *Client) get(path string, query url.Values, v any) error {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", "servermonitor")
	req.Header.Set("X-Plex-Client-Identifier", "servermonitor")
	if c.token != "" {
		req.Header.Set("X-Plex-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("unexpected response: %w", err)
	}
	return nil
}

// num and flag accept Plex attributes whether they arrive as JSON numbers, booleans or strings.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	*n = num(f)
	return nil
}

type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	*f = flag(s == "true" || s == "1")
	return nil
}

// Server is the identity and version of the PMS.
type Server struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	UpdateAvailable string `json:"update_available,omitempty"`
}

func (c *Client) Server() (Server, error) {
	var r struct {
		MediaContainer struct {
			FriendlyName    string `json:"friendlyName"`
			Version         string `json:"version"`
			Platform        string `json:"platform"`
			PlatformVersion string `json:"platformVersion"`
		}
	}
	if err := c.get("/", nil, &r); err != nil {
		return Server{}, err
	}
	m := r.MediaContainer
	return Server{Name: m.FriendlyName, Version: m.Version, Platform: m.Platform, PlatformVersion: m.PlatformVersion}, nil
}

// AvailableUpdate returns the version of a pending server update, or "" when there is none.
func (c *Client) AvailableUpdate(current string) string {
	var r struct {
		MediaContainer struct {
			Release []struct {
				Version string `json:"version"`
				State   string `json:"state"`
			}
		}
	}
	if c.get("/updater/status", nil, &r) != nil {
		return ""
	}
	for _, rel := range r.MediaContainer.Release {
		if rel.Version != "" && rel.Version != current && rel.State != "notify" {
			return rel.Version
		}
	}
	return ""
}

// Stream is one active playback session.
type Stream struct {
	ID            string  `json:"id"`
	User          string  `json:"user"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
	Subtitle      string  `json:"subtitle"`
	Player        string  `json:"player"`
	Product       string  `json:"product"`
	Platform      string  `json:"platform"`
	State         string  `json:"state"`
	Local         bool    `json:"local"`
	Decision      string  `json:"decision"`
	VideoDecision string  `json:"video_decision,omitempty"`
	AudioDecision string  `json:"audio_decision,omitempty"`
	HWTranscode   bool    `json:"hw_transcode"`
	Throttled     bool    `json:"throttled"`
	Speed         float64 `json:"speed,omitempty"`
	Quality       string  `json:"quality"`
	BitrateKbps   int     `json:"bitrate_kbps"`
	BandwidthKbps int     `json:"bandwidth_kbps"`
	DurationMS    int64   `json:"duration_ms"`
	OffsetMS      int64   `json:"offset_ms"`
	Progress      float64 `json:"progress"`
	StartedAt     float64 `json:"started_at"`
}

const (
	DirectPlay   = "direct play"
	DirectStream = "direct stream"
	Transcode    = "transcode"
)

type rawSession struct {
	SessionKey       string `json:"sessionKey"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	ParentTitle      string `json:"parentTitle"`
	GrandparentTitle string `json:"grandparentTitle"`
	ParentIndex      num    `json:"parentIndex"`
	Index            num    `json:"index"`
	Year             num    `json:"year"`
	Duration         num    `json:"duration"`
	ViewOffset       num    `json:"viewOffset"`
	User             struct {
		Title string `json:"title"`
	}
	Player struct {
		Title    string `json:"title"`
		Product  string `json:"product"`
		Platform string `json:"platform"`
		State    string `json:"state"`
		Local    flag   `json:"local"`
	}
	Session struct {
		ID        string `json:"id"`
		Bandwidth num    `json:"bandwidth"`
		Location  string `json:"location"`
	}
	TranscodeSession *struct {
		VideoDecision        string `json:"videoDecision"`
		AudioDecision        string `json:"audioDecision"`
		Throttled            flag   `json:"throttled"`
		Speed                num    `json:"speed"`
		TranscodeHwRequested flag   `json:"transcodeHwRequested"`
		TranscodeHwEncoding  string `json:"transcodeHwEncoding"`
		TranscodeHwDecoding  string `json:"transcodeHwDecoding"`
	}
	Media []struct {
		VideoResolution string `json:"videoResolution"`
		Bitrate         num    `json:"bitrate"`
	}
}

func (r rawSession) stream() Stream {
	s := Stream{
		ID: r.SessionKey, User: r.User.Title, Kind: r.Type, Title: r.Title,
		Player: r.Player.Title, Product: r.Player.Product, Platform: r.Player.Platform, State: r.Player.State,
		Local: bool(r.Player.Local) || r.Session.Location == "lan", BandwidthKbps: int(r.Session.Bandwidth),
		DurationMS: int64(r.Duration), OffsetMS: int64(r.ViewOffset), Decision: DirectPlay,
	}
	if s.DurationMS > 0 {
		s.Progress = min(100, float64(s.OffsetMS)/float64(s.DurationMS)*100)
	}
	if s.ID == "" {
		s.ID = r.Session.ID
	}
	if s.User == "" {
		s.User = "unknown"
	}
	switch r.Type {
	case "episode":
		s.Title = r.GrandparentTitle
		s.Subtitle = fmt.Sprintf("S%02dE%02d · %s", int(r.ParentIndex), int(r.Index), r.Title)
	case "track":
		s.Subtitle = strings.Trim(r.GrandparentTitle+" · "+r.ParentTitle, " ·")
	default:
		if r.Year > 0 {
			s.Subtitle = strconv.Itoa(int(r.Year))
		}
	}
	if len(r.Media) > 0 {
		m := r.Media[0]
		s.BitrateKbps = int(m.Bitrate)
		s.Quality = resolution(m.VideoResolution)
	}
	if t := r.TranscodeSession; t != nil {
		s.VideoDecision, s.AudioDecision = t.VideoDecision, t.AudioDecision
		s.Throttled, s.Speed = bool(t.Throttled), float64(t.Speed)
		s.Decision = DirectStream
		if t.VideoDecision == "transcode" || t.AudioDecision == "transcode" {
			s.Decision = Transcode
			s.HWTranscode = t.TranscodeHwEncoding != "" || t.TranscodeHwDecoding != "" || bool(t.TranscodeHwRequested)
		}
	}
	return s
}

// resolution turns Plex's "1080" / "4k" / "sd" into "1080p" / "4K" / "SD".
func resolution(raw string) string {
	if _, err := strconv.Atoi(raw); err == nil {
		return raw + "p"
	}
	return strings.ToUpper(raw)
}

func (c *Client) Sessions() ([]Stream, error) {
	var r struct {
		MediaContainer struct {
			Metadata []rawSession
		}
	}
	if err := c.get("/status/sessions", nil, &r); err != nil {
		return nil, err
	}
	out := make([]Stream, 0, len(r.MediaContainer.Metadata))
	for _, m := range r.MediaContainer.Metadata {
		out = append(out, m.stream())
	}
	return out, nil
}

// Library is one library section with its item counts.
type Library struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Count      int    `json:"count"`
	ChildCount int    `json:"child_count,omitempty"`
	ChildLabel string `json:"child_label,omitempty"`
	Refreshing bool   `json:"refreshing"`
}

// Plex metadata type numbers for counting the children of show and music libraries.
var childTypes = map[string]struct {
	typ   string
	label string
}{"show": {"4", "episodes"}, "artist": {"10", "tracks"}}

func (c *Client) count(key, typ string) (int, error) {
	q := url.Values{"X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {"0"}}
	if typ != "" {
		q.Set("type", typ)
	}
	var r struct {
		MediaContainer struct {
			TotalSize num `json:"totalSize"`
			Size      num `json:"size"`
		}
	}
	if err := c.get("/library/sections/"+url.PathEscape(key)+"/all", q, &r); err != nil {
		return 0, err
	}
	return int(max(r.MediaContainer.TotalSize, r.MediaContainer.Size)), nil
}

func (c *Client) Libraries() ([]Library, error) {
	var r struct {
		MediaContainer struct {
			Directory []struct {
				Key        string `json:"key"`
				Title      string `json:"title"`
				Type       string `json:"type"`
				Refreshing flag   `json:"refreshing"`
			}
		}
	}
	if err := c.get("/library/sections", nil, &r); err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(r.MediaContainer.Directory))
	for _, d := range r.MediaContainer.Directory {
		lib := Library{Key: d.Key, Title: d.Title, Type: d.Type, Refreshing: bool(d.Refreshing)}
		lib.Count, _ = c.count(d.Key, "")
		if child, ok := childTypes[d.Type]; ok {
			lib.ChildCount, _ = c.count(d.Key, child.typ)
			lib.ChildLabel = child.label
		}
		out = append(out, lib)
	}
	return out, nil
}
