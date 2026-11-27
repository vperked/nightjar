// Package api serves the JSON API and the built React frontend.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/collect"
	"nightjar/internal/logx"
	"nightjar/internal/monitor"
	"nightjar/internal/statusproxy"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
	"nightjar/internal/web"
)

var log = logx.Component("Web")

const (
	authFailLimit  = 10
	authFailWindow = time.Minute
	// historyPoints is the most points one response-time chart gets; longer ranges are bucketed.
	historyPoints = 720
)

// Config is what the server needs from settings.
type Config struct {
	Host, Port string
	// Accounts is the admin account everything needs, nil only in tests: then AuthToken alone
	// guards the API, and no token leaves it open.
	Accounts *accounts.Accounts
	// AuthToken lets scripts use the API without signing in.
	AuthToken     string
	AllowedOrigin string
	FrontendDir   string
	// StatusProxy adjusts the nginx set-up for the status page's domain; tests point it at a
	// temporary nginx folder and fake commands.
	StatusProxy func(*statusproxy.Options)
	Version     string
	// Store holds dashboard settings; Restart applies them (nil in tests).
	Store   *storage.Store
	Restart func()
	// Detect finds update targets for the dashboard's "Detect" button (updates.Detect when nil).
	Detect func() []updates.Spec
}

// Server is the HTTP API. Monitor state comes from the loop's context; nothing here
// blocks on slow collectors beyond a short cache.
type Server struct {
	cfg  Config
	ctx  *monitor.Context
	http *http.Server

	failMu   sync.Mutex
	failures map[string][]time.Time // by client address (an IPv6 one's /64), within authFailWindow
	held     map[string]*heldBack   // by account, or by a browser known to it (see limitKey)
	hashing  chan struct{}          // a slot for each password check running

	// noRealIP and noForwarded are set once requests through a proxy on this machine show that it
	// does not set that header, so whatever it holds is the client's own (see learnProxy).
	noRealIP, noForwarded atomic.Bool
	// proxyMu guards proxySeen, the last requests from this machine that carried just one of the
	// two headers (true for X-Real-IP), and proxyNext, where the next one goes.
	proxyMu   sync.Mutex
	proxySeen []bool
	proxyNext int

	statsMu  sync.Mutex
	stats    *Stats
	statsAt  time.Time
	statsTTL time.Duration

	days     dayCache     // the status page's days per check
	ends     endedCache   // when recent incidents ended, for the status page's notices
	findings findingCache // what the log's warnings point to, for the log viewer

	testMu   sync.Mutex
	lastTest time.Time // when a test alert last went out
}

// testAlertGap keeps test alerts from being sent over and over, by a stuck button or a script.
const testAlertGap = 10 * time.Second

// allowTestAlert claims the next test alert, or says how long until one is allowed.
func (s *Server) allowTestAlert() (wait time.Duration) {
	s.testMu.Lock()
	defer s.testMu.Unlock()
	if wait := testAlertGap - time.Since(s.lastTest); wait > 0 {
		return wait
	}
	s.lastTest = time.Now()
	return 0
}

// tooSoon answers a test alert asked for within testAlertGap of the last.
func tooSoon(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeJSON(w, http.StatusTooManyRequests, errBody(fmt.Sprintf("A test alert was just sent; try again in %ds.", int(wait.Seconds())+1)))
}

