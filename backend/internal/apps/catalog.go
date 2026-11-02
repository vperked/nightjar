// Package apps is the dashboard's app store: torrent and media apps installed from their official
// releases, each as its own systemd service, tracked so they can be removed cleanly. Apps another
// installer (swizzin, a package) already put on the machine show as installed and are left alone.
package apps

// App is one entry in the store: where to get it and how to run it.
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Homepage string `json:"homepage"`
	// Port is the default web port; 0 means the app has no web interface.
	Port int `json:"port"`
	// SignIn says how to get in the first time.
	SignIn string `json:"sign_in,omitempty"`
	// Color is the tile's colour in the store.
	Color string `json:"color"`

	source source
	// binary is the executable inside the download.
	binary string
	// exec, env, setup and after are templates: {{dir}} is where the app lives, {{data}} its
	// settings and database, {{port}} its web port, {{user}} the account it runs as.
	exec  string
	env   []string
	setup string // shell run before the first start, e.g. to write a config file
	after string // shell run once it answers, e.g. to print a first-time password
	// servarr apps need ICU; dotnet marks them.
	dotnet bool
	// elsewhere are signs another installer already put the app here: unit files and swizzin locks.
	units []string
	lock  string
}

// source is where a release comes from.
type source struct {
	github string            // owner/repo: the latest release's asset matching assets[arch]
	assets map[string]string // GOARCH → regular expression on the asset name
	urls   map[string]string // GOARCH → a URL that redirects to the latest release
	single bool              // the download is the executable itself, not an archive
}

// servarr is the Sonarr family's update service, which redirects to the newest release.
func servarr(app string) source {
	u := "https://" + app + ".servarr.com/v1/update/master/updatefile?os=linux&runtime=netcore&arch="
	return source{urls: map[string]string{"amd64": u + "x64", "arm64": u + "arm64"}}
}

// servarrConfig writes the port before the first start, which the Sonarr family reads and fills out.
const servarrConfig = `[ -f {{data}}/config.xml ] || printf '<Config>\n  <Port>{{port}}</Port>\n  <BindAddress>*</BindAddress>\n  <LaunchBrowser>False</LaunchBrowser>\n</Config>\n' > {{data}}/config.xml`

