package discord

import (
	"fmt"
	"strings"

	"nightjar/internal/format"
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

// The torrent's name is the title, cut to fit; what happened to it leads the title.
func named(what string, t TorrentInfo) string { return what + ": " + Truncate(t.Name, 200) }

func size(t TorrentInfo) Field { return f("Size", format.Bytes(float64(t.Size)), true) }

func ratio(t TorrentInfo) Field { return f("Ratio", fmt.Sprintf("%.2f", t.Ratio), true) }

func peers(t TorrentInfo) Field {
	return f("Seeds · peers", fmt.Sprintf("%d · %d", t.Seeds, t.Peers), true)
}

func saved(t TorrentInfo) Field { return f("Saved in", "`"+t.SavePath+"`", false) }

// done is each action qBittorrent took, in words.
var done = map[string]string{"reannounce": "asked its trackers again", "resume": "resumed it", "delete": "removed it", "delete_files": "removed it and its files"}

// did says what Nightjar did about a torrent, as a sentence.
func did(actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	words := make([]string, len(actions))
	for i, a := range actions {
		words[i] = done[a]
		if words[i] == "" {
			words[i] = a
		}
	}
	return " Nightjar " + strings.Join(words, ", then ") + "."
}

func DownloadComplete(s Sender, t TorrentInfo) bool {
	fields := []Field{size(t)}
	if t.Ratio > 0 {
		fields = append(fields, ratio(t))
	}
	if t.SavePath != "" {
		fields = append(fields, saved(t))
	}
	return s.SendEmbed(named("Downloaded", t), "", OK, fields, qbFooter)
}

func StalledTorrent(s Sender, t TorrentInfo, stalledSeconds float64, actions []string) bool {
	download := t.State == "stalleddl"
	what := "Not seeding for"
	if download {
		what = "No progress for"
	}
	desc := "It stopped moving."
	if stalledSeconds > 0 {
		desc = fmt.Sprintf("%s %s.", what, Span(stalledSeconds))
	}
	desc += did(actions)
	fields := []Field{f("Progress", fmt.Sprintf("%.0f%% of %s", t.ProgressPercent, format.Bytes(float64(t.Size))), true), peers(t)}
	if download && t.ETA >= 0 && t.ETA < 8640000 {
		fields = append(fields, f("Time left", Span(float64(t.ETA)), true))
	}
	return s.SendEmbed(named("Stalled", t), desc, Warning, fields, qbFooter)
}

func RatioLimit(s Sender, t TorrentInfo, limit float64, actions []string) bool {
	desc := fmt.Sprintf("It reached ratio %.2f, past the %.2f limit.%s", t.Ratio, limit, did(actions))
	fields := []Field{size(t), f("Uploaded", format.Bytes(float64(t.Uploaded)), true), peers(t)}
	return s.SendEmbed(named("Seeded enough", t), desc, Info, fields, qbFooter)
}

func InactiveTorrent(s Sender, t TorrentInfo, actions []string) bool {
	desc := fmt.Sprintf("It was neither downloading nor seeding (%s).%s", t.State, did(actions))
	fields := []Field{f("Progress", fmt.Sprintf("%.0f%%", t.ProgressPercent), true), size(t), ratio(t)}
	return s.SendEmbed(named("Removed", t), desc, Info, fields, qbFooter)
}

func Seeding(s Sender, t TorrentInfo) bool {
	fields := []Field{size(t), f("Upload", format.BytesPerSecond(float64(t.UPSpeed)), true), ratio(t)}
	return s.SendEmbed(named("Seeding", t), "", Info, fields, qbFooter)
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
		if len([]rune(name)) > 48 {
			return Truncate(name, 47) + "…"
		}
		return name
	}
	section := func(list []TorrentInfo, line func(TorrentInfo) string) string {
		var lines []string
		for i, t := range list {
			if i >= 8 {
				lines = append(lines, fmt.Sprintf("and %d more", len(list)-8))
				break
			}
			lines = append(lines, line(t))
		}
		return cap1024(List(lines))
	}
	var fields []Field
	if len(dl) > 0 {
		fields = append(fields, f(fmt.Sprintf("Downloading · %d", len(dl)), section(dl, func(t TorrentInfo) string {
			return fmt.Sprintf("%s · %.0f%% · %s", short(t.Name), t.ProgressPercent, format.BytesPerSecond(float64(t.DLSpeed)))
		}), false))
	}
	if len(up) > 0 {
		fields = append(fields, f(fmt.Sprintf("Seeding · %d", len(up)), section(up, func(t TorrentInfo) string {
			return fmt.Sprintf("%s · ratio %.2f · %s", short(t.Name), t.Ratio, format.BytesPerSecond(float64(t.UPSpeed)))
		}), false))
	}
	title := fmt.Sprintf("%d downloading, %d seeding", len(dl), len(up))
	return s.SendEmbed(title, "", Info, fields, qbFooter)
}
