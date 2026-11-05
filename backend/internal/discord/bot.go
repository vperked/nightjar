package discord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A Discord bot posts alerts like the webhook does, with buttons to act on them: check again,
// restart the service, run the repair, mute for an hour, keep a title the cleanup would remove.
// Discord sends each click to Nightjar's interactions endpoint, signed with the bot's public key;
// only the Discord users the owner allows may act.

// Button is one action under an alert. Its ID says what to do ("again:plex", "mute:check:plex").
type Button struct {
	ID    string
	Label string
	Style int // 1 primary, 2 secondary, 4 danger
}

const apiBase = "https://discord.com/api/v10"

// Bot posts to one channel and answers clicks. Its methods are safe for concurrent use.
type Bot struct {
	Token     string
	Channel   string
	PublicKey ed25519.PublicKey
	Allowed   []string // Discord user ids that may click; empty lets no one
	Origin    Origin
	// Buttons are the actions to offer under an alert about subject.
	Buttons func(subject string) []Button
	// Act runs a button's action for a Discord user and says how it went.
	Act func(ctx context.Context, id, user string) (string, error)
	// API is Discord's API root; tests replace it.
	API string

	http    *http.Client
	breaker *CircuitBreaker
	mu      sync.Mutex
	lastErr string
	// seen holds the interactions handled lately, by id, so a replayed request does nothing.
	seen map[string]time.Time
}

// freshFor is how far a click's signed timestamp may be from now; anything older is a replay.
const freshFor = 5 * time.Minute

// replayed reports whether interaction id was already handled, and remembers it if not.
func (b *Bot) replayed(id string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, at := range b.seen {
		if now.Sub(at) > 2*freshFor {
			delete(b.seen, k)
		}
	}
	if _, ok := b.seen[id]; ok {
		return true
	}
	if b.seen == nil {
		b.seen = map[string]time.Time{}
	}
	b.seen[id] = now
	return false
}

// NewBot checks the settings and makes a bot.
func NewBot(token, channel, publicKey string, allowed []string, origin Origin) (*Bot, error) {
	if token == "" || channel == "" {
		return nil, errors.New("a Discord bot needs its token and the channel id to post in")
	}
	key, err := hex.DecodeString(strings.TrimSpace(publicKey))
	if publicKey != "" && (err != nil || len(key) != ed25519.PublicKeySize) {
		return nil, errors.New("the Discord bot's public key is the 64 hex characters on its application's General Information page")
	}
	return &Bot{Token: token, Channel: channel, PublicKey: key, Allowed: allowed, Origin: origin, API: apiBase,
		http: &http.Client{Timeout: 15 * time.Second}, breaker: NewCircuitBreaker(5, time.Minute, 2).Named("Discord bot")}, nil
}

func (b *Bot) Name() string { return "Discord bot" }

func (b *Bot) LastError() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

func (b *Bot) SendEmbed(title, description string, sev Severity, fields []Field, footer string) bool {
	return b.SendAbout("", title, description, sev, fields, footer)
}

// SendAbout posts an alert about subject, with its buttons when the bot can take clicks.
func (b *Bot) SendAbout(subject, title, description string, sev Severity, fields []Field, footer string) bool {
	msg := Message(title, description, sev, fields, footer, b.Origin, time.Now())
	if len(b.PublicKey) > 0 && b.Buttons != nil && subject != "" {
		if row := actionRow(b.Buttons(subject)); row != nil {
			box := msg["components"].([]Component)[0]
			box["components"] = append(box["components"].([]Component), row)
		}
	}
	err := b.breaker.Call(func() error {
		return b.call(context.Background(), http.MethodPost, "/channels/"+b.Channel+"/messages", msg)
	})
	b.mu.Lock()
	b.lastErr = ""
	if err != nil {
		b.lastErr = err.Error()
	}
	b.mu.Unlock()
	return err == nil
}

func actionRow(buttons []Button) Component {
	if len(buttons) == 0 {
		return nil
	}
	var parts []Component
	for _, bt := range buttons[:min(5, len(buttons))] {
		parts = append(parts, Component{"type": 2, "style": max(1, bt.Style), "label": Truncate(bt.Label, 80), "custom_id": Truncate(bt.ID, 100)})
	}
	return Component{"type": 1, "components": parts}
}

func (b *Bot) call(ctx context.Context, method, path string, body any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, b.API+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if !strings.HasPrefix(path, "/webhooks/") {
		req.Header.Set("Authorization", "Bot "+b.Token)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("Discord answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Interactions is the endpoint Discord sends clicks to (set as the application's Interactions
// Endpoint URL). It answers at once and reports the action's outcome to the clicker when done.
func (b *Bot) Interactions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	sig, _ := hex.DecodeString(r.Header.Get("X-Signature-Ed25519"))
	stamp := r.Header.Get("X-Signature-Timestamp")
	if err != nil || len(b.PublicKey) == 0 || len(sig) != ed25519.SignatureSize || !ed25519.Verify(b.PublicKey, append([]byte(stamp), body...), sig) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	// The signature covers the timestamp, so a captured request replayed later is refused here.
	now := time.Now()
	if sec, err := strconv.ParseInt(stamp, 10, 64); err != nil || now.Sub(time.Unix(sec, 0)).Abs() > freshFor {
		http.Error(w, "stale request", http.StatusUnauthorized)
		return
	}
	var in struct {
		ID            string `json:"id"`
		Type          int    `json:"type"`
		Token         string `json:"token"`
		ApplicationID string `json:"application_id"`
		Data          struct {
			CustomID string `json:"custom_id"`
		} `json:"data"`
		Member *struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"member"`
		User *struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	switch in.Type {
	case 1: // Discord checking the endpoint
		reply(map[string]int{"type": 1})
		return
	case 3: // a button
		// And one replayed within that window is recognised by its id.
		if in.ID == "" || b.replayed(in.ID, now) {
			reply(map[string]any{"type": 4, "data": map[string]any{"content": "That click was already handled.", "flags": 64}})
			return
		}
	default:
		reply(map[string]any{"type": 4, "data": map[string]any{"content": "Nightjar only handles its buttons.", "flags": 64}})
		return
	}
	who := ""
	if in.Member != nil {
		who = in.Member.User.ID
	} else if in.User != nil {
		who = in.User.ID
	}
	if !slices.Contains(b.Allowed, who) {
		reply(map[string]any{"type": 4, "data": map[string]any{"content": "You are not allowed to act on Nightjar's alerts. Its owner can add your Discord user id under Settings → Alert channels.", "flags": 64}})
		return
	}
	// Restarts can take longer than the three seconds Discord waits: say "thinking", act, then
	// edit that reply with the outcome. Only the clicker sees it.
	reply(map[string]any{"type": 5, "data": map[string]any{"flags": 64}})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		text := "Done."
		if b.Act != nil {
			out, err := b.Act(ctx, in.Data.CustomID, who)
			text = out
			if err != nil {
				text = "Could not do that: " + err.Error()
			}
		}
		b.call(ctx, http.MethodPatch, "/webhooks/"+in.ApplicationID+"/"+in.Token+"/messages/@original", map[string]any{"content": Truncate(text, 1900)})
	}()
}
