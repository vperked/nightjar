package notify

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"nightjar/internal/config"
	"nightjar/internal/discord"
)

// ---------- ntfy ----------

var ntfyPriority = map[discord.Severity]int{discord.Info: 2, discord.OK: 3, discord.Warning: 4, discord.Critical: 5}
var ntfyTags = map[discord.Severity]string{discord.Info: "information_source", discord.OK: "white_check_mark", discord.Warning: "warning", discord.Critical: "rotating_light"}

// NewNtfy publishes to an ntfy topic URL such as https://ntfy.sh/my-alerts, with an optional access token.
func NewNtfy(topicURL, token string, origin discord.Origin) (Channel, error) {
	u, err := url.Parse(strings.TrimRight(topicURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return nil, fmt.Errorf("NTFY_URL must look like https://ntfy.sh/your-topic, got %q", topicURL)
	}
	topic := u.Path[strings.LastIndex(u.Path, "/")+1:]
	u.Path = strings.TrimSuffix(u.Path, "/"+topic)
	server := u.String()
	return newBase("ntfy", func(a Alert) (*http.Request, error) {
		// JSON publishing keeps emoji and accents out of HTTP headers.
		req, err := jsonRequest(server+"/", map[string]any{
			"topic":    topic,
			"title":    a.Title,
			"message":  joinNonEmpty("\n\n", a.Description, lines(a.Fields, func(n, v string) string { return "**" + n + ":** " + v }), origin.Footer(a.Category)),
			"markdown": true,
			"priority": ntfyPriority[a.Severity],
			"tags":     []string{ntfyTags[a.Severity]},
			"click":    origin.DashboardURL,
		})
		if err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req, err
	}), nil
}

// ---------- Telegram ----------

// TelegramAPI is the Bot API base URL; tests point it at a fake server.
var TelegramAPI = "https://api.telegram.org"

// NewTelegram sends through a bot (token from @BotFather) to a chat, group or channel ID.
func NewTelegram(botToken, chatID string, origin discord.Origin) (Channel, error) {
	if botToken == "" || chatID == "" {
		return nil, errors.New("telegram needs both TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID")
	}
	return newBase("Telegram", func(a Alert) (*http.Request, error) {
		return jsonRequest(TelegramAPI+"/bot"+botToken+"/sendMessage", map[string]any{
			"chat_id":                  chatID,
			"text":                     telegramText(a, origin),
			"parse_mode":               "HTML",
			"disable_web_page_preview": true,
			"disable_notification":     a.Severity == discord.Info,
		})
	}), nil
}

// telegramLimit is how many characters a Telegram message may have, counted after its HTML is parsed.
const telegramLimit = 4096

// telegramText renders an alert as Telegram HTML within telegramLimit. The description and field
// values are cut before they are escaped and marked up, so no tag or entity is ever cut in half.
func telegramText(a Alert, origin discord.Origin) string {
	runes := utf8.RuneCountInString
	title := discord.Truncate(a.Title, 256)
	footer := origin.Footer(a.Category)
	// What is left for the description and fields, counting their raw text: markup only shortens it.
	room := telegramLimit - runes(title) - runes(footer) - 16
	desc := discord.Truncate(a.Description, max(0, room))
	room -= runes(desc) + 2
	var fields []discord.Field
	for _, f := range a.Fields {
		if f.Value == "" {
			continue
		}
		name := discord.Truncate(f.Name, 256)
		left := room - runes(name) - 3
		if left < 20 {
			break
		}
		f.Name, f.Value = name, discord.Truncate(f.Value, left)
		fields = append(fields, f)
		room = left - runes(f.Value)
	}

	head := "<b>" + a.Severity.Emoji() + " " + html.EscapeString(title) + "</b>"
	if origin.DashboardURL != "" {
		head = `<a href="` + html.EscapeString(origin.DashboardURL) + `">` + head + "</a>"
	}
	return joinNonEmpty("\n\n",
		head,
		htmlText(desc),
		lines(fields, func(n, v string) string { return "<b>" + html.EscapeString(n) + ":</b> " + htmlText(v) }),
		"<i>"+html.EscapeString(footer)+"</i>",
	)
}

// ---------- Slack ----------

// NewSlack posts to a Slack incoming webhook.
func NewSlack(webhookURL string, origin discord.Origin) (Channel, error) {
	if u, err := url.Parse(webhookURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("SLACK_WEBHOOK must be an incoming webhook URL (https://hooks.slack.com/…)")
	}
	// Slack reads <!channel>, <@user> and <url|label> in text as commands, so alert text (which
	// can hold a torrent's or container's name) has &, < and > escaped before anything else.
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	mrkdwn := func(s string) string { return boldRE.ReplaceAllString(escape(s), "*$1*") }
	return newBase("Slack", func(a Alert) (*http.Request, error) {
		var fields []map[string]any
		for _, f := range a.Fields {
			fields = append(fields, map[string]any{"title": escape(f.Name), "value": mrkdwn(f.Value), "short": f.Inline})
		}
		return jsonRequest(webhookURL, map[string]any{
			"text": a.Severity.Emoji() + " " + escape(a.Title), // shown in notifications
			"attachments": []map[string]any{{
				"color":      fmt.Sprintf("#%06X", a.Severity.Color()),
				"title":      escape(a.Title),
				"title_link": origin.DashboardURL,
				"text":       mrkdwn(a.Description),
				"fields":     fields,
				"footer":     escape(origin.Footer(a.Category)),
				"ts":         a.Time.Unix(),
			}},
		})
	}), nil
}

// ---------- generic webhook ----------

// NewWebhook POSTs each alert as JSON to any URL, for home automation or custom tooling.
func NewWebhook(endpoint string, origin discord.Origin) (Channel, error) {
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("ALERT_WEBHOOK_URL must be an http(s) URL, got %q", endpoint)
	}
	return newBase("Webhook", func(a Alert) (*http.Request, error) {
		fields := make([]map[string]string, 0, len(a.Fields))
		for _, f := range a.Fields {
			fields = append(fields, map[string]string{"name": f.Name, "value": plain(f.Value)})
		}
		return jsonRequest(endpoint, map[string]any{
			"title":         a.Title,
			"description":   plain(a.Description),
			"severity":      a.Severity.String(),
			"fields":        fields,
			"host":          origin.Host,
			"category":      a.Category,
			"dashboard_url": origin.DashboardURL,
			"time":          a.Time.UTC().Format(time.RFC3339),
		})
	}), nil
}

