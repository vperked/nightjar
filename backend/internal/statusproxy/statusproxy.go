// Package statusproxy puts the public status page on a domain of its own through nginx: it writes a
// site that forwards the domain to Nightjar, checks nginx takes it, reloads nginx, and asks certbot
// for an HTTPS certificate. Each step reports how it went; a site nginx will not take is removed
// again, so a mistake never leaves nginx unable to start.
package statusproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nightjar/internal/config"
)

// Marker opens every site Nightjar writes. A file without it is someone else's and is never
// overwritten or removed.
const Marker = "# Written by Nightjar for its public status page."

// Step is one thing done, and how it went.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Runner runs a command and returns what it printed.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Exec runs commands for real.
func Exec(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Options say where nginx keeps its sites and how to reach Nightjar. Zero values are the usual
// Debian and Ubuntu layout and real commands.
type Options struct {
	Domain string
	Port   int    // Nightjar's own port on this machine
	Email  string // for Let's Encrypt's expiry warnings; empty registers without one
	// NginxDir holds sites-available and sites-enabled, or conf.d.
	NginxDir string
	Run      Runner
	LookPath func(string) (string, error)
	// IsRoot reports whether nginx's files may be changed; tests stand in for root.
	IsRoot func() bool
}

func (o *Options) fill() {
	if o.NginxDir == "" {
		o.NginxDir = "/etc/nginx"
	}
	if o.Run == nil {
		o.Run = Exec
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.IsRoot == nil {
		o.IsRoot = func() bool { return os.Geteuid() == 0 }
	}
}

// Site is the server block that forwards the domain to Nightjar. It passes the Host on, which is
// how Nightjar knows to show the status page there and nothing else.
func Site(domain string, port int) string {
	return fmt.Sprintf(`%s
# Nightjar may rewrite or remove this file; certbot adds its HTTPS part below.
server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location / {
        proxy_pass http://127.0.0.1:%d;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
`, Marker, domain, port)
}

// paths are where the site's file goes, and the link that enables it (empty for conf.d, where a
// file is enabled by being there).
func (o Options) paths() (file, link string) {
	name := "nightjar-status-" + o.Domain + ".conf"
	if dirExists(filepath.Join(o.NginxDir, "sites-available")) && dirExists(filepath.Join(o.NginxDir, "sites-enabled")) {
		return filepath.Join(o.NginxDir, "sites-available", name), filepath.Join(o.NginxDir, "sites-enabled", name)
	}
	return filepath.Join(o.NginxDir, "conf.d", name), ""
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// State is what is set up now for a domain.
type State struct {
	Nginx     bool   `json:"nginx"`     // nginx is installed
	Certbot   bool   `json:"certbot"`   // certbot is installed
	Installed bool   `json:"installed"` // Nightjar's site for the domain is in place
	HTTPS     bool   `json:"https"`     // certbot has added HTTPS to it
	File      string `json:"file,omitempty"`
	Site      string `json:"site"` // the server block, to set up by hand instead
}

// Inspect says what is in place for o.Domain.
func Inspect(o Options) State {
	o.fill()
	_, nginxErr := o.LookPath("nginx")
	_, certbotErr := o.LookPath("certbot")
	st := State{Nginx: nginxErr == nil, Certbot: certbotErr == nil, Site: Site(o.Domain, o.Port)}
	file, _ := o.paths()
	if b, err := os.ReadFile(file); err == nil && bytes.HasPrefix(b, []byte(Marker)) {
		st.Installed, st.File = true, file
		st.HTTPS = bytes.Contains(b, []byte("ssl_certificate"))
	}
	return st
}

// serverNameRE finds the names a server block answers to.
var serverNameRE = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)

// claimedBy names a site other than Nightjar's that already answers to domain, or "".
func claimedBy(o Options, ours string) string {
	var dirs []string
	for _, d := range []string{"sites-enabled", "conf.d"} {
		dirs = append(dirs, filepath.Join(o.NginxDir, d))
	}
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if real, err := filepath.EvalSymlinks(p); err == nil && real == ours {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil || bytes.HasPrefix(b, []byte(Marker)) {
				continue
			}
			for _, m := range serverNameRE.FindAllSubmatch(b, -1) {
				for _, name := range strings.Fields(string(m[1])) {
					if strings.EqualFold(name, o.Domain) {
						return p
					}
				}
			}
		}
	}
	return ""
}

// ErrNotRoot is returned when Nightjar may not change nginx's configuration.
var ErrNotRoot = errors.New("setting up nginx needs Nightjar to run as root")

// Install writes and enables the site, checks it, reloads nginx and gets a certificate. It returns
// what it did; err is set when it stopped part-way. A site nginx will not take is removed again.
func Install(ctx context.Context, o Options) ([]Step, error) {
	o.fill()
	var steps []Step
	fail := func(name, detail string) ([]Step, error) {
		steps = append(steps, Step{Name: name, Detail: detail})
		return steps, errors.New(detail)
	}
	if err := config.CheckHostName(o.Domain); err != nil {
		return fail("Check the domain", err.Error())
	}
	if o.Port < 1 || o.Port > 65535 {
		return fail("Check the domain", fmt.Sprintf("port %d is not a port", o.Port))
	}
	if !o.IsRoot() {
		return fail("Check permissions", ErrNotRoot.Error())
	}
	if _, err := o.LookPath("nginx"); err != nil {
		return fail("Find nginx", "nginx is not installed. Install it (apt install nginx), or set up your own reverse proxy with the server block shown.")
	}
	file, link := o.paths()
	if other := claimedBy(o, file); other != "" {
		return fail("Check the domain is free", fmt.Sprintf("%s already serves %s; take it out of that site first, or add the status page's server block there yourself.", other, o.Domain))
	}
	if b, err := os.ReadFile(file); err == nil && !bytes.HasPrefix(b, []byte(Marker)) {
		return fail("Write the site", fmt.Sprintf("%s exists and was not written by Nightjar; it is left alone.", file))
	} else if err == nil && bytes.Contains(b, []byte("ssl_certificate")) {
		// Already set up with HTTPS: rewriting it would drop certbot's part. Check and reload only.
		steps = append(steps, Step{Name: "Write the site", OK: true, Detail: file + " is already in place, with HTTPS"})
		return finish(ctx, o, steps, file, link, true)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fail("Write the site", err.Error())
	}
	if err := os.WriteFile(file, []byte(Site(o.Domain, o.Port)), 0o644); err != nil {
		return fail("Write the site", err.Error())
	}
	steps = append(steps, Step{Name: "Write the site", OK: true, Detail: file})
	if link != "" {
		os.Remove(link)
		if err := os.Symlink(file, link); err != nil {
			os.Remove(file)
			return fail("Enable the site", err.Error())
		}
		steps = append(steps, Step{Name: "Enable the site", OK: true, Detail: link})
	}
	return finish(ctx, o, steps, file, link, false)
}

// finish checks nginx's configuration (undoing the site if nginx refuses it), reloads nginx and,
// unless the site has HTTPS already, runs certbot.
func finish(ctx context.Context, o Options, steps []Step, file, link string, https bool) ([]Step, error) {
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := o.Run(tctx, "nginx", "-t")
	cancel()
	if err != nil {
		if !https {
			if link != "" {
				os.Remove(link)
			}
			os.Remove(file)
		}
		detail := "nginx refused its configuration; the site was taken out again. " + lastLines(out, 4)
		steps = append(steps, Step{Name: "Check nginx's configuration", Detail: detail})
		return steps, errors.New(detail)
	}
	steps = append(steps, Step{Name: "Check nginx's configuration", OK: true})
	if err := reload(ctx, o); err != nil {
		steps = append(steps, Step{Name: "Reload nginx", Detail: err.Error()})
		return steps, err
	}
	steps = append(steps, Step{Name: "Reload nginx", OK: true})
	if https {
		return steps, nil
	}
	if _, err := o.LookPath("certbot"); err != nil {
		steps = append(steps, Step{Name: "Get an HTTPS certificate", Detail: "certbot is not installed (apt install certbot python3-certbot-nginx). The page works over HTTP meanwhile."})
		return steps, nil
	}
	args := []string{"--nginx", "-d", o.Domain, "--non-interactive", "--agree-tos", "--redirect", "--keep-until-expiring"}
	if o.Email != "" {
		args = append(args, "-m", o.Email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	out, err = o.Run(cctx, "certbot", args...)
	cancel()
	if err != nil {
		steps = append(steps, Step{Name: "Get an HTTPS certificate", Detail: "certbot could not get one; the page works over HTTP meanwhile. Check the domain's DNS points at this machine and port 80 is open, then try again. " + lastLines(out, 4)})
		return steps, nil
	}
	steps = append(steps, Step{Name: "Get an HTTPS certificate", OK: true, Detail: "certbot renews it on its own"})
	return steps, nil
}

func reload(ctx context.Context, o Options) error {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := o.Run(rctx, "systemctl", "reload", "nginx"); err == nil {
		return nil
	}
	if out, err := o.Run(rctx, "nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("could not reload nginx: %s", lastLines(out, 3))
	}
	return nil
}

// Remove takes Nightjar's site for o.Domain out and reloads nginx. The certificate, if any, stays
// with certbot, which can delete it (certbot delete --cert-name domain).
func Remove(ctx context.Context, o Options) ([]Step, error) {
	o.fill()
	if err := config.CheckHostName(o.Domain); err != nil {
		return []Step{{Name: "Check the domain", Detail: err.Error()}}, err
	}
	if !o.IsRoot() {
		return []Step{{Name: "Check permissions", Detail: ErrNotRoot.Error()}}, ErrNotRoot
	}
	file, link := o.paths()
	b, err := os.ReadFile(file)
	if err != nil {
		return []Step{{Name: "Remove the site", OK: true, Detail: "there was no site for " + o.Domain}}, nil
	}
	if !bytes.HasPrefix(b, []byte(Marker)) {
		err := fmt.Errorf("%s was not written by Nightjar; it is left alone", file)
		return []Step{{Name: "Remove the site", Detail: err.Error()}}, err
	}
	if link != "" {
		os.Remove(link)
	}
	if err := os.Remove(file); err != nil {
		return []Step{{Name: "Remove the site", Detail: err.Error()}}, err
	}
	steps := []Step{{Name: "Remove the site", OK: true, Detail: file}}
	if err := reload(ctx, o); err != nil {
		return append(steps, Step{Name: "Reload nginx", Detail: err.Error()}), err
	}
	return append(steps, Step{Name: "Reload nginx", OK: true}), nil
}

// lastLines is the end of a command's output, which is where nginx and certbot say what went wrong.
func lastLines(out string, n int) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}