// Catalog is every app the store offers, in display order.
var Catalog = []App{
	{
		ID: "qbittorrent", Name: "qBittorrent", Category: "Download clients", Color: "#2f67ba", Port: 8086,
		Summary:  "The torrent client: fast, light, with a full web interface.",
		Homepage: "https://www.qbittorrent.org",
		SignIn:   "Username admin. A temporary password is printed at the end of the install log; set your own under Tools → Options → WebUI.",
		source: source{github: "userdocs/qbittorrent-nox-static", single: true,
			assets: map[string]string{"amd64": `^x86_64-qbittorrent-nox$`, "arm64": `^aarch64-qbittorrent-nox$`}},
		binary: "qbittorrent-nox",
		exec:   "{{dir}}/qbittorrent-nox --profile={{data}} --webui-port={{port}} --confirm-legal-notice",
		after:  `sleep 3; journalctl -u qbittorrent.service -n 80 --no-pager -o cat | grep -i "temporary password" | tail -n 1 || true`,
		units:  []string{"qbittorrent.service", "qbittorrent@.service", "qbittorrent-nox.service", "qbittorrent-nox@.service"},
		lock:   "qbittorrent",
	},
	{
		ID: "prowlarr", Name: "Prowlarr", Category: "Indexers", Color: "#e66000", Port: 9696,
		Summary:  "Manages your torrent trackers and indexers in one place and syncs them to Sonarr, Radarr and Lidarr.",
		Homepage: "https://prowlarr.com",
		SignIn:   "Choose a username and password on first visit.",
		source:   servarr("prowlarr"), binary: "Prowlarr", dotnet: true,
		exec:  "{{dir}}/Prowlarr -nobrowser -data={{data}}",
		setup: servarrConfig,
		units: []string{"prowlarr.service"}, lock: "prowlarr",
	},
	{
		ID: "jackett", Name: "Jackett", Category: "Indexers", Color: "#2e3440", Port: 9117,
		Summary:  "Turns hundreds of tracker sites into one search API, for apps that don't use Prowlarr.",
		Homepage: "https://github.com/Jackett/Jackett",
		SignIn:   "Open by default; set an admin password at the bottom of its page.",
		source: source{github: "Jackett/Jackett",
			assets: map[string]string{"amd64": `^Jackett\.Binaries\.LinuxAMDx64\.tar\.gz$`, "arm64": `^Jackett\.Binaries\.LinuxARM64\.tar\.gz$`}},
		binary: "jackett", dotnet: true,
		exec:  "{{dir}}/jackett --NoUpdates --Port {{port}} --DataFolder {{data}}",
		units: []string{"jackett.service", "jackett@.service"}, lock: "jackett",
	},
	{
		ID: "sonarr", Name: "Sonarr", Category: "Automation", Color: "#35c5f4", Port: 8989,
		Summary:  "Watches for new episodes of your shows, grabs them through your torrent client and files them.",
		Homepage: "https://sonarr.tv",
		SignIn:   "Choose a username and password on first visit.",
		source: source{urls: map[string]string{
			"amd64": "https://services.sonarr.tv/v1/download/main/latest?version=4&os=linux&arch=x64",
			"arm64": "https://services.sonarr.tv/v1/download/main/latest?version=4&os=linux&arch=arm64",
		}},
		binary: "Sonarr", dotnet: true,
		exec:  "{{dir}}/Sonarr -nobrowser -data={{data}}",
		setup: servarrConfig,
		units: []string{"sonarr.service", "sonarr@.service"}, lock: "sonarr",
	},
	{
		ID: "radarr", Name: "Radarr", Category: "Automation", Color: "#ffc230", Port: 7878,
		Summary:  "Sonarr for films: finds the releases you want, at the quality you want, and keeps them organised.",
		Homepage: "https://radarr.video",
		SignIn:   "Choose a username and password on first visit.",
		source:   servarr("radarr"), binary: "Radarr", dotnet: true,
		exec:  "{{dir}}/Radarr -nobrowser -data={{data}}",
		setup: servarrConfig,
		units: []string{"radarr.service", "radarr@.service"}, lock: "radarr",
	},
	{
		ID: "lidarr", Name: "Lidarr", Category: "Automation", Color: "#00a65b", Port: 8686,
		Summary:  "Sonarr for music: follows artists and fetches new albums as they come out.",
		Homepage: "https://lidarr.audio",
		SignIn:   "Choose a username and password on first visit.",
		source:   servarr("lidarr"), binary: "Lidarr", dotnet: true,
		exec:  "{{dir}}/Lidarr -nobrowser -data={{data}}",
		setup: servarrConfig,
		units: []string{"lidarr.service", "lidarr@.service"}, lock: "lidarr",
	},
	{
		ID: "autobrr", Name: "autobrr", Category: "Automation", Color: "#3d6bf5", Port: 7474,
		Summary:  "Catches new releases from tracker IRC announce channels within seconds and sends them to your client.",
		Homepage: "https://autobrr.com",
		SignIn:   "Create the admin account on first visit.",
		source: source{github: "autobrr/autobrr",
			assets: map[string]string{"amd64": `_linux_x86_64\.tar\.gz$`, "arm64": `_linux_arm64\.tar\.gz$`}},
		binary: "autobrr",
		exec:   "{{dir}}/autobrr --config={{data}}",
		setup:  `[ -f {{data}}/config.toml ] || printf 'host = "0.0.0.0"\nport = {{port}}\nlogLevel = "INFO"\ncheckForUpdates = false\nsessionSecret = "%s"\n' "$(head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9)" > {{data}}/config.toml`,
		units:  []string{"autobrr.service", "autobrr@.service"}, lock: "autobrr",
	},
	{
		ID: "unpackerr", Name: "Unpackerr", Category: "Automation", Color: "#7a5af8",
		Summary:  "Extracts downloads that arrive as RAR archives so Sonarr and Radarr can import them, then cleans up.",
		Homepage: "https://unpackerr.zip",
		SignIn:   "No web interface: add your Sonarr and Radarr URLs and API keys to its config file, then restart it.",
		source: source{github: "Unpackerr/unpackerr",
			assets: map[string]string{"amd64": `_linux_amd64\.tar\.gz$`, "arm64": `_linux_arm64\.tar\.gz$`}},
		binary: "unpackerr",
		exec:   "{{dir}}/unpackerr -c {{data}}/unpackerr.conf",
		setup:  `[ -f {{data}}/unpackerr.conf ] || cp {{dir}}/unpackerr.conf.example {{data}}/unpackerr.conf 2>/dev/null || touch {{data}}/unpackerr.conf`,
		units:  []string{"unpackerr.service"}, lock: "unpackerr",
	},
	{
		ID: "ntfy", Name: "ntfy", Category: "Notifications", Color: "#338574", Port: 2586,
		Summary:  "Your own push notification server: the monitor's alerts reach your phone without a third-party service.",
		Homepage: "https://ntfy.sh",
		SignIn:   "In the ntfy phone app, add this server's address, then subscribe to the monitor's topic.",
		source: source{github: "binwiederhier/ntfy",
			assets: map[string]string{"amd64": `_linux_amd64\.tar\.gz$`, "arm64": `_linux_arm64\.tar\.gz$`}},
		binary: "ntfy",
		exec:   "{{dir}}/ntfy serve --config {{data}}/server.yml",
		// upstream-base-url relays a wake-up through ntfy.sh, which iPhones need for instant delivery.
		setup: `[ -f {{data}}/server.yml ] || printf 'base-url: "http://%s:{{port}}"\nlisten-http: ":{{port}}"\ncache-file: "{{data}}/cache.db"\nattachment-cache-dir: "{{data}}/attachments"\nupstream-base-url: "https://ntfy.sh"\n' "$(hostname -I | awk '{print $1}')" > {{data}}/server.yml`,
		units: []string{"ntfy.service"},
	},
}

// Find returns the catalog entry with id.
func Find(id string) (App, bool) {
	for _, a := range Catalog {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}