func New(cfg Config, ctx *monitor.Context) *Server {
	s := &Server{cfg: cfg, ctx: ctx, failures: map[string][]time.Time{}, held: map[string]*heldBack{}, hashing: make(chan struct{}, maxHashing), statsTTL: 2 * time.Second}
	s.recallProxy()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("POST /api/auth/setup", s.fromDashboard(s.createAdmin))
	mux.HandleFunc("POST /api/auth/login", s.fromDashboard(s.login))
	mux.HandleFunc("POST /api/auth/logout", s.fromDashboard(s.logout))
	// Any signed-in account changes its own password, a viewer included.
	mux.Handle("POST /api/auth/password", s.auth(s.fromDashboard(s.logged(s.changePassword))))
	mux.Handle("POST /api/auth/2fa/begin", s.writable(s.beginTwoFactor))
	mux.Handle("POST /api/auth/2fa/enable", s.writable(s.enableTwoFactor))
	mux.Handle("POST /api/auth/2fa/disable", s.writable(s.disableTwoFactor))
	mux.Handle("GET /api/auth/viewers", s.admin(s.listViewers))
	mux.Handle("POST /api/auth/viewers", s.writable(s.addViewer))
	mux.Handle("PUT /api/auth/viewers/{name}", s.writable(s.setViewerPassword))
	mux.Handle("DELETE /api/auth/viewers/{name}", s.writable(s.removeViewer))
	mux.Handle("GET /api/activity", s.auth(s.activity))
	mux.Handle("GET /api/backup", s.admin(s.makeBackup))
	// A backup can be larger than other changes, so it skips their size limit for its own.
	mux.Handle("POST /api/backup/restore", s.admin(s.logged(func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(w, r) {
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, errBody("Send JSON"))
			return
		}
		s.restoreBackup(w, r)
	})))
	mux.HandleFunc("GET /api/status", s.statusPage)
	mux.Handle("GET /api/incidents", s.auth(s.incidents))
	mux.Handle("PUT /api/incidents/{id}/feedback", s.writable(s.saveFeedback))
	mux.Handle("DELETE /api/incidents/{id}/feedback", s.writable(s.saveFeedback))
	mux.Handle("PUT /api/incidents/{id}/notice", s.writable(s.saveNotice))
	mux.Handle("DELETE /api/incidents/{id}/notice", s.writable(s.saveNotice))
	mux.Handle("GET /api/maintenance", s.auth(s.maintenanceList))
	mux.Handle("POST /api/maintenance", s.writable(s.saveMaintenance))
	mux.Handle("PUT /api/maintenance/{id}", s.writable(s.saveMaintenance))
	mux.Handle("DELETE /api/maintenance/{id}", s.writable(s.deleteMaintenance))
	mux.Handle("GET /api/docker/containers", s.auth(s.containers))
	mux.Handle("GET /api/report", s.auth(s.weeklyReport))
	mux.Handle("GET /api/plex/stats", s.auth(s.plexStats))
	mux.Handle("GET /api/cleanup", s.auth(s.cleanupView))
	mux.Handle("POST /api/cleanup/scan", s.writable(slow(5*time.Minute, s.cleanupScan)))
	mux.Handle("PUT /api/cleanup/{key}/keep", s.writable(s.cleanupKeep))
	mux.Handle("DELETE /api/cleanup/{key}/keep", s.writable(s.cleanupKeep))
	mux.Handle("POST /api/report/send", s.writable(slow(time.Minute, s.sendReport)))
	mux.Handle("GET /api/appearance", s.auth(s.getAppearance))
	mux.Handle("PUT /api/appearance", s.writable(s.saveAppearance))
	mux.Handle("GET /api/statuspage", s.auth(s.statusPageDesign))
	mux.Handle("PUT /api/statuspage", s.writableUpTo(maxDesignBody, s.saveStatusPageDesign))
	mux.Handle("GET /api/statuspage/proxy", s.admin(s.statusProxy))
	// Testing nginx, reloading it and certbot's challenge take a while.
	mux.Handle("POST /api/statuspage/proxy", s.writable(slow(5*time.Minute, s.setUpStatusProxy)))
	mux.Handle("DELETE /api/statuspage/proxy", s.writable(slow(time.Minute, s.removeStatusProxy)))
	mux.Handle("GET /api/stats", s.auth(s.serveStats))
	mux.Handle("GET /api/logs", s.auth(s.logs))
	mux.Handle("GET /api/apps", s.auth(s.needApps(s.listApps)))
	mux.Handle("GET /api/apps/job", s.auth(s.needApps(s.appJob)))
	mux.Handle("POST /api/apps/{id}/install", s.writable(s.needApps(s.installApp)))
	mux.Handle("POST /api/apps/{id}/remove", s.writable(s.needApps(s.removeApp)))
	mux.Handle("GET /api/plugins", s.admin(s.needPlugins(s.listPlugins)))
	mux.Handle("GET /api/plugins/cards", s.auth(s.needPlugins(s.pluginCards)))
	mux.Handle("POST /api/plugins/rescan", s.writable(s.needPlugins(s.rescanPlugins)))
	mux.Handle("PUT /api/plugins/{id}", s.writable(s.needPlugins(s.configurePlugin)))
	mux.Handle("POST /api/plugins/{id}/try", s.writable(slow(2*time.Minute, s.needPlugins(s.tryPlugin))))
	mux.Handle("GET /api/notifications", s.auth(s.notifications))
	mux.Handle("GET /api/notifications/summary", s.auth(s.notificationSummary))
	mux.Handle("POST /api/notifications/read", s.writable(s.markNotifications))
	mux.Handle("GET /api/notifications/rules", s.auth(s.notificationRules))
	mux.Handle("PUT /api/notifications/rules", s.writable(s.saveNotificationRules))
	mux.Handle("POST /api/notifications/mute", s.writable(s.mute))
	mux.Handle("GET /api/hosts", s.auth(s.needRemote(s.listHosts)))
	mux.Handle("GET /api/hosts/{id}", s.auth(s.needRemote(s.getHost)))
	mux.Handle("POST /api/hosts/token", s.writable(s.needRemote(s.newHostToken)))
	mux.Handle("GET /api/agent/download", s.admin(s.downloadAgent))
	mux.Handle("GET /api/analytics", s.auth(s.analyticsAll))
	mux.Handle("GET /api/analytics/{id}", s.auth(s.analyticsHost))
	mux.Handle("POST /api/hosts", s.writable(slow(sshBudget, s.needRemote(s.saveHost))))
	mux.Handle("PUT /api/hosts/{id}", s.writable(slow(sshBudget, s.needRemote(s.saveHost))))
	mux.Handle("DELETE /api/hosts/{id}", s.writable(s.needRemote(s.deleteHost)))
	mux.Handle("PUT /api/hosts/{id}/ssh", s.writable(slow(sshBudget, s.needRemote(s.setHostSSH))))
	mux.Handle("DELETE /api/hosts/{id}/ssh", s.writable(s.needRemote(s.setHostSSH)))
	mux.Handle("POST /api/hosts/{id}/ssh/{action}", s.writable(slow(sshBudget, s.needRemote(s.runHostSSH))))
	mux.Handle("GET /api/hosts/{id}/firewall", s.auth(slow(hostFirewallBudget, s.needRemote(s.hostFirewall))))
	mux.Handle("POST /api/hosts/{id}/firewall/rules", s.writable(slow(hostFirewallBudget, s.needRemote(s.addHostRule))))
	mux.Handle("PUT /api/hosts/{id}/firewall/rules/{rule}", s.writable(slow(hostFirewallBudget, s.needRemote(s.updateHostRule))))
	mux.Handle("DELETE /api/hosts/{id}/firewall/rules/{rule}", s.writable(slow(hostFirewallBudget, s.needRemote(s.removeHostRule))))
	mux.Handle("PUT /api/hosts/{id}/firewall/order", s.writable(slow(hostFirewallBudget, s.needRemote(s.orderHostRules))))
	mux.Handle("PUT /api/hosts/{id}/firewall/default", s.writable(slow(hostFirewallBudget, s.needRemote(s.setHostDefault))))
	mux.Handle("POST /api/hosts/{id}/firewall/default/keep", s.writable(slow(hostFirewallBudget, s.needRemote(s.keepHostDefault))))
	mux.Handle("POST /api/hosts/{id}/firewall/locks", s.writable(slow(hostFirewallBudget, s.needRemote(s.lockHostPort))))
	mux.Handle("PUT /api/hosts/{id}/firewall/locks/{lock}", s.writable(slow(hostFirewallBudget, s.needRemote(s.relockHostPort))))
	mux.Handle("DELETE /api/hosts/{id}/firewall/locks/{lock}", s.writable(slow(hostFirewallBudget, s.needRemote(s.unlockHostPort))))
	mux.Handle("PUT /api/hosts/{id}/defense", s.writable(slow(hostFirewallBudget, s.needRemote(s.setHostDefense))))
	mux.Handle("DELETE /api/hosts/{id}/blocked/{address}", s.writable(slow(hostFirewallBudget, s.needRemote(s.unblockHost))))
	mux.Handle("GET /api/hosts/updates", s.auth(s.needRemote(s.hostUpdatesSummary)))
	mux.Handle("GET /api/hosts/{id}/updates", s.auth(slow(hostUpdatesBudget, s.needRemote(s.hostUpdates))))
	mux.Handle("POST /api/hosts/{id}/updates", s.writable(slow(hostUpdatesBudget, s.needRemote(s.saveHostUpdate))))
	mux.Handle("PUT /api/hosts/{id}/updates/settings", s.writable(slow(hostUpdatesBudget, s.needRemote(s.setHostUpdateSettings))))
	mux.Handle("PUT /api/hosts/{id}/updates/{target}", s.writable(slow(hostUpdatesBudget, s.needRemote(s.saveHostUpdate))))
	mux.Handle("DELETE /api/hosts/{id}/updates/{target}", s.writable(slow(hostUpdatesBudget, s.needRemote(s.deleteHostUpdate))))
	mux.Handle("POST /api/hosts/{id}/updates/check", s.writable(slow(hostUpdatesBudget, s.needRemote(s.checkHostUpdates))))
	mux.Handle("POST /api/hosts/{id}/updates/detect", s.writable(slow(hostUpdatesBudget, s.needRemote(s.detectHostUpdates))))
	mux.Handle("POST /api/hosts/{id}/updates/{target}/check", s.writable(slow(hostUpdatesBudget, s.needRemote(s.checkHostUpdates))))
	mux.Handle("POST /api/hosts/{id}/updates/{target}/update", s.writable(slow(hostUpdatesBudget, s.needRemote(s.applyHostUpdate))))
	mux.Handle("GET /api/hosts/{id}/sshd", s.admin(slow(hostFirewallBudget, s.needRemote(s.hostSSHD))))
	mux.Handle("POST /api/hosts/{id}/sshd/keys", s.writable(slow(hostSSHDBudget, s.needRemote(s.addHostSSHKey))))
	mux.Handle("PUT /api/hosts/{id}/sshd/keys-only", s.writable(slow(hostSSHDBudget, s.needRemote(s.setHostKeysOnly))))
	mux.Handle("POST /api/hosts/{id}/sshd/keys-only/keep", s.writable(slow(hostSSHDBudget, s.needRemote(s.keepHostKeysOnly))))
	mux.Handle("GET /api/hosts/{id}/tailscale", s.admin(slow(hostFirewallBudget, s.needRemote(s.hostTailscale))))
	mux.Handle("POST /api/hosts/{id}/tailscale/login", s.writable(slow(tailscaleBudget, s.needRemote(s.hostTailscaleLogin))))
	mux.Handle("PUT /api/hosts/{id}/tailscale/settings", s.writable(slow(tailscaleBudget, s.needRemote(s.setHostTailscale))))
	mux.Handle("POST /api/hosts/{id}/tailscale/keep", s.writable(slow(tailscaleBudget, s.needRemote(s.keepHostTailscale))))
	mux.Handle("POST /api/hosts/{id}/tailscale/{action}", s.writable(slow(tailscaleBudget, s.needRemote(s.hostTailscaleAction))))
	mux.Handle("GET /api/ssh/keys", s.admin(s.needRemote(s.sshKeys)))
	mux.Handle("POST /api/ssh/keys", s.writable(s.needRemote(s.newSSHKey)))
	mux.Handle("POST /api/ssh/authorize", s.writable(slow(sshAuthorizeBudget, s.needRemote(s.authorizeSSHKey))))
	mux.Handle("POST /api/ssh/trust", s.writable(slow(time.Minute, s.needRemote(s.trustSSHHostKey))))
	mux.Handle("GET /api/services", s.auth(s.services))
	mux.Handle("GET /api/services/history", s.auth(s.serviceHistory))
	mux.Handle("GET /api/plex", s.auth(s.plex))
	mux.Handle("POST /api/plex/streams/{id}/stop", s.writable(s.plexStop))
	mux.Handle("GET /api/qbittorrent", s.auth(s.qbittorrent))
	mux.Handle("GET /api/qbittorrent/categories", s.auth(s.needQB(s.qbCategories)))
	mux.Handle("POST /api/qbittorrent/action", s.writable(s.needQB(s.qbAction)))
	mux.Handle("POST /api/qbittorrent/add", s.writable(s.needQB(s.qbAdd)))
	mux.Handle("GET /api/checks", s.auth(s.listChecks))
	mux.Handle("POST /api/checks", s.writable(s.saveCheck))
	mux.Handle("POST /api/checks/test", s.writable(s.testCheck))
	mux.Handle("PUT /api/checks/{name}", s.writable(s.saveCheck))
	mux.Handle("DELETE /api/checks/{name}", s.writable(s.deleteCheck))
	mux.Handle("POST /api/checks/{name}/repair", s.writable(slow(3*time.Minute, s.repairCheck)))
	mux.Handle("GET /api/repairs", s.auth(s.repairs))
	// Discord signs each button click with the bot's key; the bot checks it, and who clicked.
	mux.HandleFunc("POST /discord/interactions", func(w http.ResponseWriter, r *http.Request) {
		if s.ctx.DiscordBot == nil {
			http.NotFound(w, r)
			return
		}
		s.ctx.DiscordBot.Interactions(w, r)
	})
	// A heartbeat's token is its credential; jobs ping without signing in.
	mux.HandleFunc("/ping/{token}", s.ping)
	mux.HandleFunc("/ping/{token}/{kind}", s.ping)
	mux.Handle("GET /api/settings", s.admin(s.settings))
	mux.Handle("PUT /api/settings", s.writable(s.saveSettings))
	mux.Handle("POST /api/settings/test", s.writable(s.testSettings))
	mux.Handle("GET /api/setup", s.admin(s.setupStatus))
	mux.Handle("POST /api/alerts/test", s.writable(s.testAlerts))
	mux.Handle("GET /api/units", s.auth(s.listUnits))
	// A service's journal can hold secrets (qBittorrent prints its first WebUI password there).
	mux.Handle("GET /api/units/{name}/logs", s.admin(s.unitLogs))
	mux.Handle("POST /api/units/{name}/{action}", s.writable(slow(90*time.Second, s.controlUnit)))
	mux.Handle("GET /api/drives", s.auth(s.drives))
	mux.Handle("GET /api/network", s.auth(s.network))
	mux.Handle("GET /api/attacks", s.auth(s.attacks))
	mux.Handle("PUT /api/attacks/defense", s.writable(s.setDefense))
	mux.Handle("DELETE /api/attacks/blocked/{address}", s.writable(s.unblock))
	mux.Handle("GET /api/firewall", s.auth(s.localFirewall))
	mux.Handle("POST /api/firewall/rules", s.writable(s.addLocalRule))
	mux.Handle("PUT /api/firewall/rules/{rule}", s.writable(s.updateLocalRule))
	mux.Handle("DELETE /api/firewall/rules/{rule}", s.writable(s.removeLocalRule))
	mux.Handle("PUT /api/firewall/order", s.writable(s.orderLocalRules))
	mux.Handle("PUT /api/firewall/default", s.writable(s.setLocalDefault))
	mux.Handle("POST /api/firewall/default/keep", s.writable(s.keepLocalDefault))
	mux.Handle("POST /api/firewall/locks", s.writable(s.lockLocalPort))
	mux.Handle("PUT /api/firewall/locks/{lock}", s.writable(s.relockLocalPort))
	mux.Handle("DELETE /api/firewall/locks/{lock}", s.writable(s.unlockLocalPort))
	// Account names and key fingerprints are for admins, like the dashboard's own SSH keys.
	mux.Handle("GET /api/sshd", s.admin(s.localSSHD))
	mux.Handle("POST /api/sshd/keys", s.writable(s.addLocalSSHKey))
	mux.Handle("PUT /api/sshd/keys-only", s.writable(slow(sshdBudget, s.setLocalKeysOnly)))
	mux.Handle("POST /api/sshd/keys-only/keep", s.writable(slow(sshdBudget, s.keepLocalKeysOnly)))
	// Tailnet machines, their addresses and owners are for admins, like SSH logins.
	mux.Handle("GET /api/tailscale", s.admin(slow(tailscaleBudget, s.localTailscale)))
	mux.Handle("POST /api/tailscale/login", s.writable(slow(tailscaleBudget, s.localTailscaleLogin)))
	mux.Handle("PUT /api/tailscale/settings", s.writable(slow(tailscaleBudget, s.setLocalTailscale)))
	mux.Handle("POST /api/tailscale/keep", s.writable(slow(tailscaleBudget, s.keepLocalTailscale)))
	mux.Handle("POST /api/tailscale/{action}", s.writable(slow(tailscaleBudget, s.localTailscaleAction)))
	mux.Handle("GET /api/updates", s.auth(s.updates))
	mux.Handle("POST /api/updates", s.writable(s.needUpdates(s.saveUpdateTarget)))
	mux.Handle("POST /api/updates/check", s.writable(s.needUpdates(s.checkUpdates)))
	// Looking for apps can take a couple of minutes, as on an agent.
	mux.Handle("POST /api/updates/detect", s.writable(slow(detectBudget, s.needUpdates(s.detectUpdates))))
	mux.Handle("PUT /api/updates/{id}", s.writable(s.needUpdates(s.saveUpdateTarget)))
	mux.Handle("DELETE /api/updates/{id}", s.writable(s.needUpdates(s.deleteUpdateTarget)))
	mux.Handle("POST /api/updates/{id}/check", s.writable(s.needUpdates(s.checkUpdates)))
	mux.Handle("POST /api/updates/{id}/update", s.writable(s.needUpdates(s.applyUpdate)))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusNotFound, errBody("Not Found")) })
	mux.Handle("/", s.frontend())
	s.http = &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           compress(s.headers(s.statusHostOnly(mux))),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// sshBudget is the longest an SSH action takes: finding the server's architecture, then the action
