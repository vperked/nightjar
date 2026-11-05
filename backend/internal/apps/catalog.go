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
	exec    string
	env     []string
	prepare string // shell run as root before setup, e.g. to install a package the app needs
	setup   string // shell run as the app's user before the first start, e.g. to write a config file
	after   string // shell run once it answers, e.g. to print a first-time password
	// servarr apps need ICU; dotnet marks them.
	dotnet bool
	// caps are capabilities the service is given, such as CAP_NET_BIND_SERVICE to serve DNS on port 53.
	caps []string
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
	{
		ID: "navidrome", Name: "Navidrome", Category: "Media", Color: "#0d6efd", Port: 4533,
		Summary:  "Your own music streaming server: plays your music library in the browser and in Subsonic apps on your phone.",
		Homepage: "https://www.navidrome.org",
		SignIn:   "Create the admin account on first visit. Point MusicFolder in /var/lib/navidrome/navidrome.toml at your music, then restart it. Install ffmpeg for transcoding.",
		source: source{github: "navidrome/navidrome",
			assets: map[string]string{"amd64": `_linux_amd64\.tar\.gz$`, "arm64": `_linux_arm64\.tar\.gz$`}},
		binary: "navidrome",
		exec:   "{{dir}}/navidrome --configfile {{data}}/navidrome.toml --datafolder {{data}} --address 0.0.0.0 --port {{port}}",
		setup:  `[ -f {{data}}/navidrome.toml ] || { mkdir -p {{data}}/music; printf 'MusicFolder = "{{data}}/music"\n' > {{data}}/navidrome.toml; }`,
		units:  []string{"navidrome.service"},
	},
	{
		ID: "ombi", Name: "Ombi", Category: "Media", Color: "#e0752d", Port: 5000,
		Summary:  "Lets the people you share Plex with request shows and films; requests go to Sonarr and Radarr.",
		Homepage: "https://ombi.io",
		SignIn:   "Run the setup wizard on first visit: create the admin account, then connect Plex, Sonarr and Radarr.",
		source: source{github: "Ombi-app/Ombi",
			assets: map[string]string{"amd64": `^linux-x64\.tar\.gz$`, "arm64": `^linux-arm64\.tar\.gz$`}},
		binary: "Ombi", dotnet: true,
		exec:  "{{dir}}/Ombi --host http://*:{{port}} --storage {{data}}",
		units: []string{"ombi.service"}, lock: "ombi",
	},
	{
		ID: "syncthing", Name: "Syncthing", Category: "Files and sync", Color: "#0891d1", Port: 8384,
		Summary:  "Keeps folders in sync between this server, your computers and your phone, directly and privately.",
		Homepage: "https://syncthing.net",
		SignIn:   "Open it and set a username and password under Actions → Settings → GUI; it warns until you do.",
		source: source{github: "syncthing/syncthing",
			assets: map[string]string{"amd64": `^syncthing-linux-amd64-v[\d.]+\.tar\.gz$`, "arm64": `^syncthing-linux-arm64-v[\d.]+\.tar\.gz$`}},
		binary: "syncthing",
		// Nightjar keeps it up to date, so it does not replace itself.
		env:   []string{"STNOUPGRADE=1"},
		exec:  "{{dir}}/syncthing serve --no-browser --home={{data}} --gui-address=0.0.0.0:{{port}}",
		units: []string{"syncthing.service", "syncthing@.service"}, lock: "syncthing",
	},
	{
		ID: "filebrowser", Name: "File Browser", Category: "Files and sync", Color: "#40c4ff", Port: 8090,
		Summary:  "Browse, upload, download and share the server's files from any browser.",
		Homepage: "https://filebrowser.org",
		SignIn:   "Username admin. A generated password is printed at the end of the install log; change it under Settings.",
		source: source{github: "filebrowser/filebrowser",
			assets: map[string]string{"amd64": `^linux-amd64-filebrowser\.tar\.gz$`, "arm64": `^linux-arm64-filebrowser\.tar\.gz$`}},
		binary: "filebrowser",
		exec:   "{{dir}}/filebrowser -c {{data}}/filebrowser.json",
		// It shows the home folder of the account the apps run as, where their files are.
		setup: `[ -f {{data}}/filebrowser.json ] || printf '{"port": {{port}}, "address": "0.0.0.0", "database": "{{data}}/filebrowser.db", "root": "%s"}\n' "$(getent passwd {{user}} | cut -d: -f6)" > {{data}}/filebrowser.json`,
		after: `sleep 2; journalctl -u filebrowser.service -n 80 --no-pager -o cat | grep -i "password" | tail -n 1 || true`,
		units: []string{"filebrowser.service"}, lock: "filebrowser",
	},
	{
		ID: "adguardhome", Name: "AdGuard Home", Category: "Network", Color: "#68bc71", Port: 3080,
		Summary:  "Blocks ads and trackers for every device on your network, as its DNS server.",
		Homepage: "https://adguard.com/adguard-home/overview.html",
		SignIn:   "Run the setup wizard on first visit. On Ubuntu, systemd-resolved already uses port 53 on 127.0.0.53: listen for DNS on this server's network address, not on all interfaces. Then point your router's DNS at this server.",
		source: source{github: "AdguardTeam/AdGuardHome",
			assets: map[string]string{"amd64": `^AdGuardHome_linux_amd64\.tar\.gz$`, "arm64": `^AdGuardHome_linux_arm64\.tar\.gz$`}},
		binary: "AdGuardHome",
		exec:   "{{dir}}/AdGuardHome --work-dir {{data}} --config {{data}}/AdGuardHome.yaml --web-addr 0.0.0.0:{{port}} --no-check-update",
		// DNS is port 53, which only root may open without this.
		caps:  []string{"CAP_NET_BIND_SERVICE"},
		units: []string{"AdGuardHome.service", "adguardhome.service"},
	},
	{
		ID: "gitea", Name: "Gitea", Category: "Self-hosting", Color: "#609926", Port: 3000,
		Summary:  "Your own GitHub: git repositories, issues, pull requests and a web editor, light enough for a Raspberry Pi.",
		Homepage: "https://about.gitea.com",
		SignIn:   "Finish the install page on first visit (SQLite is fine for a home server); the first account made there is the admin.",
		source: source{github: "go-gitea/gitea", single: true,
			assets: map[string]string{"amd64": `^gitea-[\d.]+-linux-amd64$`, "arm64": `^gitea-[\d.]+-linux-arm64$`}},
		binary:  "gitea",
		env:     []string{"GITEA_WORK_DIR={{data}}", "HOME={{data}}", "USER={{user}}"},
		exec:    "{{dir}}/gitea web --config {{data}}/custom/conf/app.ini --port {{port}}",
		prepare: `command -v git >/dev/null || { apt-get update -qq; apt-get install -y -qq git; }`,
		setup:   `mkdir -p {{data}}/custom/conf`,
		units:   []string{"gitea.service"},
	},
	{
		ID: "memos", Name: "Memos", Category: "Self-hosting", Color: "#4b5563", Port: 5230,
		Summary:  "Quick notes, like a private Twitter for your thoughts, links and to-dos, kept on your server.",
		Homepage: "https://usememos.com",
		SignIn:   "The first account you create is the admin.",
		source: source{github: "usememos/memos",
			assets: map[string]string{"amd64": `_linux_amd64\.tar\.gz$`, "arm64": `_linux_arm64\.tar\.gz$`}},
		binary: "memos",
		env:    []string{"MEMOS_MODE=prod", "MEMOS_PORT={{port}}", "MEMOS_DATA={{data}}"},
		exec:   "{{dir}}/memos",
		units:  []string{"memos.service"},
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
