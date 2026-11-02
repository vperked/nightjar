// Package format renders byte counts, rates and durations the way alerts and logs show them.
package format

import (
	"fmt"
	"strings"
)

const kb = 1024.0

// Bytes formats a byte count in binary units (1 GB = 1024³ bytes), the units the settings use.
func Bytes(n float64) string {
	if n == 0 {
		return "0 B"
	}
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB", "PB"} {
		if n < kb {
			return fmt.Sprintf("%.2f %s", n, unit)
		}
		n /= kb
	}
	return fmt.Sprintf("%.2f EB", n)
}

// BytesPerSecond formats a transfer rate in binary units.
func BytesPerSecond(n float64) string {
	if n == 0 {
		return "0 B/s"
	}
	for _, unit := range []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"} {
		if n < kb {
			return fmt.Sprintf("%.2f %s", n, unit)
		}
		n /= kb
	}
	return fmt.Sprintf("%.2f PB/s", n)
}

// Megabytes formats a value already in MB, switching to GB at 1024.
func Megabytes(v float64) string {
	if v < kb {
		return fmt.Sprintf("%.0f MB", v)
	}
	return fmt.Sprintf("%.2f GB", v/kb)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Duration renders seconds as "N days, N hours, N minutes".
func Duration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1f seconds", seconds)
	}
	minutes := int(seconds / 60)
	secs := seconds - float64(minutes*60)
	if minutes < 60 {
		if secs < 1 {
			return plural(minutes, "minute")
		}
		suffix := "s"
		if secs == 1 {
			suffix = ""
		}
		return fmt.Sprintf("%s, %.1f second%s", plural(minutes, "minute"), secs, suffix)
	}
	hours, mins := minutes/60, minutes%60
	if hours < 24 {
		parts := []string{plural(hours, "hour")}
		if mins > 0 {
			parts = append(parts, plural(mins, "minute"))
		}
		if secs >= 1 {
			parts = append(parts, plural(int(secs), "second"))
		}
		return strings.Join(parts, ", ")
	}
	days, hrs := hours/24, hours%24
	parts := []string{plural(days, "day")}
	if hrs > 0 {
		parts = append(parts, plural(hrs, "hour"))
	}
	if mins > 0 {
		parts = append(parts, plural(mins, "minute"))
	}
	if secs >= 1 && len(parts) < 3 {
		parts = append(parts, plural(int(secs), "second"))
	}
	return strings.Join(parts, ", ")
}
