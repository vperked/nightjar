// Package discord sends alert embeds to Discord webhooks behind a rate limiter and circuit breaker.
package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"servermonitor/internal/logx"
)

// Severity sets an alert's colour, emoji and, on channels that have one, its priority.
type Severity int

const (
	Info     Severity = iota // reports, startup, routine cleanup
	OK                       // recovered
	Warning                  // a threshold crossed
	Critical                 // down, crashed, out of space
)

// The dashboard's own palette: series blue, good green, warn amber, crit red.
var severityStyle = map[Severity]struct {
	color int
	emoji string
	name  string
}{
	Info:     {0x2A78D6, "🔵", "info"},
	OK:       {0x0CA30C, "🟢", "ok"},
	Warning:  {0xFAB219, "🟠", "warning"},
	Critical: {0xD03B3B, "🔴", "critical"},
}

func (s Severity) Color() int     { return severityStyle[s].color }
func (s Severity) Emoji() string  { return severityStyle[s].emoji }
func (s Severity) String() string { return severityStyle[s].name }

var log = logx.Component("Discord")

// Field is one embed field.
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// Embed is the subset of a Discord embed the monitor sends.
type Embed struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	URL         string  `json:"url,omitempty"`
	Color       int     `json:"color"`
	Fields      []Field `json:"fields,omitempty"`
	Footer      *struct {
		Text string `json:"text"`
	} `json:"footer,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Truncate cuts s to at most n runes.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// BuildEmbed applies Discord's length limits: title 256, description 4096, 25 fields of 256/1024, footer 2048.
func BuildEmbed(title, description string, color int, fields []Field, footer string) Embed {
	e := Embed{Title: Truncate(title, 256), Description: Truncate(description, 4096), Color: color}
	for i, f := range fields {
		if i >= 25 {
			break
		}
		e.Fields = append(e.Fields, Field{Name: Truncate(f.Name, 256), Value: Truncate(f.Value, 1024), Inline: f.Inline})
	}
	if footer != "" {
		e.Footer = &struct {
			Text string `json:"text"`
		}{Truncate(footer, 2048)}
	}
	e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return e
}

// Sender delivers alerts: Discord, another channel, or several at once. Tests capture them.
type Sender interface {
	SendEmbed(title, description string, sev Severity, fields []Field, footer string) bool
}

// About tags the alerts sent through s with what they are about ("check:Plex",
// "unit:sonarr.service", "drive:S4EVNX0N"), so they can be snoozed and linked to from the dashboard.
// Senders that keep no history send them as they are.
func About(s Sender, subject string) Sender {
	if a, ok := s.(interface{ About(string) Sender }); ok {
		return a.About(subject)
	}
	return s
}

// Origin says where alerts come from: the host's name goes in each footer, and the dashboard URL
// (when set) makes each title a link back to it.
type Origin struct {
	Host         string
	DashboardURL string
}

// Footer joins the product, host and category ("servermonitor · nas · Disk").
func (o Origin) Footer(category string) string {
	parts := []string{"servermonitor"}
	for _, p := range []string{o.Host, category} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// Webhook posts to one Discord webhook URL.
type Webhook struct {
	URL          string
	AlertLogPath string
	Origin       Origin
	client       *http.Client
	limiter      *rateLimiter
	breaker      *CircuitBreaker
	mu           sync.Mutex
	lastErr      string
}

func (w *Webhook) Name() string { return "Discord" }

// LastError is why the most recent delivery failed ("" after a success).
func (w *Webhook) LastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

func (w *Webhook) setError(msg string) {
	w.mu.Lock()
	w.lastErr = msg
	w.mu.Unlock()
}

// NewWebhook validates the URL and builds a client.
func NewWebhook(url, alertLogPath string) (*Webhook, error) {
	if url == "" {
		return nil, errors.New("webhook URL not provided")
	}
	return &Webhook{
		URL:          url,
		AlertLogPath: alertLogPath,
		client:       &http.Client{Timeout: 10 * time.Second},
		limiter:      &rateLimiter{max: 30, period: 60 * time.Second},
		breaker:      NewCircuitBreaker(5, 60*time.Second, 2),
	}, nil
}

// SendEmbed sends one embed. It never blocks on a saturated rate limit or open breaker:
// the alert goes to the fallback log instead.
func (w *Webhook) SendEmbed(title, description string, sev Severity, fields []Field, footer string) bool {
	embed := BuildEmbed(sev.Emoji()+" "+title, description, sev.Color(), fields, w.Origin.Footer(footer))
	embed.URL = w.Origin.DashboardURL
	payload := map[string]any{"embeds": []Embed{embed}}
	fallback := title
	if description != "" {
		fallback = fmt.Sprintf("%s: %s", title, Truncate(description, 100))
	}
	return w.post(payload, fallback)
}

// SendMessage sends a plain-text message.
func (w *Webhook) SendMessage(content string) bool {
	return w.post(map[string]any{"content": Truncate(content, 2000)}, content)
}

var errRateLimited = errors.New("discord returned 429")

func (w *Webhook) post(payload any, fallback string) bool {
	if w.breaker.IsOpen() {
		log.Debug("Circuit breaker open, using fallback")
		w.setError("paused after repeated failures")
		w.logFallback(fallback)
		return false
	}
	if !w.limiter.tryAcquire() {
		log.Warn("Rate limit reached, dropping alert", "next_slot_s", fmt.Sprintf("%.1f", w.limiter.retryAfter().Seconds()))
		w.setError("rate limited")
		w.logFallback(fallback)
		return false
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Error("Encode payload", "err", err)
		return false
	}
	err = w.breaker.Call(func() error {
		resp, err := w.client.Post(w.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			log.Warn("Discord returned 429", "retry_after", resp.Header.Get("Retry-After"))
			return errRateLimited
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, errRateLimited) && !errors.Is(err, ErrBreakerOpen) {
			log.Warn("Webhook request failed", "err", err)
		}
		w.setError(err.Error())
		w.logFallback(fallback)
		return false
	}
	w.setError("")
	return true
}

func (w *Webhook) logFallback(message string) {
	if w.AlertLogPath == "" {
		return
	}
	f, err := os.OpenFile(w.AlertLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Debug("Alert log write failed", "err", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s: %s\n", time.Now().UTC().Format(time.RFC3339), message)
}

type rateLimiter struct {
	mu     sync.Mutex
	max    int
	period time.Duration
	calls  []time.Time
}

func (r *rateLimiter) prune(now time.Time) {
	for len(r.calls) > 0 && r.calls[0].Before(now.Add(-r.period)) {
		r.calls = r.calls[1:]
	}
}

func (r *rateLimiter) tryAcquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.prune(now)
	if len(r.calls) >= r.max {
		return false
	}
	r.calls = append(r.calls, now)
	return true
}

func (r *rateLimiter) retryAfter() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) < r.max {
		return 0
	}
	return max(0, r.period-time.Since(r.calls[0]))
}
