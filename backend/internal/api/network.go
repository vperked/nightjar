package api

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"nightjar/internal/ddos"
	"nightjar/internal/monitor"
	"nightjar/internal/netload"
)

// The live network meter.

// network serves the live meter, which samples every second while this is being asked for.
// ?since=<ts> returns only newer samples. qBittorrent's own speeds replace its TCP-only share: most
// torrent traffic is uTP over UDP, which has no per-connection counters.
func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	m := s.ctx.Network
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	m.Watch()
	since, _ := strconv.ParseFloat(r.URL.Query().Get("since"), 64)
	v := m.View(since)
	if board := s.ctx.QBittorrentBoard; board != nil {
		if qb := board.View(); qb.Connected && qb.Transfer != nil {
			v.Processes = withQBittorrent(v.Processes, qb.Transfer.DLSpeed, qb.Transfer.UPSpeed)
		}
	}
	body := map[string]any{"enabled": true, "network": v}
	if since == 0 {
		// The loop's half hour of samples (all interfaces), for the longer view.
		history := s.ctx.History()
		if history == nil {
			history = []monitor.Sample{}
		}
		body["history"] = history
	}
	writeJSON(w, http.StatusOK, body)
}

func withQBittorrent(list []netload.Talker, down, up int64) []netload.Talker {
	row := netload.Talker{Name: "qBittorrent", RX: float64(down), TX: float64(up), Source: "qbittorrent"}
	out := list[:0:0]
	for _, t := range list {
		if strings.Contains(strings.ToLower(t.Name), "qbittorrent") {
			row.Connections, row.PIDs, row.Unit = row.Connections+t.Connections, append(row.PIDs, t.PIDs...), t.Unit
			continue
		}
		out = append(out, t)
	}
	out = append(out, row)
	slices.SortStableFunc(out, func(a, b netload.Talker) int { return cmp.Compare(b.RX+b.TX, a.RX+a.TX) })
	return out
}

// attacks is the attack watcher's view: calm or under attack, how, from where, and the attacks seen.
func (s *Server) attacks(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Attacks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "attacks": s.ctx.Attacks.View()})
}

// setDefense switches defending against attacks on or off: {"on": true}.
func (s *Server) setDefense(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.On == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"on": true} or {"on": false}`))
		return
	}
	if s.ctx.Attacks == nil || s.ctx.Attacks.Defender == nil || s.cfg.Store == nil {
		writeJSON(w, http.StatusConflict, errBody("This dashboard does not watch for attacks"))
		return
	}
	value := "on"
	if !*body.On {
		value = "off"
	}
	if err := s.cfg.Store.SetMeta(ddos.DefendKey, value); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	// The next sample puts its rules in place or takes them away.
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "attacks": s.ctx.Attacks.View()})
}

// unblock lifts the defender's block on an address, and leaves it alone for an hour.
func (s *Server) unblock(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Attacks == nil || s.ctx.Attacks.Defender == nil {
		writeJSON(w, http.StatusConflict, errBody("This dashboard does not watch for attacks"))
		return
	}
	if !s.ctx.Attacks.Defender.Unblock(r.Context(), r.PathValue("address")) {
		writeJSON(w, http.StatusNotFound, errBody("That address is not blocked"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "attacks": s.ctx.Attacks.View()})
}
