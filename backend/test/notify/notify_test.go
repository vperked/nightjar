package notify_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/discord"
	"nightjar/internal/notify"
)

var origin = discord.Origin{Host: "nas", DashboardURL: "https://monitor.example.com"}

// capture is an HTTP endpoint that records the last request and answers with status.
type capture struct {
	*httptest.Server
	mu     sync.Mutex
	path   string
	header http.Header
	body   map[string]any
	status int
}

func newCapture(t *testing.T) *capture {
	c := &capture{status: http.StatusOK}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.path, c.header = r.URL.Path, r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&c.body)
		w.WriteHeader(c.status)
		w.Write([]byte("nope"))
	}))
	t.Cleanup(c.Close)
	return c
}

func send(c notify.Channel, sev discord.Severity) bool {
	return c.SendEmbed("Service Down: api", "Request **failed** with `502`", sev, []discord.Field{{Name: "Target", Value: "https://api.local"}}, "Uptime")
}

func TestNtfy(t *testing.T) {
	srv := newCapture(t)
	ch, err := notify.NewNtfy(srv.URL+"/alerts", "tk", origin)
	if err != nil || !send(ch, discord.Critical) {
		t.Fatalf("send failed: %v %s", err, ch.LastError())
	}
	b := srv.body
	if srv.path != "/" || b["topic"] != "alerts" || b["priority"].(float64) != 5 || b["markdown"] != true || b["click"] != origin.DashboardURL {
		t.Fatalf("ntfy payload %v at %s", b, srv.path)
	}
	if !strings.Contains(b["message"].(string), "**Target:** https://api.local") || srv.header.Get("Authorization") != "Bearer tk" {
		t.Fatalf("message %q auth %q", b["message"], srv.header.Get("Authorization"))
	}
	if _, err := notify.NewNtfy("https://ntfy.sh", "", origin); err == nil {
		t.Fatal("a URL without a topic should be rejected")
	}
}

func TestTelegram(t *testing.T) {
	srv := newCapture(t)
	defer func(api string) { notify.TelegramAPI = api }(notify.TelegramAPI)
	notify.TelegramAPI = srv.URL
	ch, _ := notify.NewTelegram("123:abc", "-100", origin)
	ch.SendEmbed("<script>", "**bold**", discord.Info, nil, "")
	b := srv.body
	text := b["text"].(string)
	if srv.path != "/bot123:abc/sendMessage" || b["chat_id"] != "-100" || b["parse_mode"] != "HTML" || b["disable_notification"] != true {
		t.Fatalf("telegram request %s %v", srv.path, b)
	}
	if !strings.Contains(text, "&lt;script&gt;") || !strings.Contains(text, "<b>bold</b>") || !strings.Contains(text, `href="https://monitor.example.com"`) {
		t.Fatalf("telegram text %q", text)
	}
	if _, err := notify.NewTelegram("", "1", origin); err == nil {
		t.Fatal("missing token should be rejected")
	}
}

func TestSlackAndWebhook(t *testing.T) {
	slack := newCapture(t)
	ch, _ := notify.NewSlack(slack.URL, origin)
	send(ch, discord.Warning)
	att := slack.body["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#FAB219" || att["text"] != "Request *failed* with `502`" || att["footer"] != "nas · Uptime" {
		t.Fatalf("slack attachment %v", att)
	}

	hook := newCapture(t)
	ch, _ = notify.NewWebhook(hook.URL, origin)
	send(ch, discord.Warning)
	b := hook.body
	if b["severity"] != "warning" || b["description"] != "Request failed with 502" || b["host"] != "nas" || b["category"] != "Uptime" {
		t.Fatalf("webhook payload %v", b)
	}
}

func TestFailuresAreReported(t *testing.T) {
	srv := newCapture(t)
	srv.status = http.StatusInternalServerError
	ch, _ := notify.NewWebhook(srv.URL, origin)
	results := notify.Test([]notify.Channel{ch})
	if len(results) != 1 || results[0].OK || !strings.Contains(results[0].Error, "HTTP 500: nope") {
		t.Fatalf("results %+v", results)
	}
}

// fakeSMTP accepts one message and returns its DATA.
func fakeSMTP(t *testing.T) (port int, data <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		reply := func(s string) { conn.Write([]byte(s + "\r\n")) }
		reply("220 fake")
		var body strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					out <- body.String()
					reply("250 queued")
					continue
				}
				body.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
			case "EHLO", "HELO":
				reply("250 fake")
			case "DATA":
				inData = true
				reply("354 go")
			case "QUIT":
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestEmail(t *testing.T) {
	port, data := fakeSMTP(t)
	ch, err := notify.NewEmail(notify.SMTP{Host: "127.0.0.1", Port: port, From: "monitor@nas", To: []string{"me@example.com"}}, origin)
	if err != nil || !send(ch, discord.Critical) {
		t.Fatalf("email failed: %v %s", err, ch.LastError())
	}
	msg := <-data
	for _, want := range []string{"Subject: =?utf-8?q?", "To: me@example.com", "Content-Type: text/html", "Service Down: api", "<b>failed</b>", "Open the dashboard", "#D03B3B"} {
		if !strings.Contains(msg, want) {
			t.Errorf("email lacks %q", want)
		}
	}
	if _, err := notify.NewEmail(notify.SMTP{Host: "x"}, origin); err == nil {
		t.Fatal("missing addresses should be rejected")
	}
}
