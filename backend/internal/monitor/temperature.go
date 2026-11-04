package monitor

import (
	"fmt"
	"slices"
	"strings"

	"nightjar/internal/collect"
	"nightjar/internal/config"
)

var downloadKeywords = []string{
	"qbittorrent", "transmission", "deluge", "rtorrent", "aria2", "wget", "curl", "sabnzbd", "nzbget",
	"youtube-dl", "yt-dlp", "plex", "jellyfin", "emby", "radarr", "sonarr", "lidarr", "jackett", "prowlarr",
	"download", "torrent",
}

// TemperatureInputs is what AnalyzeTemperatureReason looks at. I/O figures are bytes per second.
type TemperatureInputs struct {
	CPUPercent   float64
	Freq         collect.CPUFreq
	PerCore      []float64
	Top          []collect.Process
	Load         []float64
	Fans         []collect.Fan
	DiskReadBps  float64
	DiskWriteBps float64
	NetRecvBps   float64
	NetSentBps   float64
	GPUTemp      float64
}

// AnalyzeTemperatureReason guesses why the CPU is hot, returning up to four reasons joined by " | ".
func AnalyzeTemperatureReason(in TemperatureInputs) string {
	reasons := cpuReasons(in)
	downloads, procReasons := processReasons(in.Top)
	reasons = append(reasons, procReasons...)
	reasons = append(reasons, loadReasons(in)...)
	reasons = append(reasons, fanReasons(in.Fans)...)
	reasons = append(reasons, ioReasons(in, downloads > 0)...)
	if in.GPUTemp > 70 {
		reasons = append(reasons, fmt.Sprintf("GPU also hot (%.1f°C) - possible ambient temperature or case airflow issue", in.GPUTemp))
	}
	if len(reasons) == 0 {
		switch {
		case in.CPUPercent > 30:
			reasons = append(reasons, fmt.Sprintf("Sustained CPU activity (%.1f%%) without specific high-load process", in.CPUPercent))
		case in.CPUPercent > 10:
			reasons = append(reasons, fmt.Sprintf("Low-moderate CPU usage (%.1f%%) with high temperature - check cooling system", in.CPUPercent))
		default:
			reasons = append(reasons, fmt.Sprintf("Very low CPU usage (%.1f%%) with high temperature - likely cooling system failure or ambient temperature issue", in.CPUPercent))
		}
	}
	return strings.Join(reasons[:min(4, len(reasons))], " | ")
}

func cpuReasons(in TemperatureInputs) []string {
	var out []string
	switch {
	case in.CPUPercent >= 80:
		out = append(out, fmt.Sprintf("Very high CPU usage (%.1f%%)", in.CPUPercent))
	case in.CPUPercent >= 60:
		out = append(out, fmt.Sprintf("High CPU usage (%.1f%%)", in.CPUPercent))
	case in.CPUPercent >= 40:
		out = append(out, fmt.Sprintf("Moderate CPU usage (%.1f%%)", in.CPUPercent))
	}
	if in.Freq.Max > 0 {
		switch pct := in.Freq.Current / in.Freq.Max * 100; {
		case pct >= 95:
			out = append(out, fmt.Sprintf("CPU running at maximum frequency (%.0f MHz)", in.Freq.Current))
		case pct >= 85:
			out = append(out, fmt.Sprintf("CPU running at high frequency (%.0f%% of max)", pct))
		}
	}
	if len(in.PerCore) > 0 && slices.Max(in.PerCore) >= 90 {
		high := 0
		for _, c := range in.PerCore {
			if c >= 80 {
				high++
			}
		}
		if high == 1 {
			out = append(out, "Single core under heavy load (single-threaded workload)")
		} else if high <= len(in.PerCore)/2 {
			out = append(out, fmt.Sprintf("%d cores under heavy load", high))
		}
	}
	return out
}

// processReasons names download/media processes and other busy ones among the top CPU users.
func processReasons(top []collect.Process) (downloads int, out []string) {
	var media, busy []string
	for i, p := range top {
		haystack := strings.ToLower(p.Name + " " + strings.Join(p.Cmdline, " "))
		if slices.ContainsFunc(downloadKeywords, func(kw string) bool { return strings.Contains(haystack, kw) }) {
			media = append(media, p.Name)
		} else if i < 5 && len(busy) < 3 && p.CPUPercent > 5 {
			busy = append(busy, fmt.Sprintf("%s (%.1f%%)", p.Name, p.CPUPercent))
		}
	}
	if len(media) > 0 {
		out = append(out, "Download/media activity: "+strings.Join(media[:min(2, len(media))], ", "))
	}
	switch len(busy) {
	case 0:
	case 1:
		out = append(out, "Process: "+busy[0])
	default:
		out = append(out, "Top processes: "+strings.Join(busy, ", "))
	}
	return len(media), out
}

func loadReasons(in TemperatureInputs) []string {
	if len(in.Load) == 0 {
		return nil
	}
	cores := max(1, len(in.PerCore))
	switch load := in.Load[0]; {
	case load > float64(cores)*1.5:
		return []string{fmt.Sprintf("High system load (%.1f vs %d cores)", load, cores)}
	case load > float64(cores):
		return []string{fmt.Sprintf("Elevated system load (%.1f vs %d cores)", load, cores)}
	}
	return nil
}

// fanReasons flags missing or slow fans. A nil slice means fans were not read at all.
func fanReasons(fans []collect.Fan) []string {
	if fans == nil {
		return nil
	}
	var speeds []float64
	for _, fan := range fans {
		if fan.RPM > 0 {
			speeds = append(speeds, float64(fan.RPM))
		}
	}
	if len(speeds) == 0 {
		return []string{"No fan speed data available - cannot verify cooling"}
	}
	sum := 0.0
	for _, v := range speeds {
		sum += v
	}
	if lo := slices.Min(speeds); lo < 500 {
		return []string{fmt.Sprintf("Very low fan speeds (min: %.0f RPM) - cooling issue likely", lo)}
	}
	if avg := sum / float64(len(speeds)); avg < 1000 {
		return []string{fmt.Sprintf("Low fan speeds (avg: %.0f RPM) - possible cooling issue", avg)}
	}
	return nil
}

func ioReasons(in TemperatureInputs, downloading bool) []string {
	const mb = float64(config.BytesPerMB)
	var out []string
	switch r, w := in.DiskReadBps, in.DiskWriteBps; {
	case w > 100*mb && downloading:
		out = append(out, fmt.Sprintf("Writing downloaded data to disk (%.0f MB/s)", w/mb))
	case w > 100*mb:
		out = append(out, fmt.Sprintf("High disk write activity (%.0f MB/s)", w/mb))
	case r > 200*mb:
		out = append(out, fmt.Sprintf("High disk read activity (%.0f MB/s)", r/mb))
	case r+w > 250*mb:
		out = append(out, fmt.Sprintf("High disk I/O activity (%.0f MB/s)", (r+w)/mb))
	}
	switch recv, sent := in.NetRecvBps, in.NetSentBps; {
	case recv > 10*mb && downloading:
		out = append(out, fmt.Sprintf("Active download in progress (%.1f MB/s)", recv/mb))
	case recv > 10*mb:
		out = append(out, fmt.Sprintf("High network download activity (%.1f MB/s)", recv/mb))
	case sent > 10*mb:
		out = append(out, fmt.Sprintf("High network upload activity (%.1f MB/s)", sent/mb))
	}
	return out
}
