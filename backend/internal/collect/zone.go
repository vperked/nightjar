package collect

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// TimeZone is the machine's time zone as an IANA name where it can be found, else as its offset
// from UTC, and that offset at now in seconds east of UTC. Schedules (update windows, maintenance,
// quiet hours) run on this clock, so pages name it.
func TimeZone(now time.Time) (string, int) {
	_, offset := now.Zone()
	name := time.Local.String()
	if name == "Local" {
		// Go calls the zone it read from /etc/localtime "Local"; that file is usually a link
		// into the zoneinfo tree, whose path names the zone.
		if p, err := os.Readlink("/etc/localtime"); err == nil {
			if _, zone, ok := strings.Cut(p, "zoneinfo/"); ok && zone != "" {
				name = zone
			}
		}
	}
	if name == "Local" || name == "" {
		name = "UTC"
		if offset != 0 {
			sign, o := '+', offset
			if o < 0 {
				sign, o = '-', -o
			}
			name = fmt.Sprintf("UTC%c%02d:%02d", sign, o/3600, o%3600/60)
		}
	}
	return name, offset
}
