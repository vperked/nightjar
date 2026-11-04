package discord

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Alerts go to Discord as Components V2 messages: one container in the severity's colour holding
// the title as a heading, what happened, the details and, in small print, where and when it came
// from, with a button back to the dashboard. Webhooks need with_components=true to take them.

// FlagComponentsV2 marks a message laid out with components instead of content and embeds.
const FlagComponentsV2 = 1 << 15

// Discord's limits: 4000 characters of text across a message's text displays.
const maxComponentText = 4000

// Component is one Discord message component.
type Component map[string]any

func textDisplay(s string) Component { return Component{"type": 10, "content": s} }

func separator() Component { return Component{"type": 14, "divider": true, "spacing": 1} }

func linkButton(label, link string) Component {
	return Component{"type": 1, "components": []Component{{"type": 2, "style": 5, "label": label, "url": link}}}
}

// details is the fields as text: short facts one to a line ("**Response:** 4 ms"), and longer
// ones, or those with several lines, under their name.
func details(fields []Field) string {
	var blocks, facts []string
	flush := func() {
		if len(facts) > 0 {
			blocks = append(blocks, strings.Join(facts, "\n"))
			facts = nil
		}
	}
	for _, f := range fields {
		if f.Inline && !strings.Contains(f.Value, "\n") {
			facts = append(facts, "**"+f.Name+":** "+f.Value)
			continue
		}
		flush()
		blocks = append(blocks, "**"+f.Name+"**\n"+f.Value)
	}
	flush()
	return strings.Join(blocks, "\n\n")
}

// Message is an alert as a Components V2 message.
func Message(title, description string, sev Severity, fields []Field, footer string, origin Origin, at time.Time) map[string]any {
	head := "### " + Truncate(title, 250)
	if description != "" {
		head += "\n" + description
	}
	body := details(tidy(fields))
	small := fmt.Sprintf("-# %s · <t:%d:R>", origin.Footer(footer), at.Unix())
	// The text shares one limit: the details give way first, then the description.
	room := maxComponentText - len([]rune(small)) - 10
	if len([]rune(head))+len([]rune(body)) > room {
		body = Truncate(body, max(0, room-len([]rune(head))-1)) + "…"
		head = Truncate(head, room)
	}
	parts := []Component{textDisplay(head)}
	if strings.TrimSpace(body) != "" {
		parts = append(parts, separator(), textDisplay(body))
	}
	parts = append(parts, textDisplay(small))
	if u, err := url.Parse(origin.DashboardURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		parts = append(parts, linkButton("Open Nightjar", origin.DashboardURL))
	}
	return map[string]any{
		"flags":            FlagComponentsV2,
		"components":       []Component{{"type": 17, "accent_color": sev.Color(), "components": parts}},
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
}

// withComponents is a webhook URL that takes components, keeping any query it has (thread_id).
func withComponents(hook string) string {
	u, err := url.Parse(hook)
	if err != nil {
		return hook
	}
	q := u.Query()
	q.Set("with_components", "true")
	u.RawQuery = q.Encode()
	return u.String()
}
