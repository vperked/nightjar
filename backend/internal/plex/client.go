// Package plex polls a Plex Media Server: reachability, active streams, libraries and updates.
package plex

import (
	"cmp"
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

// request sends a GET with the token in a header; the caller closes the body.
func (c *Client) request(path string, query url.Values) (*http.Response, error) {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", "nightjar")
	req.Header.Set("X-Plex-Client-Identifier", "nightjar")
	if c.token != "" {
		req.Header.Set("X-Plex-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	return resp, nil
}

// maxBody is the most of one answer that is read. A longer one is refused rather than handed
// to the parser cut short; what can be long (a library, the watch history) is read in pages.
const maxBody = 8 << 20

func (c *Client) get(path string, query url.Values, v any) error {
	resp, err := c.request(path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return fmt.Errorf("Plex's answer to %s is larger than the %d MB Nightjar reads", path, maxBody>>20)
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
	// Reason is why Plex converts the stream, and Fix what would let it play as it is.
	Reason string `json:"reason,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// Address is the player's IP address, as Plex sees it.
	Address string `json:"address,omitempty"`
	// SessionID is Plex's id for the playback session, which stopping it takes; the dashboard names streams by ID.
	SessionID string `json:"-"`
	// Source is the file as it is; Output is what the player is sent, when Plex converts or repackages it.
	Source *Format `json:"source,omitempty"`
	Output *Format `json:"output,omitempty"`
	// The audio and subtitle tracks playing, as Plex names them: "English (TrueHD 7.1)".
	AudioTrack    string `json:"audio_track,omitempty"`
	SubtitleTrack string `json:"subtitle_track,omitempty"`
	// SubtitleDecision is what Plex does with the subtitles: burn, transcode or copy.
	SubtitleDecision string `json:"subtitle_decision,omitempty"`
	// HWDecode and HWEncode name the hardware doing each half of a transcode; "" is the CPU.
	HWDecode string `json:"hw_decode,omitempty"`
	HWEncode string `json:"hw_encode,omitempty"`
	// PeakKbps is the most bandwidth the stream has used so far.
	PeakKbps int `json:"peak_kbps,omitempty"`
}

// Format is a stream's video, audio and container, as they read: "HEVC", "Dolby TrueHD 7.1", "MKV", "4K".
type Format struct {
	Video      string `json:"video,omitempty"`
	Audio      string `json:"audio,omitempty"`
	Container  string `json:"container,omitempty"`
	Resolution string `json:"resolution,omitempty"`
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
		Address  string `json:"address"`
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
		SubtitleDecision     string `json:"subtitleDecision"`
		SourceVideoCodec     string `json:"sourceVideoCodec"`
		SourceAudioCodec     string `json:"sourceAudioCodec"`
		VideoCodec           string `json:"videoCodec"`
		AudioCodec           string `json:"audioCodec"`
		SourceContainer      string `json:"sourceContainer"`
		Container            string `json:"container"`
		Height               num    `json:"height"`
		Width                num    `json:"width"`
		AudioChannels        num    `json:"audioChannels"`
		HwDecodingTitle      string `json:"transcodeHwDecodingTitle"`
		HwEncodingTitle      string `json:"transcodeHwEncodingTitle"`
	}
	Media []struct {
		VideoResolution string `json:"videoResolution"`
		Bitrate         num    `json:"bitrate"`
		Height          num    `json:"height"`
		VideoCodec      string `json:"videoCodec"`
		AudioCodec      string `json:"audioCodec"`
		AudioChannels   num    `json:"audioChannels"`
		Container       string `json:"container"`
		Part            []struct {
			Stream []struct {
				StreamType   num    `json:"streamType"`
				Selected     flag   `json:"selected"`
				DisplayTitle string `json:"displayTitle"`
			}
		}
	}
}

func (r rawSession) stream() Stream {
	s := Stream{
		ID: r.SessionKey, User: r.User.Title, Kind: r.Type, Title: r.Title,
		Player: r.Player.Title, Product: r.Player.Product, Platform: r.Player.Platform, State: r.Player.State,
		Local: bool(r.Player.Local) || r.Session.Location == "lan", BandwidthKbps: int(r.Session.Bandwidth),
		DurationMS: int64(r.Duration), OffsetMS: int64(r.ViewOffset), Decision: DirectPlay,
		Address: r.Player.Address, SessionID: r.Session.ID,
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
		s.Source = &Format{Video: codecOr(m.VideoCodec), Audio: audio(m.AudioCodec, int(m.AudioChannels)), Container: upper(m.Container), Resolution: s.Quality}
		for _, p := range m.Part {
			for _, st := range p.Stream {
				// Plex lists the tracks playing, and marks the chosen one when there are several.
				switch {
				case st.StreamType == 2 && (bool(st.Selected) || s.AudioTrack == ""):
					s.AudioTrack = st.DisplayTitle
				case st.StreamType == 3 && bool(st.Selected):
					s.SubtitleTrack = st.DisplayTitle
				}
			}
		}
	}
	if t := r.TranscodeSession; t != nil {
		s.VideoDecision, s.AudioDecision, s.SubtitleDecision = t.VideoDecision, t.AudioDecision, t.SubtitleDecision
		s.Throttled, s.Speed = bool(t.Throttled), float64(t.Speed)
		s.Decision = DirectStream
		if t.VideoDecision == "transcode" || t.AudioDecision == "transcode" {
			s.Decision = Transcode
			s.HWTranscode = t.TranscodeHwEncoding != "" || t.TranscodeHwDecoding != "" || bool(t.TranscodeHwRequested)
			s.HWDecode = hardware(t.HwDecodingTitle, t.TranscodeHwDecoding)
			s.HWEncode = hardware(t.HwEncodingTitle, t.TranscodeHwEncoding)
		}
		if s.Source == nil {
			s.Source = &Format{}
		}
		// The transcoder's own word on the source is the surer one.
		if t.SourceVideoCodec != "" {
			s.Source.Video = codec(t.SourceVideoCodec)
		}
		if t.SourceAudioCodec != "" && !strings.HasPrefix(s.Source.Audio, codec(t.SourceAudioCodec)) {
			s.Source.Audio = codec(t.SourceAudioCodec)
		}
		if t.SourceContainer != "" {
			s.Source.Container = upper(t.SourceContainer)
		}
		out := *s.Source
		if t.VideoDecision == "transcode" {
			out.Video, out.Resolution = codecOr(t.VideoCodec), cmp.Or(size(int(t.Width), int(t.Height)), out.Resolution)
		}
		if t.AudioDecision == "transcode" {
			out.Audio = audio(t.AudioCodec, int(t.AudioChannels))
		}
		if t.Container != "" {
			out.Container = upper(t.Container)
		}
		if t.VideoDecision == "" && t.AudioDecision == "" {
			out = Format{}
		}
		if out != (Format{}) {
			s.Output = &out
		}
		var src struct {
			height                  int
			video, audio, container string
		}
		if len(r.Media) > 0 {
			m := r.Media[0]
			src.height, src.video, src.audio, src.container = int(m.Height), m.VideoCodec, m.AudioCodec, m.Container
		}
		s.Reason, s.Fix = why(t.SubtitleDecision, t.VideoDecision, t.AudioDecision, cmp.Or(t.SourceVideoCodec, src.video), t.VideoCodec,
			cmp.Or(t.SourceAudioCodec, src.audio), cmp.Or(t.SourceContainer, src.container), t.Container, src.height, int(t.Height), s.Local)
	}
	if s.Source != nil && *s.Source == (Format{}) {
		s.Source = nil
	}
	return s
}

// codecName is how a codec reads.
var codecName = map[string]string{"hevc": "HEVC", "h265": "HEVC", "av1": "AV1", "vc1": "VC-1", "mpeg2video": "MPEG-2", "h264": "H.264", "vp9": "VP9",
	"truehd": "Dolby TrueHD", "dca": "DTS", "dts": "DTS", "eac3": "Dolby Digital Plus", "ac3": "Dolby Digital", "flac": "FLAC", "aac": "AAC", "opus": "Opus", "mp3": "MP3"}

func codec(c string) string { return cmp.Or(codecName[strings.ToLower(c)], strings.ToUpper(c)) }

// codecOr is a codec's name, or "" for none.
func codecOr(c string) string {
	if c == "" {
		return ""
	}
	return codec(c)
}

func upper(s string) string { return strings.ToUpper(s) }

// audio is a codec with its channels: "Dolby TrueHD 7.1", "AAC stereo".
func audio(c string, channels int) string {
	name := codecOr(c)
	if name == "" {
		return ""
	}
	switch {
	case channels == 1:
		return name + " mono"
	case channels == 2:
		return name + " stereo"
	case channels > 2:
		return fmt.Sprintf("%s %d.1", name, channels-1)
	}
	return name
}

// size names a picture size the way Plex's resolutions read, by width first so wide films
// (1920×800) still count as 1080p.
func size(w, h int) string {
	switch {
	case w >= 3200 || h >= 2000:
		return "4K"
	case w >= 1800 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	case h > 0:
		return fmt.Sprintf("%dp", h)
	}
	return ""
}

// hwName is how the hardware APIs Plex names read, when it gives no title of its own.
var hwName = map[string]string{"vaapi": "VA-API", "qsv": "Intel Quick Sync", "nvdec": "NVIDIA", "nvenc": "NVIDIA", "cuda": "NVIDIA",
	"videotoolbox": "VideoToolbox", "mediacodec": "MediaCodec", "dxva2": "DirectX", "d3d11va": "DirectX", "mf": "Media Foundation"}

func hardware(title, api string) string {
	if title != "" {
		return title
	}
	if api == "" {
		return ""
	}
	return cmp.Or(hwName[strings.ToLower(api)], api)
}

// why says why Plex is converting a stream, and what would let it play as it is. Plex does not
// say outright; this reads its decisions the way the usual causes show in them.
func why(subtitle, video, audio, srcVideo, dstVideo, srcAudio, srcContainer, dstContainer string, srcHeight, dstHeight int, local bool) (string, string) {
	switch {
	case subtitle == "burn":
		return "Burning in subtitles", "Pick a text subtitle (SRT), or turn subtitles off: picture subtitles (PGS, VobSub) have to be drawn into the video."
	case video == "transcode" && srcHeight > 0 && dstHeight > 0 && dstHeight < srcHeight:
		if local {
			return fmt.Sprintf("Lowering the quality to %dp", dstHeight), "Set the player's quality to Original or Maximum."
		}
		return fmt.Sprintf("Lowering the quality to %dp", dstHeight), "The player's remote quality is set below the file's, or the server's upload limit is too low: raise either (Plex Settings → Remote Access)."
	case video == "transcode" && srcVideo != "" && !strings.EqualFold(srcVideo, dstVideo):
		return "The player cannot play " + codec(srcVideo) + " video", "Use a player that plays " + codec(srcVideo) + ", or keep an H.264 copy for it."
	case video == "transcode":
		return "Converting the video", "Set the player's quality to Original; if it already is, the player may not support this file."
	case audio == "transcode" && srcAudio != "":
		return "The player cannot play " + codec(srcAudio) + " audio", "Turn on audio passthrough to the receiver, or keep a stereo AAC or Dolby Digital track."
	case audio == "transcode":
		return "Converting the audio", "Turn on audio passthrough, or check the player's audio settings."
	case srcContainer != "" && dstContainer != "" && !strings.EqualFold(srcContainer, dstContainer):
		return fmt.Sprintf("Repackaging %s as %s", strings.ToUpper(srcContainer), strings.ToUpper(dstContainer)), "Harmless: the picture and sound are untouched."
	}
	return "", ""
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

var (
	// ErrNoStream means the stream to stop is no longer playing.
	ErrNoStream = errors.New("that stream has already ended")
	// ErrStopRefused means Plex would not stop the stream, which takes Plex Pass and the server owner's token.
	ErrStopRefused = errors.New("Plex refused to stop the stream: that takes Plex Pass on the server owner's account, and the owner's token in PLEX_TOKEN")
)

// Terminate stops a playback session; the player shows reason to whoever was watching.
func (c *Client) Terminate(sessionID, reason string) error {
	resp, err := c.request("/status/sessions/terminate", url.Values{"sessionId": {sessionID}, "reason": {reason}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrStopRefused
	case http.StatusNotFound:
		return ErrNoStream
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
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

// History reads Plex's watch history since a time, newest first, at most 2000 entries, with each
// entry's account by name.
func (c *Client) History(since float64) ([]Viewed, error) {
	var r struct {
		MediaContainer struct {
			Metadata []struct {
				ViewedAt         num    `json:"viewedAt"`
				AccountID        num    `json:"accountID"`
				Type             string `json:"type"`
				Title            string `json:"title"`
				GrandparentTitle string `json:"grandparentTitle"`
			}
		}
	}
	q := url.Values{"sort": {"viewedAt:desc"}, "X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {"2000"}}
	if err := c.get("/status/sessions/history/all", q, &r); err != nil {
		return nil, err
	}
	names := c.accounts()
	out := []Viewed{}
	for _, m := range r.MediaContainer.Metadata {
		if float64(m.ViewedAt) < since {
			break
		}
		user := names[int(m.AccountID)]
		if user == "" {
			user = fmt.Sprintf("account %d", int(m.AccountID))
		}
		out = append(out, Viewed{At: float64(m.ViewedAt), User: user, Kind: m.Type, Title: m.Title, Show: m.GrandparentTitle})
	}
	return out, nil
}

// accounts is the server's accounts by id; the owner is account 1.
func (c *Client) accounts() map[int]string {
	var r struct {
		MediaContainer struct {
			Account []struct {
				ID   num    `json:"id"`
				Name string `json:"name"`
			}
		}
	}
	out := map[int]string{}
	if c.get("/accounts", nil, &r) == nil {
		for _, a := range r.MediaContainer.Account {
			if a.Name != "" {
				out[int(a.ID)] = a.Name
			}
		}
	}
	return out
}

// Item is a movie or show in a library, as the media cleanup weighs it.
type Item struct {
	Key     string   `json:"key"`  // Plex's ratingKey
	Kind    string   `json:"kind"` // movie or show
	Title   string   `json:"title"`
	Year    int      `json:"year,omitempty"`
	Library string   `json:"library"`
	Added   float64  `json:"added"`
	Size    int64    `json:"size"`  // a movie's files; 0 for a show, whose episodes are not listed here
	Files   []string `json:"files"` // a movie's files
	// Watched reports the owner's own view count or progress, or (for a show) any episode watched.
	Watched bool `json:"watched"`
	// Guids are the item's ids elsewhere: tmdb://603, tvdb://81189, imdb://tt0133093.
	Guids []string `json:"guids"`
}

// itemsPage is how many items Items asks for at once: a movie with its files, guids, genres and
// cast is a few KB, so a whole large library would not fit in one answer.
const itemsPage = 500

// Items lists a movie or show library, a page at a time. A library that changes while it is read
// is an error rather than a list that may miss an item.
func (c *Client) Items(lib Library) ([]Item, error) {
	out := []Item{}
	seen := map[string]bool{}
	total := -1
	for start := 0; ; start += itemsPage {
		page, size, err := c.itemsFrom(lib, start)
		if err != nil {
			return nil, err
		}
		if total < 0 {
			total = size
		} else if size != total {
			return nil, fmt.Errorf("%s changed while it was read", lib.Title)
		}
		added := 0
		for _, it := range page {
			if !seen[it.Key] {
				seen[it.Key] = true
				out = append(out, it)
				added++
			}
		}
		// A server that ignores the paging sends the whole library each time: a page with nothing
		// new, or one that reaches the total, is the end rather than a reason to ask again.
		if len(page) < itemsPage || added == 0 || (total > 0 && start+len(page) >= total) {
			break
		}
	}
	if total > 0 && len(out) < total {
		return nil, fmt.Errorf("%s changed while it was read: %d of its %d items were listed", lib.Title, len(out), total)
	}
	return out, nil
}

// itemsFrom is one page of a library from start, and how many items Plex says it has in all
// (0 when it does not say).
func (c *Client) itemsFrom(lib Library, start int) ([]Item, int, error) {
	var r struct {
		MediaContainer struct {
			TotalSize num `json:"totalSize"`
			Metadata  []struct {
				RatingKey       string `json:"ratingKey"`
				Type            string `json:"type"`
				Title           string `json:"title"`
				Year            num    `json:"year"`
				AddedAt         num    `json:"addedAt"`
				ViewCount       num    `json:"viewCount"`
				ViewOffset      num    `json:"viewOffset"`
				ViewedLeafCount num    `json:"viewedLeafCount"`
				Guid            guids
				Media           []struct {
					Part []struct {
						File string `json:"file"`
						Size num    `json:"size"`
					}
				}
			}
		}
	}
	q := url.Values{"includeGuids": {"1"}, "X-Plex-Container-Start": {strconv.Itoa(start)}, "X-Plex-Container-Size": {strconv.Itoa(itemsPage)}}
	if err := c.get("/library/sections/"+url.PathEscape(lib.Key)+"/all", q, &r); err != nil {
		return nil, 0, err
	}
	out := make([]Item, 0, len(r.MediaContainer.Metadata))
	for _, m := range r.MediaContainer.Metadata {
		it := Item{Key: m.RatingKey, Kind: m.Type, Title: m.Title, Year: int(m.Year), Library: lib.Title, Added: float64(m.AddedAt),
			Watched: m.ViewCount > 0 || m.ViewOffset > 0 || m.ViewedLeafCount > 0, Files: []string{}, Guids: []string{}}
		it.Guids = append(it.Guids, m.Guid...)
		for _, md := range m.Media {
			for _, p := range md.Part {
				it.Files = append(it.Files, p.File)
				it.Size += int64(p.Size)
			}
		}
		out = append(out, it)
	}
	return out, int(r.MediaContainer.TotalSize), nil
}

// guids is an item's ids elsewhere (tmdb://603, imdb://tt0133093), from its "Guid" list. Plex also sends
// "guid", its own plex:// id as a string, and Go matches keys regardless of case, so both land here: the
// string is skipped rather than failing the whole library.
type guids []string

func (g *guids) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '[' {
		return nil
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, x := range list {
		if x.ID != "" {
			*g = append(*g, x.ID)
		}
	}
	return nil
}

// Sections lists the libraries without counting them.
func (c *Client) Sections() ([]Library, error) {
	var r struct {
		MediaContainer struct {
			Directory []struct {
				Key   string `json:"key"`
				Title string `json:"title"`
				Type  string `json:"type"`
			}
		}
	}
	if err := c.get("/library/sections", nil, &r); err != nil {
		return nil, err
	}
	out := []Library{}
	for _, d := range r.MediaContainer.Directory {
		out = append(out, Library{Key: d.Key, Title: d.Title, Type: d.Type})
	}
	return out, nil
}

// maxWatchHistory is the most watch history entries EverWatched reads.
const maxWatchHistory = 2_000_000

// EverWatched is every item anyone on the server has watched, by ratingKey, from Plex's whole
// watch history: each movie, episode, and the season and show an episode belongs to. History it
// could not read in full is an error, never a shorter map: the media cleanup takes what is missing
// from it as never watched.
func (c *Client) EverWatched() (map[string]bool, error) {
	seen := map[string]bool{}
	const page = 1000
	read, total := 0, -1
	for start := 0; ; start += page {
		if start >= maxWatchHistory {
			return nil, fmt.Errorf("the watch history has more than %d entries, more than Nightjar reads", maxWatchHistory)
		}
		var r struct {
			MediaContainer struct {
				TotalSize num `json:"totalSize"`
				Metadata  []struct {
					RatingKey            string `json:"ratingKey"`
					ParentRatingKey      string `json:"parentRatingKey"`
					GrandparentRatingKey string `json:"grandparentRatingKey"`
				}
			}
		}
		q := url.Values{"X-Plex-Container-Start": {strconv.Itoa(start)}, "X-Plex-Container-Size": {strconv.Itoa(page)}}
		if err := c.get("/status/sessions/history/all", q, &r); err != nil {
			return nil, err
		}
		if total < 0 {
			total = int(r.MediaContainer.TotalSize)
		}
		read += len(r.MediaContainer.Metadata)
		for _, m := range r.MediaContainer.Metadata {
			for _, k := range []string{m.RatingKey, m.ParentRatingKey, m.GrandparentRatingKey} {
				if k != "" {
					seen[k] = true
				}
			}
		}
		if len(r.MediaContainer.Metadata) < page {
			break
		}
	}
	// Entries deleted while it was read shift the pages, so some may have been skipped.
	if read < total {
		return nil, fmt.Errorf("the watch history changed while it was read: %d of its %d entries were read", read, total)
	}
	return seen, nil
}

// DeleteItem deletes an item and its files from Plex, which needs "Allow media deletion" on in
// the server's settings.
func (c *Client) DeleteItem(key string) error {
	u := *c.base
	u.Path += "/library/metadata/" + url.PathEscape(key)
	req, err := http.NewRequest(http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", "nightjar")
	req.Header.Set("X-Plex-Client-Identifier", "nightjar")
	if c.token != "" {
		req.Header.Set("X-Plex-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("Plex refused to delete it: turn on Allow media deletion in Plex's settings (Server, Library)")
	case resp.StatusCode >= 300:
		return fmt.Errorf("Plex answered HTTP %d", resp.StatusCode)
	}
	return nil
}
