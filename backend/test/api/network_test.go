package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"nightjar/internal/netload"
	"nightjar/internal/qbittorrent"
)

func TestNetworkRoute(t *testing.T) {
	m := newManaged(t)
	n := netload.New()
	n.ReadDev = func() (map[string]netload.Counters, error) {
		return map[string]netload.Counters{"eth0": {RXBytes: 100}}, nil
	}
	n.ReadSockets = func(context.Context) ([]netload.Socket, error) {
		return []netload.Socket{{Local: "10.0.0.5:6881", Peer: "198.51.100.4:51413", Process: "qbittorrent-nox", PID: 7}}, nil
	}
	n.Identify = func(pid int, comm string) (string, string) { return comm, "qbittorrent@media" }
	n.Describe = func(string) (bool, bool, int) { return true, false, 1000 }
	n.DefaultIf = func() string { return "eth0" }
	n.Tick = time.Hour // the test samples by hand
	m.ctx.Network = n
	board := &qbittorrent.Board{}
	board.Publish(1, "http://localhost:8080", "v5", "", nil, &qbittorrent.Transfer{DLSpeed: 5_000_000, UPSpeed: 700_000})
	m.ctx.QBittorrentBoard = board

	code, body := m.send(t, http.MethodGet, "/api/network", "")
	if code != http.StatusOK || body["enabled"] != true || body["history"] == nil {
		t.Fatalf("network: %d %v", code, body)
	}
	n.Sample(time.Now(), true)
	_, body = m.send(t, http.MethodGet, "/api/network?since=1", "")
	if _, has := body["history"]; has {
		t.Fatal("history comes with the first answer only")
	}
	procs := body["network"].(map[string]any)["processes"].([]any)
	qb := procs[0].(map[string]any)
	if len(procs) != 1 || qb["name"] != "qBittorrent" || qb["rx_bps"] != 5_000_000.0 || qb["source"] != "qbittorrent" || qb["connections"] != 1.0 || qb["unit"] != "qbittorrent@media" {
		t.Fatalf("qBittorrent's own speeds replace its TCP share: %v", procs)
	}
}
