package discord_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/discord"
)

func TestBot(t *testing.T) {
	var mu sync.Mutex
	var posted []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posted = append(posted, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		mu.Unlock()
	}))
	defer api.Close()
	pub, priv, _ := ed25519.GenerateKey(nil)
	bot, err := discord.NewBot("tok", "123", hex.EncodeToString(pub), []string{"42"}, discord.Origin{Host: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	bot.API = api.URL
	bot.Buttons = func(subject string) []discord.Button {
		return []discord.Button{{ID: "again:" + strings.TrimPrefix(subject, "check:"), Label: "Check again", Style: 1}}
	}
	acted := make(chan string, 1)
	bot.Act = func(_ context.Context, id, user string) (string, error) {
		acted <- id + " by " + user
		return "Checking plex again now.", nil
	}

	// An alert goes to the channel as the bot, with its buttons.
	if !bot.SendAbout("check:plex", "plex is down", "timed out", discord.Critical, nil, "Uptime") {
		t.Fatal(bot.LastError())
	}
	if !strings.Contains(posted[0], "POST /channels/123/messages Bot tok") || !strings.Contains(posted[0], `"custom_id":"again:plex"`) {
		t.Fatalf("post: %s", posted[0])
	}

	click := func(body string, sign bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/discord/interactions", strings.NewReader(body))
		stamp := "1700000000"
		req.Header.Set("X-Signature-Timestamp", stamp)
		sig := ed25519.Sign(priv, append([]byte(stamp), body...))
		if !sign {
			sig[0] ^= 1
		}
		req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
		rec := httptest.NewRecorder()
		bot.Interactions(rec, req)
		return rec
	}
	if rec := click(`{"type":1}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", rec.Code)
	}
	if rec := click(`{"type":1}`, true); !strings.Contains(rec.Body.String(), `"type":1`) {
		t.Fatalf("ping: %s", rec.Body)
	}
	rec := click(`{"type":3,"token":"itok","application_id":"app","data":{"custom_id":"again:plex"},"member":{"user":{"id":"7"}}}`, true)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["type"] != float64(4) || !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("a stranger: %s", rec.Body)
	}
	rec = click(`{"type":3,"token":"itok","application_id":"app","data":{"custom_id":"again:plex"},"member":{"user":{"id":"42"}}}`, true)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"type":5`)) {
		t.Fatalf("deferred: %s", rec.Body)
	}
	select {
	case got := <-acted:
		if got != "again:plex by 42" {
			t.Fatalf("acted %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not acted on")
	}
	// The outcome replaces the "thinking" reply.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(posted)
		last := posted[n-1]
		mu.Unlock()
		if n == 2 {
			if !strings.Contains(last, "PATCH /webhooks/app/itok/messages/@original") || !strings.Contains(last, "Checking plex again now.") {
				t.Fatalf("follow-up: %s", last)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no follow-up")
}
