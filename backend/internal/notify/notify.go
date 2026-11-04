// Package notify delivers alerts to channels besides Discord (ntfy, Telegram, Slack, email and a
// generic JSON webhook), and its Hub keeps every alert and routes it to the channels. Every channel
// and route is a discord.Sender, so alert code does not know where its alerts go.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/logx"
)

var log = logx.Component("Notify")

// Alert is one notification as every channel sees it.
type Alert struct {
	Title       string
	Description string
	Severity    discord.Severity
	Fields      []discord.Field
	Category    string
	Time        time.Time
}

// Channel is somewhere alerts can go.
type Channel interface {
	discord.Sender
	Name() string
	// LastError is why the most recent delivery failed ("" after a success).
	LastError() string
}

// delivery remembers a channel's last error and logs failures.
type delivery struct {
	mu      sync.Mutex
	lastErr string
}

// LastError is why the most recent delivery failed ("" after a success).
func (d *delivery) LastError() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastErr
}

// record notes a delivery's outcome and reports whether it worked.
func (d *delivery) record(channel string, err error) bool {
	d.mu.Lock()
	d.lastErr = ""
	if err != nil {
		d.lastErr = err.Error()
	}
	d.mu.Unlock()
	if err != nil && !errors.Is(err, discord.ErrBreakerOpen) {
		log.Warn(channel+" delivery failed", "err", err)
	}
	return err == nil
}

func newAlert(title, description string, sev discord.Severity, fields []discord.Field, category string) Alert {
	return Alert{Title: title, Description: description, Severity: sev, Fields: fields, Category: category, Time: time.Now()}
}

// base is an HTTP channel: render turns an alert into a request, sent behind a circuit breaker.
type base struct {
	delivery
	name    string
	client  *http.Client
	breaker *discord.CircuitBreaker
	render  func(Alert) (*http.Request, error)
}

func newBase(name string, render func(Alert) (*http.Request, error)) *base {
	return &base{
		name: name, render: render,
		client:  &http.Client{Timeout: 10 * time.Second},
		breaker: discord.NewCircuitBreaker(5, time.Minute, 2),
	}
}

func (b *base) Name() string { return b.name }

func (b *base) SendEmbed(title, description string, sev discord.Severity, fields []discord.Field, footer string) bool {
	return b.record(b.name, b.deliver(newAlert(title, description, sev, fields, footer)))
}

func (b *base) deliver(a Alert) error {
	req, err := b.render(a)
	if err != nil {
		return err
	}
	return b.breaker.Call(func() error {
		resp, err := b.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil
	})
}

func jsonRequest(url string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// ---------- text rendering ----------

var (
	boldRE = regexp.MustCompile(`\*\*(.+?)\*\*`)
	codeRE = regexp.MustCompile("`([^`]+)`")
)

// plain strips Discord markdown.
func plain(s string) string {
	s = strings.ReplaceAll(s, "```", "")
	return codeRE.ReplaceAllString(boldRE.ReplaceAllString(s, "$1"), "$1")
}

// htmlText renders Discord markdown as Telegram/email HTML.
func htmlText(s string) string {
	s = html.EscapeString(strings.ReplaceAll(s, "```", ""))
	return codeRE.ReplaceAllString(boldRE.ReplaceAllString(s, "<b>$1</b>"), "<code>$1</code>")
}

// lines renders fields one per line with format, e.g. "%s: %s".
func lines(fields []discord.Field, format func(name, value string) string) string {
	var b strings.Builder
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		b.WriteString(format(f.Name, f.Value))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// Result is one channel's answer to a test message.
type Result struct {
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// Test sends a test message to every channel at once and reports how each went.
func Test(channels []Channel) []Result {
	results := make([]Result, len(channels))
	var wg sync.WaitGroup
	for i, c := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := c.SendEmbed("Test alert", "Alerts from Nightjar reach this channel.", discord.Info,
				[]discord.Field{{Name: "Severity colours", Value: "🔴 critical · 🟠 warning · 🟢 recovered · 🔵 info"}}, "Test")
			results[i] = Result{Channel: c.Name(), OK: ok}
			if !ok {
				results[i].Error = c.LastError()
			}
		}()
	}
	wg.Wait()
	return results
}
