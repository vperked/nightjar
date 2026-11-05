package ddos

// Not every surge is an attack. A package install or an update downloads as fast as the line allows,
// and a torrent client sends and receives UDP all day; the detector asks what is running before it
// calls traffic an attack.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Busy is what is running that explains heavy traffic.
type Busy struct {
	// Installing names what is installing or updating ("apt-get", "Nightjar installing Sonarr").
	Installing string `json:"installing,omitempty"`
	// Torrents names a torrent client that is running; its UDP traffic is uTP, not a flood.
	Torrents string `json:"torrents,omitempty"`
}

// installers are programs whose running means packages are being downloaded and installed.
var installers = []string{"apt", "apt-get", "aptitude", "dpkg", "unattended-upgr", "yum", "dnf", "rpm", "zypper", "pacman", "apk", "flatpak", "pip", "pip3", "npm", "pnpm", "yarn", "cargo"}

// torrentClients are programs that move most of their traffic over UDP.
var torrentClients = []string{"qbittorrent-nox", "qbittorrent", "transmission-da", "deluged", "rtorrent", "aria2c"}

// ReadBusy says what is running, from /proc: the program names (comm, cut at 15 characters by the kernel) and, for
// snap and docker, whether they are fetching something.
func ReadBusy(root string) Busy {
	var b Busy
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		if d.Name()[0] < '0' || d.Name()[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, d.Name(), "comm"))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(raw))
		switch {
		case b.Installing == "" && slices.Contains(installers, comm):
			b.Installing = comm
		case b.Installing == "" && (comm == "snap" || comm == "docker"):
			args, _ := os.ReadFile(filepath.Join(root, d.Name(), "cmdline"))
			for _, a := range strings.Split(string(args), "\x00") {
				if a == "install" || a == "refresh" || a == "pull" {
					b.Installing = comm + " " + a
				}
			}
		case b.Torrents == "" && slices.Contains(torrentClients, comm):
			b.Torrents = comm
		}
	}
	return b
}
