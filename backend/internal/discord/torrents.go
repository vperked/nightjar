package discord

import (
	"fmt"
	"strings"

	"servermonitor/internal/format"
)

// TorrentInfo is the subset of a torrent the qBittorrent embeds show.
type TorrentInfo struct {
	Name, State, SavePath  string
	Size, Uploaded         int64
	ProgressPercent, Ratio float64
	DLSpeed, UPSpeed       int64
	Seeds, Peers           int
	ETA                    int64
}

const qbFooter = "qBittorrent"

func savePath(p string) string {
	if p == "" {
		return "N/A"
	}
	return p
}

func DownloadComplete(s Sender, t TorrentInfo) bool {
	fields := []Field{f("Torrent Name", t.Name, false), f("Size", format.Bytes(float64(t.Size)), true), f("Save Path", savePath(t.SavePath), false)}
	if t.Ratio > 0 {
		fields = append(fields, f("Ratio", fmt.Sprintf("%.2f", t.Ratio), true))
	}
	if t.Uploaded > 0 {
		fields = append(fields, f("Uploaded", format.Bytes(float64(t.Uploaded)), true))
	}
	return s.SendEmbed("Download Complete", fmt.Sprintf("Torrent download has completed: **%s**", Truncate(t.Name, 100)), OK, fields, qbFooter)
}

func StalledTorrent(s Sender, t TorrentInfo, stalledSeconds float64, actions []string) bool {
	download := t.State == "stalleddl"
	kind := "Upload"
	if download {
		kind = "Download"
	}
	fields := []Field{f("Torrent Name", t.Name, false), f("State", t.State, true), f("Progress", fmt.Sprintf("%.1f%%", t.ProgressPercent), true), f("Size", format.Bytes(float64(t.Size)), true)}
	if download {
		fields = append(fields, f("Download Speed", format.BytesPerSecond(float64(t.DLSpeed)), true))
		if t.ETA >= 0 && t.ETA < 8640000 {
			eta := fmt.Sprintf("%dm", t.ETA/60)
			if t.ETA >= 3600 {
				eta = fmt.Sprintf("%dh %dm", t.ETA/3600, t.ETA%3600/60)
			}
			fields = append(fields, f("ETA", eta, true))
		}
	} else {
		fields = append(fields, f("Upload Speed", format.BytesPerSecond(float64(t.UPSpeed)), true))
	}
	fields = append(fields, f("Seeds", fmt.Sprint(t.Seeds), true), f("Peers", fmt.Sprint(t.Peers), true))
	if stalledSeconds > 0 {
		h, m := int(stalledSeconds/3600), int(stalledSeconds)%3600/60
		d := fmt.Sprintf("%dm", m)
		if h > 0 {
			d = fmt.Sprintf("%dh %dm", h, m)
		}
		fields = append(fields, f("Stalled Duration", d, true))
	}
	desc := fmt.Sprintf("Torrent **%s** has stalled and is not downloading", Truncate(t.Name, 100))
	if len(actions) > 0 {
		fields = append(fields, f("Action Taken", strings.Join(actions, ", "), false))
		desc = fmt.Sprintf("Torrent **%s** has stalled; attempted recovery: %s", Truncate(t.Name, 100), strings.Join(actions, ", "))
	}
	fields = append(fields, f("Save Path", savePath(t.SavePath), false))
	return s.SendEmbed(fmt.Sprintf("Torrent Stalled (%s)", kind), desc, Warning, fields, qbFooter)
}

func RatioLimit(s Sender, t TorrentInfo, limit float64, actions []string) bool {
	fields := []Field{
		f("Torrent Name", t.Name, false), f("State", t.State, true), f("Progress", fmt.Sprintf("%.1f%%", t.ProgressPercent), true),
		f("Size", format.Bytes(float64(t.Size)), true), f("Ratio", fmt.Sprintf("%.2f", t.Ratio), true),
		f("Ratio Limit", fmt.Sprintf("%.2f", limit), true), f("Uploaded", format.Bytes(float64(t.Uploaded)), true),
		f("Upload Speed", format.BytesPerSecond(float64(t.UPSpeed)), true), f("Seeds / Peers", fmt.Sprintf("%d / %d", t.Seeds, t.Peers), true),
	}
	desc := fmt.Sprintf("Torrent **%s** reached seeding ratio %.2f (limit %.2f)", Truncate(t.Name, 100), t.Ratio, limit)
	if len(actions) > 0 {
		fields = append(fields, f("Action Taken", strings.Join(actions, ", "), false))
		desc = fmt.Sprintf("Torrent **%s** reached ratio %.2f; attempted: %s", Truncate(t.Name, 100), t.Ratio, strings.Join(actions, ", "))
	}
	fields = append(fields, f("Save Path", savePath(t.SavePath), false))
	return s.SendEmbed("Seeding Ratio Limit Reached", desc, Info, fields, qbFooter)
}

