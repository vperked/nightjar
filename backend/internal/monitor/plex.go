package monitor

import (
	"fmt"
	"strings"

	"servermonitor/internal/discord"
	"servermonitor/internal/format"
	"servermonitor/internal/plex"
)

// plexWatcher polls Plex on its own interval and alerts on outages, transcode load and new streams.
type plexWatcher struct {
	handler
}

func (h *plexWatcher) check(s *Snapshot) {
	m := h.ctx.Plex
	if m == nil {
		return
	}
	hook := h.ctx.PlexWebhook
	for _, e := range m.Poll(s.Now) {
		switch e.Kind {
		case plex.EventDown:
			h.raiseAlert("plex:down", s.Now, "Plex unreachable: "+e.Detail, hook, func() bool {
				return hook.SendEmbed("Plex Down", e.Detail, discord.Critical, []discord.Field{
					field("Server", m.URL(), false), field("Failed checks", fmt.Sprint(m.Failures()), true),
				}, "Plex")
			}, "plex_down", true)
		case plex.EventUp:
			h.log.Info(fmt.Sprintf("[RECOVERED] Plex reachable again after %s", format.Duration(e.Downtime)))
			if hook != nil {
				hook.SendEmbed("Plex Recovered", "Plex Media Server is reachable again", discord.OK, []discord.Field{
					field("Down for", format.Duration(e.Downtime), true),
				}, "Plex")
			}
		case plex.EventStreamStart:
			st := e.Stream
			h.log.Info(fmt.Sprintf("Stream started: %s - %s on %s (%s)", st.User, streamName(st), st.Player, decision(st)))
			if hook != nil && h.ctx.Cfg.PlexNotifyStreams {
				hook.SendEmbed("Now playing: "+streamName(st), "", discord.Info, []discord.Field{
					field("User", st.User, true), field("Player", strings.TrimSpace(st.Player+" · "+st.Product), true),
					field("Stream", decision(st), true), field("Quality", or(st.Quality, "n/a"), true),
					field("Bandwidth", fmt.Sprintf("%.1f Mbps", float64(st.BandwidthKbps)/1000), true),
				}, "Plex")
			}
		}
	}
	if f := m.Failures(); f > 0 && f <= m.Retries() {
		h.log.Info(fmt.Sprintf("Plex check failed (%s) [%d/%d]", m.Detail(), f, m.Retries()+1))
	}
	if limit := h.ctx.Cfg.PlexTranscodeAlert; limit > 0 {
		if n := m.Transcodes(); n >= limit {
			msg := fmt.Sprintf("Plex is transcoding %d stream(s) (alert at %d)", n, limit)
			h.raiseAlert("plex:transcode", s.Now, msg, hook, func() bool {
				return hook.SendEmbed("Plex Transcode Load", msg, discord.Warning, []discord.Field{
					field("Transcodes", fmt.Sprint(n), true), field("CPU", fmt.Sprintf("%.1f%%", s.CPUPercent), true),
				}, "Plex")
			}, "plex_transcode", true)
		}
	}
}

func streamName(s plex.Stream) string {
	if s.Kind == "episode" && s.Subtitle != "" {
		return s.Title + " " + strings.SplitN(s.Subtitle, " ", 2)[0]
	}
	return s.Title
}

func decision(s plex.Stream) string {
	if s.Decision == plex.Transcode && s.HWTranscode {
		return "transcode (hw)"
	}
	return s.Decision
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