// ---------- email ----------

// SMTP is an email server and the addresses alerts go between.
type SMTP struct {
	Host, Username, Password, From string
	Port                           int
	To                             []string
}

type email struct {
	delivery
	cfg     SMTP
	origin  discord.Origin
	breaker *discord.CircuitBreaker
}

// NewEmail sends alerts by SMTP: implicit TLS on port 465, STARTTLS elsewhere when offered.
func NewEmail(cfg SMTP, origin discord.Origin) (Channel, error) {
	switch {
	case cfg.Host == "":
		return nil, errors.New("SMTP_HOST is required")
	case cfg.From == "" || len(cfg.To) == 0:
		return nil, errors.New("SMTP_FROM and SMTP_TO are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &email{cfg: cfg, origin: origin, breaker: discord.NewCircuitBreaker(5, time.Minute, 2).Named("Email")}, nil
}

func (e *email) Name() string { return "Email" }

func (e *email) SendEmbed(title, description string, sev discord.Severity, fields []discord.Field, footer string) bool {
	msg := e.message(newAlert(title, description, sev, fields, footer))
	return e.record("Email", e.breaker.Call(func() error { return e.send(msg) }))
}

func (e *email) message(a Alert) []byte {
	const boundary = "nightjar-alert"
	subject := mime.QEncoding.Encode("utf-8", a.Severity.Emoji()+" "+a.Title)
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n", e.cfg.From, strings.Join(e.cfg.To, ", "), subject, a.Time.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)

	// Both parts are quoted-printable, which keeps every line well under SMTP's 998-octet limit.
	part := func(contentType, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, contentType)
		qp := quotedprintable.NewWriter(&b)
		qp.Write([]byte(body))
		qp.Close()
		b.WriteString("\r\n")
	}
	text := joinNonEmpty("\n\n", a.Title, plain(a.Description), lines(a.Fields, func(n, v string) string { return n + ": " + plain(v) }), e.origin.Footer(a.Category), e.origin.DashboardURL)
	part("text/plain", text)

	var rows strings.Builder
	for _, f := range a.Fields {
		fmt.Fprintf(&rows, `<tr><td style="padding:6px 16px 6px 0;color:#6f6b62;vertical-align:top">%s</td><td style="padding:6px 0">%s</td></tr>`,
			html.EscapeString(f.Name), strings.ReplaceAll(htmlText(f.Value), "\n", "<br>"))
	}
	link := ""
	if e.origin.DashboardURL != "" {
		link = fmt.Sprintf(` · <a href="%s" style="color:#2a78d6">Open the dashboard</a>`, html.EscapeString(e.origin.DashboardURL))
	}
	part("text/html", fmt.Sprintf(`<div style="font-family:system-ui,sans-serif;font-size:14px;color:#1a1915;background:#f6f5f1;padding:24px">`+
		`<div style="max-width:560px;margin:0 auto;background:#fcfbf8;border:1px solid #e3e0d8;border-left:4px solid #%06X;border-radius:12px;padding:20px 24px">`+
		`<h2 style="margin:0 0 8px;font-size:18px">%s %s</h2><p style="margin:0 0 14px;color:#4a4740">%s</p>`+
		`<table style="border-collapse:collapse;font-size:13px">%s</table>`+
		`<p style="margin:16px 0 0;font-size:12px;color:#6f6b62">%s%s</p></div></div>`,
		a.Severity.Color(), a.Severity.Emoji(), html.EscapeString(a.Title), htmlText(a.Description), rows.String(), html.EscapeString(e.origin.Footer(a.Category)), link))
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

func (e *email) send(msg []byte) error {
	addr := net.JoinHostPort(e.cfg.Host, strconv.Itoa(e.cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{ServerName: e.cfg.Host}
	var conn net.Conn
	var err error
	if e.cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && e.cfg.Port != 465 {
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if e.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(e.cfg.From); err != nil {
		return err
	}
	for _, to := range e.cfg.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// ---------- from settings ----------

// FromSettings builds every channel besides Discord that cfg has settings for. A channel whose
// settings are incomplete or invalid is left out and reported in problems.
func FromSettings(cfg config.Settings, origin discord.Origin) (channels []Channel, problems []error) {
	add := func(c Channel, err error) {
		if err != nil {
			problems = append(problems, err)
			return
		}
		channels = append(channels, c)
	}
	if cfg.NtfyURL != "" {
		add(NewNtfy(cfg.NtfyURL, cfg.NtfyToken, origin))
	}
	if cfg.TelegramBotToken != "" || cfg.TelegramChatID != "" {
		add(NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID, origin))
	}
	if cfg.SlackWebhookURL != "" {
		add(NewSlack(cfg.SlackWebhookURL, origin))
	}
	if cfg.DiscordBotToken != "" || cfg.DiscordChannelID != "" {
		b, err := discord.NewBot(cfg.DiscordBotToken, cfg.DiscordChannelID, cfg.DiscordPublicKey, cfg.DiscordAllowedUsers, origin)
		if err != nil {
			add(nil, err)
		} else {
			add(b, nil)
		}
	}
	if cfg.AlertWebhookURL != "" {
		add(NewWebhook(cfg.AlertWebhookURL, origin))
	}
	if cfg.SMTPHost != "" {
		add(NewEmail(SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, To: cfg.SMTPTo}, origin))
	}
	return channels, problems
}

// ---------- plugins ----------

// PluginWait is how long an alert waits for a plugin's program before giving up on it (the program
// may run on to its own timeout); tests shorten it. Alerts are sent from the monitoring loop, which
// a slow plugin must not hold up.
var PluginWait = 20 * time.Second

// plugin hands each alert to a plugin's program, which delivers it however it likes.
type plugin struct {
	delivery
	name    string
	origin  discord.Origin
	send    func(payload map[string]any) error
	breaker *discord.CircuitBreaker
}

// NewPlugin is a channel for a plugin that provides notify. send runs the plugin with the alert.
func NewPlugin(name string, send func(payload map[string]any) error, origin discord.Origin) Channel {
	return &plugin{name: name, send: send, origin: origin, breaker: discord.NewCircuitBreaker(5, time.Minute, 2).Named(name)}
}

func (p *plugin) Name() string { return p.name }

func (p *plugin) SendEmbed(title, description string, sev discord.Severity, fields []discord.Field, footer string) bool {
	a := newAlert(title, description, sev, fields, footer)
	list := make([]map[string]string, 0, len(a.Fields))
	for _, f := range a.Fields {
		list = append(list, map[string]string{"name": f.Name, "value": plain(f.Value)})
	}
	payload := map[string]any{
		"title":         a.Title,
		"description":   plain(a.Description),
		"severity":      a.Severity.String(),
		"fields":        list,
		"host":          p.origin.Host,
		"category":      a.Category,
		"dashboard_url": p.origin.DashboardURL,
		"time":          a.Time.UTC().Format(time.RFC3339),
	}
	return p.record(p.name, p.breaker.Call(func() error {
		done := make(chan error, 1)
		go func() { done <- p.send(payload) }()
		select {
		case err := <-done:
			return err
		case <-time.After(PluginWait):
			return fmt.Errorf("no answer after %s", PluginWait)
		}
	}))
}