// itself (an agent reinstall streams the whole build over).
const sshBudget = 30*time.Second + 5*time.Minute

// hostFirewallBudget is the longest a call to a server's agent about its firewall takes.
const hostFirewallBudget = 30 * time.Second

// hostSSHDBudget is the longest a change of a server's SSH logins through its agent takes.
const hostSSHDBudget = time.Minute

// sshAuthorizeBudget is the longest putting a key on a server takes: checking its host key (a scan
// and fingerprinting each key it presents), then a minute's password login.
const sshAuthorizeBudget = 30*time.Second + time.Minute

// slow lets a handler whose work may run past the server's WriteTimeout still answer: without it, a
// unit restart or an agent reinstall that took a minute would be carried out, but the browser would
// get no reply and report it failed. budget is the longest the work may take.
func slow(budget time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Not every writer can (a test's recorder cannot); those have no deadline to extend.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget + 30*time.Second))
		next(w, r)
	}
}

func (s *Server) detect() []updates.Spec {
	if s.cfg.Detect != nil {
		return s.cfg.Detect()
	}
	return updates.Detect(updates.Exec)
}

// Handler exposes the routes for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ErrPublicWithoutToken refuses to expose host metrics on a public interface unauthenticated.
var ErrPublicWithoutToken = errors.New("refusing to bind dashboard to a non-loopback address without WEB_AUTH_TOKEN; set WEB_AUTH_TOKEN, or set WEB_HOST=127.0.0.1 for local-only access")

