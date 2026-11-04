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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/collect"
	"nightjar/internal/logx"
	"nightjar/internal/monitor"
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
	Version       string
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
	failures map[string][]time.Time

	statsMu  sync.Mutex
	stats    *Stats
	statsAt  time.Time
	statsTTL time.Duration

	days dayCache // the status page's days per check

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
	s := &Server{cfg: cfg, ctx: ctx, failures: map[string][]time.Time{}, statsTTL: 2 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("POST /api/auth/setup", fromDashboard(s.createAdmin))
	mux.HandleFunc("POST /api/auth/login", fromDashboard(s.login))
	mux.HandleFunc("POST /api/auth/logout", fromDashboard(s.logout))
	mux.Handle("POST /api/auth/password", s.writable(s.changePassword))
	mux.Handle("POST /api/auth/2fa/begin", s.writable(s.beginTwoFactor))
	mux.Handle("POST /api/auth/2fa/enable", s.writable(s.enableTwoFactor))
	mux.Handle("POST /api/auth/2fa/disable", s.writable(s.disableTwoFactor))
	mux.Handle("GET /api/activity", s.auth(s.activity))
	mux.Handle("GET /api/backup", s.auth(s.makeBackup))
	// A backup can be larger than other changes, so it skips their size limit for its own.
	mux.Handle("POST /api/backup/restore", s.auth(s.logged(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, errBody("Cross-origin changes are not allowed"))
				return
			}
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
	mux.Handle("POST /api/cleanup/scan", s.writable(s.cleanupScan))
	mux.Handle("PUT /api/cleanup/{key}/keep", s.writable(s.cleanupKeep))
	mux.Handle("DELETE /api/cleanup/{key}/keep", s.writable(s.cleanupKeep))
	mux.Handle("POST /api/report/send", s.writable(s.sendReport))
	mux.Handle("GET /api/appearance", s.auth(s.getAppearance))
	mux.Handle("PUT /api/appearance", s.writable(s.saveAppearance))
	mux.Handle("GET /api/statuspage", s.auth(s.statusPageDesign))
	mux.Handle("PUT /api/statuspage", s.writable(s.saveStatusPageDesign))
	mux.Handle("GET /api/stats", s.auth(s.serveStats))
	mux.Handle("GET /api/logs", s.auth(s.logs))
	mux.Handle("GET /api/apps", s.auth(s.needApps(s.listApps)))
	mux.Handle("GET /api/apps/job", s.auth(s.needApps(s.appJob)))
	mux.Handle("POST /api/apps/{id}/install", s.writable(s.needApps(s.installApp)))
	mux.Handle("POST /api/apps/{id}/remove", s.writable(s.needApps(s.removeApp)))
	mux.Handle("GET /api/plugins", s.auth(s.needPlugins(s.listPlugins)))
	mux.Handle("GET /api/plugins/cards", s.auth(s.needPlugins(s.pluginCards)))
	mux.Handle("POST /api/plugins/rescan", s.writable(s.needPlugins(s.rescanPlugins)))
	mux.Handle("PUT /api/plugins/{id}", s.writable(s.needPlugins(s.configurePlugin)))
	mux.Handle("POST /api/plugins/{id}/try", s.writable(s.needPlugins(s.tryPlugin)))
	mux.Handle("GET /api/notifications", s.auth(s.notifications))
	mux.Handle("GET /api/notifications/summary", s.auth(s.notificationSummary))
	mux.Handle("POST /api/notifications/read", s.writable(s.markNotifications))
	mux.Handle("GET /api/notifications/rules", s.auth(s.notificationRules))
	mux.Handle("PUT /api/notifications/rules", s.writable(s.saveNotificationRules))
	mux.Handle("POST /api/notifications/mute", s.writable(s.mute))
	mux.Handle("GET /api/hosts", s.auth(s.needRemote(s.listHosts)))
	mux.Handle("GET /api/hosts/{id}", s.auth(s.needRemote(s.getHost)))
	mux.Handle("POST /api/hosts/token", s.writable(s.needRemote(s.newHostToken)))
	mux.Handle("GET /api/agent/download", s.auth(s.downloadAgent))
	mux.Handle("GET /api/analytics", s.auth(s.analyticsAll))
	mux.Handle("GET /api/analytics/{id}", s.auth(s.analyticsHost))
	mux.Handle("POST /api/hosts", s.writable(s.needRemote(s.saveHost)))
	mux.Handle("PUT /api/hosts/{id}", s.writable(s.needRemote(s.saveHost)))
	mux.Handle("DELETE /api/hosts/{id}", s.writable(s.needRemote(s.deleteHost)))
	mux.Handle("PUT /api/hosts/{id}/ssh", s.writable(s.needRemote(s.setHostSSH)))
	mux.Handle("DELETE /api/hosts/{id}/ssh", s.writable(s.needRemote(s.setHostSSH)))
	mux.Handle("POST /api/hosts/{id}/ssh/{action}", s.writable(s.needRemote(s.runHostSSH)))
	mux.Handle("GET /api/ssh/keys", s.auth(s.needRemote(s.sshKeys)))
	mux.Handle("POST /api/ssh/keys", s.writable(s.needRemote(s.newSSHKey)))
	mux.Handle("POST /api/ssh/authorize", s.writable(s.needRemote(s.authorizeSSHKey)))
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
	mux.Handle("POST /api/checks/{name}/repair", s.writable(s.repairCheck))
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
	mux.Handle("GET /api/settings", s.auth(s.settings))
	mux.Handle("PUT /api/settings", s.writable(s.saveSettings))
	mux.Handle("POST /api/settings/test", s.writable(s.testSettings))
	mux.Handle("GET /api/setup", s.auth(s.setupStatus))
	mux.Handle("POST /api/alerts/test", s.writable(s.testAlerts))
	mux.Handle("GET /api/units", s.auth(s.listUnits))
	mux.Handle("GET /api/units/{name}/logs", s.auth(s.unitLogs))
	mux.Handle("POST /api/units/{name}/{action}", s.writable(s.controlUnit))
	mux.Handle("GET /api/drives", s.auth(s.drives))
	mux.Handle("GET /api/network", s.auth(s.network))
	mux.Handle("GET /api/updates", s.auth(s.updates))
	mux.Handle("POST /api/updates", s.writable(s.needUpdates(s.saveUpdateTarget)))
	mux.Handle("POST /api/updates/check", s.writable(s.needUpdates(s.checkUpdates)))
	mux.Handle("POST /api/updates/detect", s.writable(s.needUpdates(s.detectUpdates)))
	mux.Handle("PUT /api/updates/{id}", s.writable(s.needUpdates(s.saveUpdateTarget)))
	mux.Handle("DELETE /api/updates/{id}", s.writable(s.needUpdates(s.deleteUpdateTarget)))
	mux.Handle("POST /api/updates/{id}/check", s.writable(s.needUpdates(s.checkUpdates)))
	mux.Handle("POST /api/updates/{id}/update", s.writable(s.needUpdates(s.applyUpdate)))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusNotFound, errBody("Not Found")) })
	mux.Handle("/", s.frontend())
	s.http = &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           compress(s.headers(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
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
		h := w.Header()
		if r.URL.Path == "/status" || r.URL.Path == "/status-page" {
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

// clientIP is who is asking. Behind a reverse proxy on this machine (nginx on most media boxes)
// every request comes from loopback, so the proxy's X-Real-IP or X-Forwarded-For names the client;
// from anywhere else those headers could be forged and are ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// authorized accepts a signed-in browser's session cookie, or WEB_AUTH_TOKEN as
// "Authorization: Bearer <token>" or "X-Auth-Token".
func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.AuthToken != "" && subtle.ConstantTimeCompare([]byte(presentedToken(r)), []byte(s.cfg.AuthToken)) == 1 {
		return true
	}
	if s.cfg.Accounts != nil {
		return s.cfg.Accounts.Valid(sessionCookie(r))
	}
	return s.cfg.AuthToken == ""
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

func (s *Server) throttled(ip string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
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
	return len(recent) >= authFailLimit
}

func (s *Server) recordFailure(ip string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if len(s.failures) > 1024 {
		for k := range s.failures {
			delete(s.failures, k)
			break
		}
	}
	s.failures[ip] = append(s.failures[ip], time.Now())
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		// Checked before the credentials, so a client that is guessing learns nothing while locked out.
		if s.throttled(ip) {
			writeJSON(w, http.StatusTooManyRequests, errBody("Too Many Requests"))
			return
		}
		if !s.authorized(r) {
			// Only a wrong token counts towards the limit. A session cookie cannot be guessed, and a
			// stale one (signed out elsewhere, or expired) comes with every request a page makes at
			// once, which would lock its owner out of signing in again; it is cleared instead.
			if presentedToken(r) != "" {
				s.recordFailure(ip)
				log.Warn("Unauthorized request", "path", r.URL.Path, "ip", ip)
			}
			if sessionCookie(r) != "" {
				s.setCookie(w, r, "", -1)
			}
			writeJSON(w, http.StatusUnauthorized, errBody("Unauthorized"))
			return
		}
		next(w, r)
	})
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
	signedIn := s.authorized(r)
	out := map[string]any{
		"auth_required":       s.signInRequired(),
		"authorized":          signedIn,
		"account":             s.account(signedIn),
		"status_page_enabled": s.ctx.Config().StatusPageEnabled,
		"status_page_title":   s.ctx.Config().StatusPageTitle,
		"needs_setup":         false,
		"hidden":              []string{},
		"appearance":          s.publicAppearance(r),
	}
	if signedIn {
		out["plex_enabled"], out["qbittorrent_enabled"] = s.ctx.Plex != nil, s.ctx.QBittorrentBoard != nil
		out["needs_setup"] = s.needsSetup()
		out["version"] = s.cfg.Version
		out["hidden"] = append([]string{}, s.ctx.Config().HiddenSections...)
	}
	writeJSON(w, http.StatusOK, out)
}

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
	if per, err := collect.PerCore(); err == nil && len(per) > 0 {
		sum := 0.0
		for _, v := range per {
			sum += v
		}
		pct := sum / float64(len(per))
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
	sys := collect.SystemInfo()
	checks["system"] = check{Status: "ok", UptimeHours: round(float64(sys.UptimeSeconds)/3600, 1)}
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
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
