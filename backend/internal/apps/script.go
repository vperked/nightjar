package apps

import (
	"fmt"
	"strconv"
	"strings"
)

// Where store apps live. Each app's program goes in its own directory under Root, its settings and
// database under DataRoot, and a manifest under ManifestDir records that the store installed it.
const (
	Root        = "/opt"
	DataRoot    = "/var/lib"
	ManifestDir = "/etc/nightjar/apps"
	// DefaultUser runs store apps when the machine has no swizzin user to share files with.
	DefaultUser = "media"
)

// plan is one install, fully decided: the script must not need anything else.
type plan struct {
	app     App
	url     string // the release to download
	version string
	port    int
	user    string
}

func (p plan) dir() string  { return Root + "/" + p.app.ID }
func (p plan) data() string { return DataRoot + "/" + p.app.ID }
func (p plan) unit() string { return p.app.ID + ".service" }

// fill expands a recipe template.
func (p plan) fill(s string) string {
	return strings.NewReplacer("{{dir}}", p.dir(), "{{data}}", p.data(), "{{port}}", strconv.Itoa(p.port), "{{user}}", p.user).Replace(s)
}

// steps mark progress in the log: the dashboard shows the latest as the install's current step.
const prelude = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
step() { printf '::step:: %s\n' "$*"; }
`

// installScript is the whole install as one bash script, so the log shows every command's output
// and nothing depends on the monitor staying up.
func (p plan) installScript() string {
	a := p.app
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	b.WriteString(prelude)

	w(`step "Preparing"`)
	w(`id -u %[1]s >/dev/null 2>&1 || useradd --system --user-group --create-home --home-dir %[2]s/nightjar-apps --shell /usr/sbin/nologin %[1]s`, p.user, DataRoot)
	if a.dotnet {
		w(`if ! ldconfig -p | grep libicuuc >/dev/null; then step "Installing ICU (needed by .NET apps)"; apt-get update -qq; apt-get install -y -qq --no-install-recommends libicu-dev sqlite3; fi`)
	}

	w(`step "Downloading %s %s"`, a.Name, p.version)
	w(`tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT`)
	w(`curl -fL --retry 3 --progress-bar -o "$tmp/download" '%s' 2>&1 | tr '\r' '\n' | tail -n 1`, strings.ReplaceAll(p.url, "'", `'\''`))
	w(`systemctl stop %s 2>/dev/null || true`, p.unit())
	if a.source.single {
		w(`mkdir -p %s && install -m 0755 "$tmp/download" %s/%s`, p.dir(), p.dir(), a.binary)
	} else {
		w(`mkdir "$tmp/x" && tar -xf "$tmp/download" -C "$tmp/x"`)
		w(`bin=$(find "$tmp/x" -type f -name %q | head -n 1)`, a.binary)
		w(`[ -n "$bin" ] || { echo "%s was not found in the download"; exit 1; }`, a.binary)
		w(`rm -rf %[1]s && mkdir -p %[2]s && mv "$(dirname "$bin")" %[1]s && chmod +x %[1]s/%[3]s`, p.dir(), Root, a.binary)
	}

	w(`step "Configuring"`)
	w(`mkdir -p %s`, p.data())
	if a.setup != "" {
		w(`%s`, p.fill(a.setup))
	}
	w(`chown -R %[1]s:%[1]s %[2]s %[3]s`, p.user, p.dir(), p.data())

	w(`step "Creating the %s service"`, p.unit())
	w(`cat > /etc/systemd/system/%s <<'UNIT'`, p.unit())
	w(`[Unit]`)
	w(`Description=%s (installed by Nightjar)`, a.Name)
	w(`After=network-online.target`)
	w(`Wants=network-online.target`)
	w(``)
	w(`[Service]`)
	w(`User=%[1]s`, p.user)
	w(`Group=%[1]s`, p.user)
	w(`WorkingDirectory=%s`, p.data())
	for _, e := range a.env {
		w(`Environment=%s`, p.fill(e))
	}
	w(`ExecStart=%s`, p.fill(a.exec))
	w(`Restart=on-failure`)
	w(`RestartSec=5`)
	w(``)
	w(`[Install]`)
	w(`WantedBy=multi-user.target`)
	w(`UNIT`)
	w(`systemctl daemon-reload`)
	w(`systemctl enable --now %s`, p.unit())

	w(`step "Starting %s"`, a.Name)
	if p.port > 0 {
		w(`for _ in $(seq 1 90); do curl -s -o /dev/null --max-time 2 http://127.0.0.1:%d/ && break; sleep 1; done`, p.port)
	} else {
		w(`sleep 3`)
	}
	w(`systemctl is-active --quiet %[1]s || { echo "%[2]s did not start. Its log:"; journalctl -u %[1]s -n 30 --no-pager -o cat; exit 1; }`, p.unit(), a.Name)
	if p.port > 0 {
		w(`curl -s -o /dev/null --max-time 3 http://127.0.0.1:%[1]d/ || { echo "%[2]s is running but does not answer on port %[1]d yet"; }`, p.port, a.Name)
	}
	if a.after != "" {
		w(`%s`, p.fill(a.after))
	}

	w(`mkdir -p %s`, ManifestDir)
	w(`printf '{"id":%q,"version":%q,"port":%d,"user":%q,"installed":%%s}\n' "$(date +%%s)" > %s/%s.json`, a.ID, p.version, p.port, p.user, ManifestDir, a.ID)
	w(`step "Installed"`)
	return b.String()
}

// removeScript stops and deletes a store app; purge also deletes its settings and database.
func removeScript(a App, purge bool) string {
	unit := a.ID + ".service"
	var b strings.Builder
	b.WriteString(prelude)
	fmt.Fprintf(&b, "step \"Stopping %s\"\n", a.Name)
	fmt.Fprintf(&b, "systemctl disable --now %s 2>/dev/null || true\n", unit)
	fmt.Fprintf(&b, "rm -f /etc/systemd/system/%s\nsystemctl daemon-reload\n", unit)
	b.WriteString("step \"Removing files\"\n")
	fmt.Fprintf(&b, "rm -rf %s/%s\n", Root, a.ID)
	if purge {
		fmt.Fprintf(&b, "rm -rf %s/%s\n", DataRoot, a.ID)
	}
	fmt.Fprintf(&b, "rm -f %s/%s.json\n", ManifestDir, a.ID)
	b.WriteString("step \"Removed\"\n")
	return b.String()
}