// Start listens in the background. With the admin account every address is safe to listen on;
// without it (tests), a public address needs a token.
func (s *Server) Start() error {
	if !isLoopback(s.cfg.Host) && s.cfg.AuthToken == "" && s.cfg.Accounts == nil {
		return ErrPublicWithoutToken
	}
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	log.Info("Web dashboard listening", "addr", "http://"+ln.Addr().String())
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("Web server stopped", "err", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) headers(next http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'"
	// The status page, and its designer's preview, may show the owner's images, fonts and
	// stylesheets from other sites. Scripts stay the page's own.
	custom := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https:; img-src 'self' data: https:; " +
		"font-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before anything else, so a rebound page cannot even teach learnProxy anything.
		if !s.knownHost(r) {
			log.Debug("Refused a request for an unknown host name", "host", r.Host)
			http.Error(w, "Nightjar does not know this address. Open it by IP address or localhost, or set DASHBOARD_URL to the address you use.", http.StatusForbidden)
			return
		}
		s.learnProxy(r)
		h := w.Header()
		if r.URL.Path == "/status-page" || s.isStatusPage(r) {
			h.Set("Content-Security-Policy", custom)
		} else {
			h.Set("Content-Security-Policy", csp)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Only a site named in the settings may read answers from another origin. Left unset, no
		// header goes out: "null" would let sandboxed frames and local files read them.
		if o := s.cfg.AllowedOrigin; o != "" && o != "null" {
			h.Set("Access-Control-Allow-Origin", s.cfg.AllowedOrigin)
			h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token")
		}
		h.Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.Contains(r.URL.RawQuery, "\x00") || len(r.URL.RawQuery) > 2000 {
			writeJSON(w, http.StatusBadRequest, errBody("Bad Request"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// knownHost guards against DNS rebinding: a page on evil.example whose name is then pointed at
// 127.0.0.1 is, to the browser, its own origin, so it could talk to a dashboard that listens only on
// this machine (or at the end of an SSH tunnel) as if it were its own site, passing every same-origin
// check, since its Origin matches the Host it sends. Such a request comes straight from the browser,
// so its Host is the attacker's name with the dashboard's own port.
//
// So, as Syncthing does: on a dashboard listening only on loopback, a Host naming the dashboard's port
// must be an IP address, localhost (or a name under .localhost, which browsers keep to this machine
// themselves), or a name in the settings (DASHBOARD_URL's, STATUS_PAGE_DOMAIN). Anything else is
// refused. A Host for another port (or none, port 80) is let through: that came through a reverse
// proxy on this machine, which serves its own port and passes on the name it was asked for (nginx's
// proxy_set_header Host $host). A dashboard listening on other addresses is reached by name from the
// network anyway, and is left alone.
func (s *Server) knownHost(r *http.Request) bool {
	if !isLoopback(s.cfg.Host) || r.Host == "" {
		return true
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, ""
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if port == "" {
		port = "80"
	}
	if port != s.cfg.Port {
		return true
	}
	cfg := s.ctx.Config()
	if d, err := url.Parse(cfg.DashboardURL); err == nil && d.Hostname() != "" && strings.EqualFold(strings.TrimSuffix(d.Hostname(), "."), host) {
		return true
	}
	return cfg.StatusPageDomain != "" && strings.EqualFold(cfg.StatusPageDomain, host)
}

// clientIPs is who is asking, as the sign-in limits see it. Behind a reverse proxy on this machine
// (nginx on most media boxes) every request comes from loopback, so the proxy names the client: nginx
// set up as the docs say in X-Real-IP; Caddy, cloudflared and nginx's $proxy_add_x_forwarded_for as
// the last X-Forwarded-For hop. Each of them passes the other header on just as the client sent it.
// Once learnProxy has seen which one the proxy leaves out, only the other is taken, so a made-up
// header can neither slip the limits nor count failures against someone else's address. Until then
// (or behind two proxies that set one each) both are taken, when they hold an address: a client still
// cannot slip the limits. From anywhere else those headers could be forged and are ignored.
func (s *Server) clientIPs(r *http.Request) []string {
	host, local := peer(r)
	if !local {
		return []string{host}
	}
	noRealIP, noForwarded := s.noRealIP.Load(), s.noForwarded.Load()
	var out []string
	add := func(v string) {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil && !slices.Contains(out, ip.String()) {
			out = append(out, ip.String())
		}
	}
	if !noRealIP || noForwarded {
		add(r.Header.Get("X-Real-IP"))
	}
	if fwd := r.Header.Values("X-Forwarded-For"); len(fwd) > 0 && (!noForwarded || noRealIP) {
		parts := strings.Split(fwd[len(fwd)-1], ",")
		add(parts[len(parts)-1])
	}
	if len(out) == 0 {
		return []string{host}
	}
	return out
}

// proxyWindow is how many of the latest telling requests learnProxy weighs, and proxyEvidence how
// many it waits for before it believes any.
const (
	proxyWindow   = 64
	proxyEvidence = 16
)

// learnProxy notes which of X-Real-IP and X-Forwarded-For the proxy on this machine does not set. A
// browser sends neither, so a proxied request with one and not the other suggests that the proxy
// leaves the other out. A client can add a header but not take away one the proxy sets, so this
// cannot be faked from outside.
//
// It can from this machine, though: any program here may send a request straight to the dashboard
// with whichever header it likes, and the dashboard cannot tell it from the proxy. One such request
// must not be enough to trust a header the real proxy passes on untouched. So no single request
// decides: of the latest requests that carried just one of the two, the header most of them had is
// the proxy's, once there are enough of them. The proxy's own traffic, a few requests for every page
// and poll, soon outweighs a stray request, and puts right whatever one taught it.
func (s *Server) learnProxy(r *http.Request) {
	if _, local := peer(r); !local {
		return
	}
	realIP, forwarded := r.Header.Get("X-Real-IP") != "", len(r.Header.Values("X-Forwarded-For")) > 0
	if realIP == forwarded {
		return // neither (this machine asking for itself) or both: nothing to learn
	}
	s.proxyMu.Lock()
	if len(s.proxySeen) < proxyWindow {
		s.proxySeen = append(s.proxySeen, realIP)
	} else {
		s.proxySeen[s.proxyNext] = realIP
	}
	s.proxyNext = (s.proxyNext + 1) % proxyWindow
	reals := 0
	for _, v := range s.proxySeen {
		if v {
			reals++
		}
	}
	others := len(s.proxySeen) - reals
	learned := false
	if len(s.proxySeen) >= proxyEvidence && reals != others {
		// The proxy sets the header most requests had, and leaves the other to the client.
		noForwarded := reals > others
		learned = s.noForwarded.Swap(noForwarded) != noForwarded
		learned = s.noRealIP.Swap(!noForwarded) != !noForwarded || learned
	}
	s.proxyMu.Unlock()
	if learned && s.cfg.Store != nil {
		// Kept, so a restart does not reopen the window in which a made-up header counts failures
		// against someone else's address until the proxy shows itself again.
		value := s.proxyMeta()
		go s.cfg.Store.SetMeta(proxyKey, value)
	}
}

// proxyKey is the meta key holding what learnProxy found out, as proxyMeta writes it.
const proxyKey = "proxy.unset_headers"

// proxyMeta is the headers the proxy is known to leave out, comma-separated.
func (s *Server) proxyMeta() string {
	var unset []string
	if s.noRealIP.Load() {
		unset = append(unset, "x-real-ip")
	}
	if s.noForwarded.Load() {
		unset = append(unset, "x-forwarded-for")
	}
	return strings.Join(unset, ",")
}

// recallProxy restores what learnProxy found out before the last restart.
func (s *Server) recallProxy() {
	if s.cfg.Store == nil {
		return
	}
	for _, h := range strings.Split(s.cfg.Store.Meta(proxyKey), ",") {
		switch h {
		case "x-real-ip":
			s.noRealIP.Store(true)
		case "x-forwarded-for":
			s.noForwarded.Store(true)
		}
	}
}

// peer is the address the request came from, and whether that is this machine.
func peer(r *http.Request) (host string, local bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return host, ip != nil && ip.IsLoopback()
}

// clientIP is the one address to act on for the client, such as the one the firewall keeps reachable.
func (s *Server) clientIP(r *http.Request) string { return s.clientIPs(r)[0] }

// clientNames names the client for the logs: every address the proxy gave, when they differ.
func (s *Server) clientNames(r *http.Request) string { return strings.Join(s.clientIPs(r), " or ") }

// access is what a request's credentials come to.
type access int

const (
	denied access = iota
	allowed
	lockedOut // a token was sent from a client locked out for guessing, and was not looked at
)

// access accepts a signed-in browser's session cookie, or WEB_AUTH_TOKEN as
// "Authorization: Bearer <token>" or "X-Auth-Token". A session cookie cannot be guessed, so it is
// let in even while the client's address is locked out: others sharing that address (behind the
// same proxy or edge) cannot lock the admin out of a dashboard already open. A token can be guessed,
// so it is only looked at while the client is not locked out, and every wrong one counts, wherever
// it is sent: the public endpoints that say whether the caller is signed in included.
//
// It also says who the request is from: the session's account, or the admin for the token (and for
// a server with no sign-in at all, which only tests run).
func (s *Server) access(r *http.Request) (access, accounts.Who) {
	if s.cfg.Accounts != nil {
		if who, ok := s.cfg.Accounts.SessionWho(sessionCookie(r)); ok {
			return allowed, who
		}
	}
	admin := accounts.Who{Role: accounts.RoleAdmin}
	if s.cfg.AuthToken == "" {
		if s.cfg.Accounts == nil {
			return allowed, admin
		}
		return denied, accounts.Who{}
	}
	token := presentedToken(r)
	if token == "" {
		return denied, accounts.Who{}
	}
	if s.throttled(r) {
		return lockedOut, accounts.Who{}
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AuthToken)) == 1 {
		admin.Username = "API token"
		return allowed, admin
	}
	s.recordFailure(r)
	log.Warn("Unauthorized request", "path", r.URL.Path, "ip", s.clientNames(r))
	return denied, accounts.Who{}
}

// authorized reports whether the request is signed in, for the public endpoints that only say more
// when it is.
func (s *Server) authorized(r *http.Request) bool {
	a, _ := s.access(r)
	return a == allowed
}

type whoKey struct{}

// whoFrom is who a request that went through auth is from.
func whoFrom(r *http.Request) accounts.Who {
	who, _ := r.Context().Value(whoKey{}).(accounts.Who)
	return who
}

func presentedToken(r *http.Request) string {
	if t := r.Header.Get("X-Auth-Token"); t != "" {
		return strings.TrimSpace(t)
	}
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// signInRequired reports whether every request must be signed in (always, outside tests).
func (s *Server) signInRequired() bool { return s.cfg.Accounts != nil || s.cfg.AuthToken != "" }

// maxFailClients is how many clients' failures are kept at most.
const maxFailClients = 1024

// failKeys is what the client's failures count against: each of its addresses, an IPv6 one as its
// /64. One machine is handed a whole /64, and could otherwise take a fresh address every few guesses.
func (s *Server) failKeys(r *http.Request) []string {
	var keys []string
	for _, ip := range s.clientIPs(r) {
		if k := netKey(ip); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// netKey is an IPv4 address as it is, and an IPv6 one as its /64 ("2001:db8::/64").
func netKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() != nil {
		return addr
	}
	mask := net.CIDRMask(64, 128)
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}

// throttled reports whether the client has failed too often of late, under any of its addresses.
func (s *Server) throttled(r *http.Request) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.throttledLocked(s.failKeys(r))
}

func (s *Server) throttledLocked(ips []string) bool {
	for _, ip := range ips {
		if len(s.recentLocked(ip)) >= authFailLimit {
			return true
		}
	}
	return false
}

// recentLocked is ip's failures within the window; older ones are forgotten.
func (s *Server) recentLocked(ip string) []time.Time {
	var recent []time.Time
	for _, t := range s.failures[ip] {
		if time.Since(t) < authFailWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(s.failures, ip)
	} else {
		s.failures[ip] = recent
	}
	return recent
}

// recordFailure counts a failure against each of the client's addresses.
func (s *Server) recordFailure(r *http.Request) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now := time.Now()
	for _, ip := range s.failKeys(r) {
		s.addFailureLocked(ip, now)
	}
}

func (s *Server) addFailureLocked(ip string, at time.Time) {
	if _, ok := s.failures[ip]; !ok && len(s.failures) >= maxFailClients {
		s.evictLocked()
	}
	s.failures[ip] = append(s.failures[ip], at)
}

// evictLocked makes room for one more client: clients whose failures have all expired go first, and
// failing that the one whose last failure is oldest, so the clients failing right now are kept.
func (s *Server) evictLocked() {
	var oldest string
	var oldestAt time.Time
	for ip, list := range s.failures {
		last := list[len(list)-1]
		if time.Since(last) >= authFailWindow {
			delete(s.failures, ip)
			continue
		}
		if oldest == "" || last.Before(oldestAt) {
			oldest, oldestAt = ip, last
		}
	}
	if len(s.failures) >= maxFailClients {
		delete(s.failures, oldest)
	}
}

// The limits above are per client address, which a client on this machine (or a page rebound to
// it) names itself in X-Real-IP or X-Forwarded-For, taking a new one for every few guesses. So wrong
// passwords, codes and setup codes also count against the account they were for, whatever address
// they came from: past accountFreeTries the account's sign-ins are held back, for accountFirstHold
// at first and twice as long after each further wrong try, up to accountMaxHold. That is at most
// about a hundred guesses a day, and never a lockout that lasts: once the guessing stops, the
// account opens again. A browser that has signed in to the account before (its device cookie says
// so) counts against limits of its own instead, so whoever runs up the account's cannot keep its
// owner out.
const (
	accountFreeTries = 20
	accountFirstHold = 30 * time.Second
	accountMaxHold   = 15 * time.Minute
	// accountForget is how long after the last wrong try an account's count starts again.
	accountForget = 24 * time.Hour
	// maxHeldKeys is how many accounts' (and browsers') counts are kept, a made-up name each at most.
	maxHeldKeys = 4096
)

// heldBack is the wrong tries counted against one account or browser.
type heldBack struct {
	n     int       // wrong tries, one after another
	last  time.Time // the last of them
	until time.Time // tries are held back until then
}

// setupAccount stands for the setup code in the per-account limits.
const setupAccount = "\x00setup"

// limitKey is what a try at account counts against in the per-account limits: the account, or the
// browser itself when its device cookie shows it signed in to that account before. label names it in
// the log.
func (s *Server) limitKey(r *http.Request, account string) (key, label string) {
	if account == setupAccount {
		return "setup", "the setup code"
	}
	name := strings.ToLower(strings.TrimSpace(account))
	// Longer is no account's name; this keeps a made-up one from taking much room.
	if len(name) > 64 {
		name = name[:64]
	}
	if s.cfg.Accounts != nil {
		if c, err := r.Cookie(deviceCookieName); err == nil {
			if id, ok := s.cfg.Accounts.Device(c.Value, name); ok {
				return "device:" + id, name + " (from a browser that signed in before)"
			}
		}
	}
	return "account:" + name, name
}

// heldLocked is how much longer tries counting against key are held back (none when not positive).
func (s *Server) heldLocked(key string, now time.Time) time.Duration {
	f := s.held[key]
	if f == nil {
		return 0
	}
	if now.Sub(f.last) > accountForget {
		delete(s.held, key)
		return 0
	}
	return f.until.Sub(now)
}

// holdBackLocked counts a wrong try against key, holding its tries back once there are too many.
func (s *Server) holdBackLocked(key, label string, now time.Time) {
	f := s.held[key]
	if f == nil || now.Sub(f.last) > accountForget {
		if f == nil && len(s.held) >= maxHeldKeys {
			s.evictHeldLocked(now)
		}
		f = &heldBack{}
		s.held[key] = f
	}
	f.n++
	f.last = now
	if over := f.n - accountFreeTries; over >= 0 {
		hold := min(accountMaxHold, accountFirstHold<<min(over, 16))
		f.until = now.Add(hold)
		log.Warn("Too many wrong sign-in tries for one account: its sign-ins are held back", "account", label, "tries", f.n, "for", hold.String())
	}
}

// evictHeldLocked makes room for one more count: those long forgotten go first, then the one whose
// last wrong try is oldest. One held back now is kept, so a flood of made-up names cannot set it free.
func (s *Server) evictHeldLocked(now time.Time) {
	var oldest string
	var oldestAt time.Time
	for k, f := range s.held {
		if now.Sub(f.last) > accountForget {
			delete(s.held, k)
			continue
		}
		if f.until.After(now) {
			continue
		}
		if oldest == "" || f.last.Before(oldestAt) {
			oldest, oldestAt = k, f.last
		}
	}
	if len(s.held) >= maxHeldKeys && oldest != "" {
		delete(s.held, oldest)
	}
}

// heldBackReply answers a try at an account whose sign-ins are held back.
func heldBackReply(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	in := fmt.Sprintf("%d seconds", secs)
	if secs > 90 {
		in = fmt.Sprintf("%d minutes", (secs+59)/60)
	}
	writeJSON(w, http.StatusTooManyRequests, errBody("Too many wrong tries for this account. Try again in "+in+"."))
}

// tryResult is how a try that passwordTry let in went.
type tryResult int

const (
	notAGuess  tryResult = iota // nothing was guessed (the password was right, and a code is yet to come)
	wrongGuess                  // a wrong password, code or setup code
	rightGuess                  // the password, code or setup code was right
)

// How many password checks (600,000 rounds of PBKDF2 each) may run at once, and how long a sign-in
// waits for its turn. Without a cap, a flood of sign-ins would keep every core busy hashing.
const (
	maxHashing = 2
	hashWait   = 5 * time.Second
)

// passwordTry starts a try at account's password (or a code for it, or the setup code: setupAccount),
// which may be a guess: it is counted against the client's limit before the slow check, so tries
// sent all at once cannot all pass the limit before the first failure is counted, and it waits for a
// hashing slot. It answers and is false when the client or the account is held back, or too many
// checks are running. Otherwise finish ends the try: a wrong guess stays counted against the client
// and counts against the account; anything else is taken back, and a right guess clears the account's
// count.
func (s *Server) passwordTry(w http.ResponseWriter, r *http.Request, account string) (finish func(tryResult), ok bool) {
	ips := s.failKeys(r)
	key, label := s.limitKey(r, account)
	at := time.Now()
	s.failMu.Lock()
	if s.throttledLocked(ips) {
		s.failMu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, errBody("Too many tries. Wait a minute and try again."))
		return nil, false
	}
	if wait := s.heldLocked(key, at); wait > 0 {
		s.failMu.Unlock()
		heldBackReply(w, wait)
		return nil, false
	}
	for _, ip := range ips {
		s.addFailureLocked(ip, at)
	}
	s.failMu.Unlock()
	takeBack := func() {
		s.failMu.Lock()
		defer s.failMu.Unlock()
		for _, ip := range ips {
			if i := slices.Index(s.failures[ip], at); i >= 0 {
				s.failures[ip] = slices.Delete(s.failures[ip], i, i+1)
			}
			if len(s.failures[ip]) == 0 {
				delete(s.failures, ip)
			}
		}
	}
	wait := time.NewTimer(hashWait)
	defer wait.Stop()
	select {
	case s.hashing <- struct{}{}:
	case <-wait.C:
		takeBack()
		writeJSON(w, http.StatusTooManyRequests, errBody("Too many sign-ins at once. Try again in a moment."))
		return nil, false
	case <-r.Context().Done():
		takeBack()
		writeJSON(w, http.StatusServiceUnavailable, errBody("The request was cancelled"))
		return nil, false
	}
	// Tries sent together all passed the account's check above before any of them was found wrong.
	// Looked at again holding a slot, only the few running already go on once it is held back.
	s.failMu.Lock()
	held := s.heldLocked(key, time.Now())
	s.failMu.Unlock()
	if held > 0 {
		<-s.hashing
		takeBack()
		heldBackReply(w, held)
		return nil, false
	}
	return func(res tryResult) {
		// Counted before the slot is given up, so the next try waiting for it sees the count.
		switch res {
		case wrongGuess:
			s.failMu.Lock()
			s.holdBackLocked(key, label, time.Now())
			s.failMu.Unlock()
		case rightGuess:
			s.failMu.Lock()
			delete(s.held, key)
			s.failMu.Unlock()
		}
		<-s.hashing
		if res != wrongGuess {
			takeBack()
		}
	}, true
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, who := s.access(r)
		switch a {
		case allowed:
			next(w, r.WithContext(context.WithValue(r.Context(), whoKey{}, who)))
		case lockedOut:
			// The token is not looked at, so a client that is guessing learns nothing while locked out.
			writeJSON(w, http.StatusTooManyRequests, errBody("Too Many Requests"))
		default:
			// Only a wrong token counts towards the limit. A session cookie cannot be guessed, and a
			// stale one (signed out elsewhere, or expired) comes with every request a page makes at
			// once, which would lock its owner out of signing in again; it is cleared instead.
			if sessionCookie(r) != "" {
				s.setCookie(w, r, "", -1)
			}
			writeJSON(w, http.StatusUnauthorized, errBody("Unauthorized"))
		}
	})
}

// admin is auth for what only the admin may do or see: every change, and what holds secrets
// (settings, backups, SSH keys, the agent). A viewer is turned away with 403.
func (s *Server) admin(next http.HandlerFunc) http.Handler {
	return s.auth(adminOnly(next))
}

func adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !whoFrom(r).Admin() {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "You are signed in as a viewer: you can look, but not change anything", "read_only": true})
			return
		}
		next(w, r)
	}
}

