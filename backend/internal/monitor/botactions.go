package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/incidents"
)

// ConnectBot gives the Discord bot, when one is set up, its buttons and what they do.
func ConnectBot(c *Context) {
	for _, ch := range c.Channels {
		if b, ok := ch.(*discord.Bot); ok {
			c.DiscordBot = b
			b.Buttons = func(subject string) []discord.Button { return buttons(c, subject) }
			b.Act = func(ctx context.Context, id, user string) (string, error) {
				out, err := act(c, ctx, id)
				if err == nil && c.Activity != nil {
					c.Activity("Discord user "+user, "discord", id, strings.TrimSuffix(out, "."))
				}
				return out, err
			}
		}
	}
}

// buttons are the actions under an alert about subject.
func buttons(c *Context, subject string) []discord.Button {
	kind, name, _ := strings.Cut(subject, ":")
	mute := discord.Button{ID: "mute:" + subject, Label: "Mute 1 hour", Style: 2}
	switch kind {
	case "check":
		out := []discord.Button{{ID: "again:" + name, Label: "Check again", Style: 1}}
		if c.Services != nil && c.Services.State(name).Spec.Repair != nil {
			out = append(out, discord.Button{ID: "repair:" + name, Label: "Run its repair", Style: 1})
		}
		return append(out, mute)
	case "unit":
		label, _ := incidents.Label(subject)
		return []discord.Button{{ID: "restart:" + name, Label: "Restart " + label, Style: 4}, mute}
	case "drive", "host":
		return []discord.Button{mute}
	case "cleanup":
		var out []discord.Button
		if c.MediaCleanup != nil {
			titles := map[string]string{}
			for _, it := range c.MediaCleanup.View().Items {
				titles[it.Key] = it.Title
			}
			for _, key := range strings.Split(name, ",") {
				if t, ok := titles[key]; ok {
					out = append(out, discord.Button{ID: "keep:" + key, Label: "Keep " + t, Style: 2})
				}
			}
		}
		return out
	}
	return nil
}

// act runs a button's action.
func act(c *Context, ctx context.Context, id string) (string, error) {
	verb, arg, _ := strings.Cut(id, ":")
	switch verb {
	case "again":
		if c.Services == nil || !c.Services.Has(arg) {
			return "", errors.New("there is no check called " + arg)
		}
		c.Services.RunSoon(arg)
		return fmt.Sprintf("Checking %s again now.", arg), nil
	case "repair":
		if c.Services == nil || !c.Services.Has(arg) || c.Repairs == nil {
			return "", errors.New("there is no check called " + arg)
		}
		st := c.Services.State(arg)
		if st.Spec.Repair == nil {
			return "", errors.New(arg + " has no repair")
		}
		detail := ""
		if st.LastResult != nil {
			detail = st.LastResult.Detail
		}
		rec := c.Repairs.Run(ctx, st.Spec, detail, float64(time.Now().Unix()), false)
		c.Services.RunSoon(arg)
		if !rec.OK {
			return "", errors.New(rec.Detail)
		}
		return "Repaired: " + rec.Detail + ". Checking it again now.", nil
	case "restart":
		if c.Units == nil {
			return "", errors.New("systemd is not available")
		}
		if err := c.Units.Control(ctx, "restart", arg); err != nil {
			return "", err
		}
		return "Restarted " + arg + ".", nil
	case "mute":
		if c.Notifications == nil {
			return "", errors.New("alerts are not kept here")
		}
		label, _ := incidents.Label(arg)
		if err := c.Notifications.Mute(arg, label, time.Hour); err != nil {
			return "", err
		}
		return "Muted " + label + " for an hour.", nil
	case "keep":
		if c.MediaCleanup == nil {
			return "", errors.New("the media cleanup is off")
		}
		if err := c.MediaCleanup.Keep(arg, true); err != nil {
			return "", err
		}
		return "Kept. The cleanup will leave it alone.", nil
	}
	return "", errors.New("Nightjar does not know that button")
}