func InactiveTorrent(s Sender, t TorrentInfo, actions []string) bool {
	fields := []Field{
		f("Torrent Name", t.Name, false), f("State", t.State, true), f("Progress", fmt.Sprintf("%.1f%%", t.ProgressPercent), true),
		f("Size", format.Bytes(float64(t.Size)), true), f("Ratio", fmt.Sprintf("%.2f", t.Ratio), true),
	}
	desc := fmt.Sprintf("Torrent **%s** is not downloading or seeding (state: %s)", Truncate(t.Name, 100), t.State)
	if len(actions) > 0 {
		fields = append(fields, f("Action Taken", strings.Join(actions, ", "), false))
		desc = fmt.Sprintf("Torrent **%s** is not downloading or seeding; attempted: %s", Truncate(t.Name, 100), strings.Join(actions, ", "))
	}
	fields = append(fields, f("Save Path", savePath(t.SavePath), false))
	return s.SendEmbed("Inactive Torrent Removed", desc, Info, fields, qbFooter)
}

func Seeding(s Sender, t TorrentInfo) bool {
	fields := []Field{
		f("Torrent Name", t.Name, false), f("Size", format.Bytes(float64(t.Size)), true),
		f("Upload Speed", format.BytesPerSecond(float64(t.UPSpeed)), true), f("Ratio", fmt.Sprintf("%.2f", t.Ratio), true),
		f("Seeds / Peers", fmt.Sprintf("%d / %d", t.Seeds, t.Peers), true), f("Save Path", savePath(t.SavePath), false),
	}
	return s.SendEmbed("Torrent Seeding", fmt.Sprintf("Torrent is now seeding: **%s**", Truncate(t.Name, 100)), Info, fields, qbFooter)
}

// TorrentList posts the active downloads and seeds at startup; it skips sending when nothing is moving.
func TorrentList(s Sender, downloading, seeding []TorrentInfo) bool {
	var dl, up []TorrentInfo
	for _, t := range downloading {
		if t.DLSpeed > 0 {
			dl = append(dl, t)
		}
	}
	for _, t := range seeding {
		if t.UPSpeed > 0 {
			up = append(up, t)
		}
	}
	if len(dl) == 0 && len(up) == 0 {
		return true
	}
	short := func(name string) string {
		if len([]rune(name)) > 60 {
			return Truncate(name, 57) + "..."
		}
		return name
	}
	section := func(list []TorrentInfo, line func(TorrentInfo) string) string {
		var lines []string
		for i, t := range list {
			if i >= 10 {
				lines = append(lines, fmt.Sprintf("\n*+%d more*", len(list)-10))
				break
			}
			lines = append(lines, line(t))
		}
		return strings.Join(lines, "\n\n")
	}
	var fields []Field
	if len(dl) > 0 {
		fields = append(fields, f(fmt.Sprintf("Downloading (%d)", len(dl)), section(dl, func(t TorrentInfo) string {
			return fmt.Sprintf("`%s`\n%.1f%% • %s", short(t.Name), t.ProgressPercent, format.BytesPerSecond(float64(t.DLSpeed)))
		}), false))
	}
	if len(up) > 0 {
		fields = append(fields, f(fmt.Sprintf("Seeding (%d)", len(up)), section(up, func(t TorrentInfo) string {
			return fmt.Sprintf("`%s`\nRatio: %.2f • %s", short(t.Name), t.Ratio, format.BytesPerSecond(float64(t.UPSpeed)))
		}), false))
	}
	return s.SendEmbed("qBittorrent Status", fmt.Sprintf("%d downloading • %d seeding", len(dl), len(up)), Info, fields, qbFooter)
}