// statusRefused answers a request Nightjar carried out but that something it talks to turned down or
// did not answer: Plex, qBittorrent, systemd, a server over SSH. Not a 5xx: a proxy in front (Cloudflare)
// swaps those for its own page, and the reason in the body would never reach the dashboard.
const statusRefused = http.StatusUnprocessableEntity

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// config is what the page needs before it draws. Signed out, that is only enough to sign in: what
// runs on the machine, its version and the admin's name are left out.
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	a, who := s.access(r)
	signedIn := a == allowed
	out := map[string]any{
		"auth_required":       s.signInRequired(),
		"authorized":          signedIn,
		"account":             s.account(signedIn, who),
		"status_page_enabled": s.ctx.Config().StatusPageEnabled,
		"status_page_title":   s.ctx.Config().StatusPageTitle,
		"status_page_domain":  s.ctx.Config().StatusPageDomain,
		"status_page_path":    s.ctx.Config().StatusPagePath,
		"needs_setup":         false,
		"hidden":              []string{},
		"appearance":          s.publicAppearance(signedIn),
	}
	if signedIn {
		out["plex_enabled"], out["qbittorrent_enabled"] = s.ctx.Plex != nil, s.ctx.QBittorrentBoard != nil
		out["needs_setup"] = s.needsSetup()
		out["version"] = s.cfg.Version
		out["hidden"] = append([]string{}, s.ctx.Config().HiddenSections...)
		// Weekly maintenance and quiet hours run on the server's clock, so the page names it.
		name, offset := serverZone(time.Now())
		out["time_zone"] = map[string]any{"name": name, "offset": offset}
	}
	writeJSON(w, http.StatusOK, out)
}

