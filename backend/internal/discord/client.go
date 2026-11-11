// Package discord sends alert embeds to Discord webhooks behind a rate limiter and circuit breaker.
package discord

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"nightjar/internal/logx"
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
	fit(&e)
	e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return e
}

// embedLimit is the most characters Discord takes across an embed's title, description, field
// names and values, and footer.
const embedLimit = 6000

// fit keeps an embed within embedLimit: the description gives way first (down to 1024
// characters), then the last fields, then the rest of the description.
func fit(e *Embed) {
	n := utf8.RuneCountInString
	total := n(e.Title) + n(e.Description)
	for _, f := range e.Fields {
		total += n(f.Name) + n(f.Value)
	}
	if e.Footer != nil {
		total += n(e.Footer.Text)
	}
	cut := func(keep int) {
		if over := total - embedLimit; over > 0 && n(e.Description) > keep {
			d := n(e.Description)
			e.Description = Truncate(e.Description, max(keep, d-over))
			total -= d - n(e.Description)
		}
	}
	cut(1024)
	for total > embedLimit && len(e.Fields) > 0 {
		last := e.Fields[len(e.Fields)-1]
		total -= n(last.Name) + n(last.Value)
		e.Fields = e.Fields[:len(e.Fields)-1]
	}
	cut(0)
}

