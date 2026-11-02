package api

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"servermonitor/internal/monitor"
	"servermonitor/internal/netload"
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