// serverZone is the server's time zone and its offset now (see collect.TimeZone).
func serverZone(now time.Time) (string, int) { return collect.TimeZone(now) }

type check struct {
	Status         string   `json:"status"`
	UsagePercent   *float64 `json:"usage_percent,omitempty"`
	AvailableGB    *float64 `json:"available_gb,omitempty"`
	FreeGB         *float64 `json:"free_gb,omitempty"`
	UptimeHours    *float64 `json:"uptime_hours,omitempty"`
	CPUTempCelsius *float64 `json:"cpu_temp_celsius,omitempty"`
	Error          string   `json:"error,omitempty"`
}

func round(v float64, places int) *float64 {
	scale := math.Pow10(places)
	r := math.Round(v*scale) / scale
	return &r
}

// health is public so uptime probes work; without credentials it returns the overall status only.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	var issues []string
	checks := map[string]check{}
	degrade := func(issue string) {
		issues = append(issues, issue)
		if status == "ok" {
			status = "degraded"
		}
	}
	// The loop's latest reading: measuring here would reset the CPU baseline the loop measures from,
	// and anyone can call this as often as they like.
	if latest := s.ctx.Latest(); latest != nil {
		pct := latest.CPUPercent
		checks["cpu"] = check{Status: "ok", UsagePercent: round(pct, 1)}
		if pct > 95 {
			degrade("CPU usage very high")
		}
	}
	if m, err := collect.VirtualMemory(); err == nil {
		checks["memory"] = check{Status: "ok", UsagePercent: round(m.Percent, 1), AvailableGB: round(float64(m.Available)/(1<<30), 2)}
		if m.Percent > 95 {
			degrade("Memory usage very high")
		}
		if m.Available < 100<<20 {
			degrade("Low memory available (<100MB)")
		}
	} else {
		checks["memory"] = check{Status: "error", Error: "check failed"}
		issues, status = append(issues, "Memory monitor failed"), "error"
	}
	disk := collect.TotalDisk(collect.Partitions(true))
	free := float64(disk.Free) / (1 << 30)
	checks["disk"] = check{Status: "ok", UsagePercent: round(disk.Percent, 1), FreeGB: round(free, 2)}
	if disk.Percent > 95 {
		degrade("Disk usage very high (>95%)")
	}
	if disk.Total > 0 && free < 1 {
		degrade("Low disk space (<1GB free)")
	}
	checks["system"] = check{Status: "ok", UptimeHours: round(float64(collect.UptimeSeconds())/3600, 1)}
	if s.ctx.Sensors.Available() {
		c := check{Status: "ok"}
		if t := s.ctx.Sensors.CPUTemperature(); t != nil {
			c.CPUTempCelsius = round(*t, 1)
			if *t > 90 {
				degrade("CPU temperature very high (>90°C)")
			}
		}
		checks["temperature"] = c
	} else {
		checks["temperature"] = check{Status: "unavailable"}
	}

	code := map[string]int{"ok": http.StatusOK, "degraded": http.StatusServiceUnavailable, "error": http.StatusInternalServerError}[status]
	if !s.authorized(r) {
		writeJSON(w, code, map[string]string{"status": status})
		return
	}
	body := map[string]any{"status": status, "timestamp": nowSeconds(), "timestamp_iso": time.Now().Format(time.RFC3339), "checks": checks}
	if len(issues) > 0 {
		body["issues"] = issues
	}
	writeJSON(w, code, body)
}