// tidy drops fields with nothing to say, so an embed shows only what it knows.
func tidy(fields []Field) []Field {
	out := make([]Field, 0, len(fields))
	for _, f := range fields {
		v := strings.TrimSpace(f.Value)
		if v == "" || v == "n/a" || v == "N/A" || v == "None" {
			continue
		}
		out = append(out, f)
	}
	return out
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

// areaLabel is how a category reads in a footer. Categories also route alerts, so several older
// ones read as the area they belong to.
var areaLabel = map[string]string{
	"Storage Monitoring": "Storage", "Directory Monitoring": "Storage", "Disk": "Storage", "Emergency": "Storage",
	"Kernel Warning": "Kernel", "Kernel Stability": "Kernel",
	"Watchdog": "Nightjar", "Error Alert": "Nightjar", "Metrics Report": "Nightjar", "Monitor": "Nightjar",
}

// Footer is where an alert comes from: the host and the area ("nas · Storage"), or "Nightjar"
// and the area when the host is unknown.
func (o Origin) Footer(category string) string {
	area := category
	if l, ok := areaLabel[category]; ok {
		area = l
	}
	parts := []string{}
	for _, p := range []string{cmp.Or(o.Host, "Nightjar"), area} {
		if p != "" && !slices.Contains(parts, p) {
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
	classic      atomic.Bool // the webhook refused components, so alerts go as embeds
	// RetryWaits are the pauses before trying a delivery again that failed fast on the network,
	// as when the machine has just booted and its DNS is not up yet, or Discord answered 5xx.
	RetryWaits []time.Duration
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
		client:       &http.Client{Timeout: 10 * time.Second, CheckRedirect: KeepMethod},
		limiter:      &rateLimiter{max: 30, period: 60 * time.Second},
		breaker:      NewCircuitBreaker(5, 60*time.Second, 2).Named("Discord"),
		RetryWaits:   []time.Duration{time.Second, 3 * time.Second, 6 * time.Second},
	}, nil
}

// retryable is a failure worth trying again at once: the network not up yet (no DNS, refused,
// reset, unreachable) or Discord failing for a moment (5xx). A timeout is not: trying again would
// hold up the caller for as long again.
func retryable(status int, err error) bool {
	if status >= 500 {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return !dns.IsTimeout
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}

// SendEmbed sends one alert. It never blocks on a saturated rate limit or open breaker: the alert
// goes to the fallback log instead. Alerts go as Components V2 messages; a webhook that refuses
// them gets the classic embed from then on.
func (w *Webhook) SendEmbed(title, description string, sev Severity, fields []Field, footer string) bool {
	fallback := title
	if description != "" {
		fallback = fmt.Sprintf("%s: %s", title, Truncate(description, 100))
	}
	if !w.classic.Load() {
		status, err := w.send(withComponents(w.URL), Message(title, description, sev, fields, footer, w.Origin, time.Now()))
		if err == nil {
			w.setError("")
			return true
		}
		if status != http.StatusBadRequest {
			w.failed(err, fallback)
			return false
		}
		log.Warn("Discord refused a components message; sending classic embeds from now on", "err", err)
		w.classic.Store(true)
	}
	// The colour bar says how serious it is; the title says what happened, without an emoji.
	embed := BuildEmbed(title, description, sev.Color(), tidy(fields), w.Origin.Footer(footer))
	embed.URL = w.Origin.DashboardURL
	return w.post(map[string]any{"embeds": []Embed{embed}}, fallback)
}

// SendMessage sends a plain-text message.
func (w *Webhook) SendMessage(content string) bool {
	return w.post(map[string]any{"content": Truncate(content, 2000)}, content)
}

var errRateLimited = errors.New("discord returned 429")

func (w *Webhook) post(payload any, fallback string) bool {
	if _, err := w.send(w.URL, payload); err != nil {
		w.failed(err, fallback)
		return false
	}
	w.setError("")
	return true
}

// failed notes why a delivery failed and keeps the alert in the fallback log.
func (w *Webhook) failed(err error, fallback string) {
	w.setError(logx.Redact(err.Error()))
	w.logFallback(fallback)
}

// send posts payload to target behind the breaker and the rate limit, with the HTTP status.
func (w *Webhook) send(target string, payload any) (int, error) {
	if w.breaker.IsOpen() {
		log.Debug("Circuit breaker open, using fallback")
		return 0, errors.New("paused after repeated failures")
	}
	if !w.limiter.tryAcquire() {
		log.Warn("Rate limit reached, dropping alert", "next_slot_s", fmt.Sprintf("%.1f", w.limiter.retryAfter().Seconds()))
		return 0, errors.New("rate limited")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Error("Encode payload", "err", err)
		return 0, err
	}
	status := 0
	// One delivery is one call through the breaker, however many tries it takes.
	err = w.breaker.Call(func() error {
		var resp *http.Response
		var err error
		for try := 0; ; try++ {
			status = 0
			resp, err = w.client.Post(target, "application/json", bytes.NewReader(body))
			if err == nil {
				status = resp.StatusCode
			}
			if try >= len(w.RetryWaits) || !retryable(status, err) {
				break
			}
			if resp != nil {
				resp.Body.Close()
			}
			log.Debug("Delivery failed; trying again", "status", status, "err", err, "in", w.RetryWaits[try])
			time.Sleep(w.RetryWaits[try])
		}
		if err != nil {
			return withoutURL(err)
		}
		defer resp.Body.Close()
		status = resp.StatusCode
		if resp.StatusCode == http.StatusTooManyRequests {
			log.Warn("Discord returned 429", "retry_after", resp.Header.Get("Retry-After"))
			return errRateLimited
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return RedirectError(resp)
		}
		if resp.StatusCode >= 400 {
			// Discord says why in the body; keep a little of it for the dashboard.
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			if m := strings.TrimSpace(string(msg)); m != "" {
				return fmt.Errorf("HTTP %d: %s", resp.StatusCode, m)
			}
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errRateLimited) && !errors.Is(err, ErrBreakerOpen) {
		log.Warn("Webhook request failed", "err", err)
	}
	return status, err
}

// KeepMethod is an http.Client's CheckRedirect for alert deliveries. A 301, 302 or 303 turns a POST
// into a GET without its body, which a server may well answer 2xx: the alert would count as sent
// though nothing arrived. Those come back as the response instead, for RedirectError; 307 and 308
// keep the method and body, so they are followed.
func KeepMethod(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.Method != via[0].Method {
		return http.ErrUseLastResponse
	}
	return nil
}

// RedirectError says a delivery was redirected rather than taken, and where to, so the address can
// be fixed (often http:// that should be https://). Only the scheme and host are named: the rest of
// a webhook's address is its secret.
func RedirectError(resp *http.Response) error {
	where := "elsewhere"
	if loc, err := resp.Location(); err == nil && loc.Host != "" {
		where = loc.Scheme + "://" + loc.Host + "/…"
	}
	return fmt.Errorf("HTTP %d: the address redirects to %s; use that address instead", resp.StatusCode, where)
}

// withoutURL drops the request URL from a network error: a webhook's URL holds its token, and the
// error is kept and shown in the dashboard.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
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