// Stats is the dashboard payload.
type Stats struct {
	Timestamp   float64                 `json:"timestamp"`
	System      collect.System          `json:"system"`
	CPU         CPUStats                `json:"cpu"`
	Memory      collect.Memory          `json:"memory"`
	Swap        collect.Memory          `json:"swap"`
	Disk        DiskStats               `json:"disk"`
	GPU         *collect.GPU            `json:"gpu"`
	Temperature *float64                `json:"temperature"`
	Fans        []collect.Fan           `json:"fans"`
	Network     NetworkStats            `json:"network"`
	Processes   collect.ProcessOverview `json:"processes"`
	History     []monitor.Sample        `json:"history"`
}

type CPUStats struct {
	Percent float64         `json:"percent"`
	PerCore []float64       `json:"per_core"`
	Cores   int             `json:"cores"`
	Freq    collect.CPUFreq `json:"freq"`
	Load    []float64       `json:"load"`
}

type DiskStats struct {
	Total      collect.DiskTotal   `json:"total"`
	Partitions []collect.Partition `json:"partitions"`
}

type NetworkStats struct {
	Bandwidth  *collect.Bandwidth  `json:"bandwidth"`
	Interfaces []collect.Interface `json:"interfaces"`
}

// collectStats builds a fresh payload, reusing the loop's latest snapshot for the slow parts.
func (s *Server) collectStats() *Stats {
	st := &Stats{Timestamp: nowSeconds(), System: collect.SystemInfo(), Fans: []collect.Fan{}}
	st.CPU.Cores = collect.CPUCount()
	st.CPU.Freq = collect.Freq()
	st.CPU.Load = collect.LoadAverage()
	if latest := s.ctx.Latest(); latest != nil {
		st.CPU.Percent, st.CPU.PerCore = latest.CPUPercent, latest.PerCore
		st.Memory, st.Swap = latest.Memory, latest.Swap
		st.Disk = DiskStats{Total: latest.TotalDisk, Partitions: latest.Partitions}
		st.GPU, st.Temperature = latest.GPU, latest.CPUTemp
		st.Network.Bandwidth = latest.Bandwidth
	} else {
		st.Memory, _ = collect.VirtualMemory()
		st.Swap, _ = collect.Swap()
		parts := collect.Partitions(true)
		st.Disk = DiskStats{Total: collect.TotalDisk(parts), Partitions: parts}
		st.Temperature = s.ctx.Sensors.CPUTemperature()
	}
	if st.Disk.Partitions == nil {
		st.Disk.Partitions = []collect.Partition{}
	}
	if fans := s.ctx.Sensors.Fans(); fans != nil {
		st.Fans = fans
	}
	st.Network.Interfaces = collect.Interfaces()
	st.Processes = s.ctx.Processes.Overview(5)
	// The dashboard shows names and percentages; command lines (kept for alerts) would be most of the payload.
	// The lists share their entries with the scanner's cache, which the loop's alerts read, so they are
	// copied before being trimmed.
	st.Processes.TopCPU, st.Processes.TopMemory = slices.Clone(st.Processes.TopCPU), slices.Clone(st.Processes.TopMemory)
	for _, list := range [][]collect.Process{st.Processes.TopCPU, st.Processes.TopMemory} {
		for i := range list {
			list[i].Cmdline, list[i].RSS, list[i].VMS = nil, 0, 0
		}
	}
	st.History = s.ctx.History()
	if st.History == nil {
		st.History = []monitor.Sample{}
	}
	return st
}

func (s *Server) serveStats(w http.ResponseWriter, r *http.Request) {
	s.statsMu.Lock()
	if s.stats == nil || time.Since(s.statsAt) >= s.statsTTL {
		s.stats, s.statsAt = s.collectStats(), time.Now()
	}
	st := *s.stats
	s.statsMu.Unlock()
	// ?since=<ts> sends only newer history samples: the dashboard keeps the rest, so each poll
	// carries one new sample instead of the last half hour.
	if since, err := strconv.ParseFloat(r.URL.Query().Get("since"), 64); err == nil {
		i := sort.Search(len(st.History), func(i int) bool { return st.History[i].TS > since })
		st.History = st.History[i:]
	}
	writeJSON(w, http.StatusOK, &st)
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Services == nil {
		writeJSON(w, http.StatusOK, map[string]any{"services": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": s.ctx.Services.Summaries(nowSeconds())})
}

func (s *Server) serviceHistory(w http.ResponseWriter, r *http.Request) {
	m := s.ctx.Services
	name := r.URL.Query().Get("name")
	if m == nil || !m.Has(name) {
		writeJSON(w, http.StatusNotFound, errBody("Unknown service"))
		return
	}
	hours := 24.0
	if raw := r.URL.Query().Get("hours"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("hours must be a number"))
			return
		}
		// NaN and the infinities parse, but cannot be written back as JSON once the answer has begun.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			writeJSON(w, http.StatusBadRequest, errBody("hours must be a number"))
			return
		}
		hours = min(2160, max(1, v))
	}
	now := nowSeconds()
	points := m.History(name, now-hours*3600, now, historyPoints)
	if points == nil {
		points = []storage.Point{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "hours": hours, "points": points})
}

func (s *Server) plex(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Plex == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	summary := r.URL.Query().Get("view") == "summary"
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "plex": s.ctx.Plex.Snapshot(summary)})
}

// needsSetup is true until the monitor has somewhere to send alerts and something to check.
func (s *Server) needsSetup() bool {
	return len(s.ctx.Channels) == 0 || s.ctx.Services == nil || len(s.ctx.Services.Specs()) == 0
}

// qbittorrent serves what the monitoring loop last saw in qBittorrent.
func (s *Server) qbittorrent(w http.ResponseWriter, r *http.Request) {
	if s.ctx.QBittorrentBoard == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "qbittorrent": s.ctx.QBittorrentBoard.View()})
}

// frontend serves the SPA, falling back to index.html for client-side routes. It reads
// FrontendDir from disk when set (handy while iterating on the UI) and the embedded build otherwise.
func (s *Server) frontend() http.Handler {
	var files fs.FS
	if s.cfg.FrontendDir != "" {
		if _, err := os.Stat(filepath.Join(s.cfg.FrontendDir, "index.html")); err == nil {
			files = os.DirFS(s.cfg.FrontendDir)
			log.Info("Serving frontend from disk", "dir", s.cfg.FrontendDir)
		}
	}
	if files == nil && web.Built() {
		files = web.FS()
	}
	if files == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Frontend not built. Run `make` (or `npm run build` in frontend/) and rebuild the backend.", http.StatusNotFound)
		})
	}
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if info, err := fs.Stat(files, name); name != "" && err == nil && info.Mode().IsRegular() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		// The status page, wherever it is, says so in its HTML (see markStatusPage).
		if s.isStatusPage(r) {
			if index, err := fs.ReadFile(files, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.Method != http.MethodHead {
					w.Write(markStatusPage(index))
				}
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
