// Package services runs uptime checks (HTTP(S), TCP, ping, TLS expiry) and tracks each service's status.
package services

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var CheckTypes = []string{"http", "tcp", "ping", "tls", "plugin", "heartbeat", "docker"}

// PluginCheck runs a plugin's check, whose target is the plugin's id and, after a colon, its
// argument. Nightjar sets it when it loads plugins.
var PluginCheck func(s Spec, id, argument string) Result

// DockerCheck judges a container check, whose target is the container's name. Nightjar sets it
// when it connects to Docker.
var DockerCheck func(s Spec) Result

var containerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

var pluginRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}(:.*)?$`)

// PluginTarget splits a plugin check's target into the plugin and its argument.
func PluginTarget(target string) (id, argument string) {
	id, argument, _ = strings.Cut(target, ":")
	return id, strings.TrimSpace(argument)
}

const minInterval = 5.0

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-:]*[A-Za-z0-9])?$`)

// Spec is one configured service check.
type Spec struct {
	Name           string  `json:"name"`
	Type           string  `json:"type"`
	Target         string  `json:"target"`
	Interval       float64 `json:"interval"`
	Timeout        float64 `json:"timeout"`
	Retries        int     `json:"retries"`
	ExpectedStatus []int   `json:"expected_status"`
	Keyword        string  `json:"keyword"`
	VerifyTLS      bool    `json:"verify_tls"`
	TLSWarnDays    int     `json:"tls_warn_days"`
	Paused         bool    `json:"paused"`
	// RetryInterval is how soon a failed check runs again while it is not yet down.
	RetryInterval float64 `json:"retry_interval"`
	// PingCount echoes are sent PingInterval seconds apart on each ping check.
	PingCount    int     `json:"ping_count"`
	PingInterval float64 `json:"ping_interval"`
	// A heartbeat's schedule (cron, @daily, or "every 1h"), how late a ping may be, and how long
	// a started run may take (0 for no limit). Its target is the token in its ping URL.
	Schedule   string  `json:"schedule,omitempty"`
	Grace      float64 `json:"grace,omitempty"`
	MaxRuntime float64 `json:"max_runtime,omitempty"`
	// Repair is what Nightjar does to bring the check back when it goes down.
	Repair *Repair `json:"repair,omitempty"`
	// Server is the id of the watched server the checked thing runs on; empty for none.
	Server string `json:"server,omitempty"`
}

// Repair restarts a systemd service or a Docker container, or runs a script from the repairs
// folder, when its check goes down: at most Max times an outage, Wait seconds apart.
type Repair struct {
	Action    string  `json:"action"` // restart, container or script
	Unit      string  `json:"unit,omitempty"`
	Container string  `json:"container,omitempty"`
	Script    string  `json:"script,omitempty"`
	Max       int     `json:"max"`
	Wait      float64 `json:"wait"`
}

var (
	unitRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]*$`)
	scriptRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	serverRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func (r *Repair) validate(name string) error {
	r.Unit, r.Script, r.Container = strings.TrimSpace(r.Unit), strings.TrimSpace(r.Script), strings.TrimSpace(r.Container)
	switch r.Action {
	case "container":
		if !containerRE.MatchString(r.Container) {
			return fmt.Errorf("%s: a container repair needs the container to restart", name)
		}
		r.Unit, r.Script = "", ""
	case "restart":
		if !unitRE.MatchString(r.Unit) {
			return fmt.Errorf("%s: a restart repair needs the system service to restart", name)
		}
		if !strings.Contains(r.Unit, ".") {
			r.Unit += ".service"
		}
		r.Script, r.Container = "", ""
	case "script":
		if !scriptRE.MatchString(r.Script) {
			return fmt.Errorf("%s: a script repair needs a script's file name from the repairs folder", name)
		}
		r.Unit, r.Container = "", ""
	default:
		return fmt.Errorf("%s: a repair's action must be restart, container or script", name)
	}
	r.Max = min(5, max(1, cmp.Or(r.Max, 2)))
	r.Wait = min(3600, max(30, cmp.Or(r.Wait, 120)))
	return nil
}

// Result is the outcome of one check attempt.
type Result struct {
	Up        bool     `json:"up"`
	LatencyMS *float64 `json:"latency_ms"`
	Detail    string   `json:"detail"`
}

func up(start time.Time, detail string) Result {
	ms := float64(time.Since(start).Microseconds()) / 1000
	return Result{Up: true, LatencyMS: &ms, Detail: detail}
}

func down(detail string) Result { return Result{Detail: detail} }

func downAfter(start time.Time, detail string) Result {
	ms := float64(time.Since(start).Microseconds()) / 1000
	return Result{LatencyMS: &ms, Detail: detail}
}

func (s Spec) timeout() time.Duration { return time.Duration(s.Timeout * float64(time.Second)) }

func describeNetErr(err error, timeout float64) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("timed out after %gs", timeout)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return "connect failed: " + opErr.Err.Error()
	}
	return "connect failed: " + err.Error()
}

func splitHostPort(target string, defaultPort int) (string, string, error) {
	host, port, err := net.SplitHostPort(target)
	if err == nil {
		return host, port, nil
	}
	if defaultPort == 0 {
		return "", "", fmt.Errorf("target needs a port: %s", target)
	}
	return strings.Trim(target, "[]"), strconv.Itoa(defaultPort), nil
}

// maxBody is how much of a response is read, and searched for the keyword.
const maxBody = 4 << 20

const userAgent = "Nightjar uptime check"

// CheckHTTP is up when the status is expected (default < 400) and the keyword, if any, is present.
// Every attempt opens a new connection and asks caches to revalidate, so DNS, connect, TLS and the
// origin itself are tested each time rather than a pooled connection or a cached page.
func CheckHTTP(s Spec) Result {
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: !s.VerifyTLS},
		ForceAttemptHTTP2: true,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: s.timeout(), Transport: transport}
	// A check that expects a redirect must see it, not the page it leads to.
	if slices.ContainsFunc(s.ExpectedStatus, func(c int) bool { return c >= 300 && c < 400 }) {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	req, err := http.NewRequest(http.MethodGet, s.Target, nil)
	if err != nil {
		return down("invalid URL: " + err.Error())
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return down(describeHTTPErr(err, s.Timeout))
	}
	defer resp.Body.Close()
	// A body that stalls or breaks off is a failing server, even after a good status.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return downAfter(start, fmt.Sprintf("HTTP %d, but the response broke off: %s", resp.StatusCode, describeHTTPErr(err, s.Timeout)))
	}
	r := up(start, fmt.Sprintf("HTTP %d", resp.StatusCode))

	ok := resp.StatusCode < 400
	if len(s.ExpectedStatus) > 0 {
		ok = slices.Contains(s.ExpectedStatus, resp.StatusCode)
	}
	if !ok {
		r.Up = false
		if n := len(s.ExpectedStatus); n > 0 && n <= 5 {
			codes := make([]string, n)
			for i, c := range s.ExpectedStatus {
				codes[i] = strconv.Itoa(c)
			}
			r.Detail += ", expected " + strings.Join(codes, " or ")
		}
		return r
	}
	if s.Keyword != "" && !bytes.Contains(body, []byte(s.Keyword)) {
		r.Up, r.Detail = false, fmt.Sprintf("keyword %q not found", s.Keyword)
		if len(body) == maxBody {
			r.Detail += fmt.Sprintf(" in the first %d MB", maxBody>>20)
		}
	}
	return r
}

// describeHTTPErr says why a request failed, without the URL the dashboard already shows.
func describeHTTPErr(err error, timeout float64) string {
	var certErr *tls.CertificateVerificationError
	var ne net.Error
	var opErr *net.OpError
	switch {
	case errors.As(err, &certErr):
		return "TLS error: " + certErr.Err.Error()
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Sprintf("timed out after %gs", timeout)
	case errors.As(err, &opErr):
		return describeNetErr(err, timeout)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return "request failed: " + err.Error()
}

// CheckTCP opens a connection to host:port.
func CheckTCP(s Spec) Result {
	host, port, err := splitHostPort(s.Target, 0)
	if err != nil {
		return down(err.Error())
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), s.timeout())
	if err != nil {
		return down(describeNetErr(err, s.Timeout))
	}
	conn.Close()
	return up(start, "connected")
}

// A ping check sends PingCount echoes and is up when any is answered, so one dropped packet is
// not an outage. Below 0.2s apart ping needs root.
const (
	defaultPingCount    = 3
	maxPingCount        = 10
	defaultPingInterval = 0.2
	minPingInterval     = 0.2
	maxPingInterval     = 5.0
)

// pings is the spec's echo count and spacing, with defaults for a spec built in code.
func (s Spec) pings() (int, float64) {
	count, gap := s.PingCount, s.PingInterval
	if count <= 0 {
		count = defaultPingCount
	}
	if gap <= 0 {
		gap = defaultPingInterval
	}
	return min(count, maxPingCount), min(maxPingInterval, max(minPingInterval, gap))
}

// PingDuration is the longest a ping check can take: sending every echo, then the timeout for the last.
func (s Spec) PingDuration() float64 {
	count, gap := s.pings()
	return float64(count-1)*gap + s.Timeout
}

var (
	// iputils: "3 packets transmitted, 2 received"; BusyBox: "3 packets transmitted, 2 packets received".
	pingCountRE = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
	// The summary's average: "rtt min/avg/max/mdev = 0.1/0.2/0.3/0.0 ms".
	pingAvgRE  = regexp.MustCompile(`= [\d.]+/([\d.]+)/`)
	pingTimeRE = regexp.MustCompile(`time[=<]([\d.]+)`)
	// ping names itself as it was run: "ping: " or "/usr/bin/ping: ".
	pingPrefixRE = regexp.MustCompile(`^\S*ping: `)
)

// CheckPing sends a few ICMP echoes with the system ping. Latency is their average round trip.
func CheckPing(s Spec) Result {
	ping, err := exec.LookPath("ping")
	if err != nil {
		return down("ping command not available")
	}
	if !hostRE.MatchString(s.Target) {
		return down("invalid host")
	}
	count, gap := s.pings()
	total := s.PingDuration()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration((total+2)*float64(time.Second)))
	defer cancel()
	// -w bounds the whole attempt; -n skips reverse lookups, which add their own delay.
	deadline := strconv.Itoa(max(1, int(math.Ceil(total))))
	cmd := exec.CommandContext(ctx, ping, "-n", "-c", strconv.Itoa(count), "-i", strconv.FormatFloat(gap, 'f', -1, 64), "-w", deadline, s.Target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return down(fmt.Sprintf("timed out after %gs", total))
	}
	m := pingCountRE.FindSubmatch(out)
	if m == nil {
		// No summary: trust the exit status, and say what ping said when it failed.
		if err != nil {
			return down(pingFailure(stderr.String(), total))
		}
		return Result{Up: true, LatencyMS: pingRTT(out), Detail: "reply received"}
	}
	sent, received := atoi(m[1]), atoi(m[2])
	if received == 0 {
		return down(pingFailure(stderr.String(), total))
	}
	detail := "reply received"
	if received < sent {
		detail = fmt.Sprintf("%d of %d replies received", received, sent)
	}
	return Result{Up: true, LatencyMS: pingRTT(out), Detail: detail}
}

// pingFailure is ping's own complaint (an unknown host, no permission), or "no reply".
func pingFailure(stderr string, timeout float64) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(pingPrefixRE.ReplaceAllString(line, "")); line != "" {
			return line
		}
	}
	return fmt.Sprintf("no reply within %gs", timeout)
}

// pingRTT is the average round trip in ms from the summary, else from the replies, else nil.
func pingRTT(out []byte) *float64 {
	if m := pingAvgRE.FindSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(string(m[1]), 64); err == nil {
			return &v
		}
	}
	var sum float64
	var n int
	for _, m := range pingTimeRE.FindAllSubmatch(out, -1) {
		if v, err := strconv.ParseFloat(string(m[1]), 64); err == nil {
			sum += v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	avg := sum / float64(n)
	return &avg
}

func atoi(b []byte) int {
	n, _ := strconv.Atoi(string(b))
	return n
}

// CheckTLS is down when the certificate is invalid or any certificate in its chain expires within
// TLSWarnDays: an intermediate that runs out breaks the site as surely as the server's own.
func CheckTLS(s Spec) Result {
	host, port, err := splitHostPort(tlsAddress(s.Target), 443)
	if err != nil {
		return down(err.Error())
	}
	start := time.Now()
	dialer := &net.Dialer{Timeout: s.timeout()}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host})
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return down("TLS error: " + certErr.Err.Error())
		}
		return down(describeNetErr(err, s.Timeout))
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return downAfter(start, "no certificate presented")
	}
	first := firstExpiry(state)
	what := "certificate"
	if !first.Equal(state.PeerCertificates[0]) {
		what = fmt.Sprintf("chain certificate %q", certName(first))
	}
	days := int(time.Until(first.NotAfter).Hours() / 24)
	if days < s.TLSWarnDays {
		return downAfter(start, fmt.Sprintf("%s expires in %d day(s)", what, days))
	}
	if what != "certificate" {
		return up(start, fmt.Sprintf("certificate valid for %d day(s), until %s expires", days, what))
	}
	return up(start, fmt.Sprintf("certificate valid for %d day(s)", days))
}

// firstExpiry is the certificate that expires first in the verified chain that lasts longest
// (a client can switch to another chain when one runs out), or among those the server sent.
func firstExpiry(state tls.ConnectionState) *x509.Certificate {
	earliest := func(chain []*x509.Certificate) *x509.Certificate {
		return slices.MinFunc(chain, func(a, b *x509.Certificate) int { return a.NotAfter.Compare(b.NotAfter) })
	}
	if len(state.VerifiedChains) == 0 {
		return earliest(state.PeerCertificates)
	}
	var best *x509.Certificate
	for _, chain := range state.VerifiedChains {
		if c := earliest(chain); best == nil || c.NotAfter.After(best.NotAfter) {
			best = c
		}
	}
	return best
}

func certName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	return c.Subject.String()
}

// tlsAddress accepts a URL as well as host[:port], since that is what people often paste.
func tlsAddress(target string) string {
	if strings.Contains(target, "://") {
		if u, err := url.Parse(target); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return target
}

// Run performs one attempt; a panic in a check counts as a failure.
func Run(s Spec) (r Result) {
	defer func() {
		if p := recover(); p != nil {
			r = down(fmt.Sprintf("check error: %v", p))
		}
	}()
	switch s.Type {
	case "http":
		return CheckHTTP(s)
	case "tcp":
		return CheckTCP(s)
	case "ping":
		return CheckPing(s)
	case "tls":
		return CheckTLS(s)
	case "plugin":
		if PluginCheck == nil {
			return down("plugins are not loaded")
		}
		id, arg := PluginTarget(s.Target)
		return PluginCheck(s, id, arg)
	case "docker":
		if DockerCheck == nil {
			return down("Docker is not connected")
		}
		return DockerCheck(s)
	case "heartbeat":
		// A heartbeat is judged by the pings it gets (Monitor.Heartbeat), not by reaching out.
		return down("a heartbeat has nothing to try: save it, then ping its URL from the job")
	}
	return down("unknown check type")
}

type rawSpec struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Target         string   `json:"target"`
	Interval       *float64 `json:"interval"`
	Timeout        *float64 `json:"timeout"`
	Retries        *int     `json:"retries"`
	ExpectedStatus []int    `json:"expected_status"`
	Keyword        string   `json:"keyword"`
	VerifyTLS      *bool    `json:"verify_tls"`
	TLSWarnDays    *int     `json:"tls_warn_days"`
	Paused         bool     `json:"paused"`
	RetryInterval  *float64 `json:"retry_interval"`
	PingCount      *int     `json:"ping_count"`
	PingInterval   *float64 `json:"ping_interval"`
	Schedule       string   `json:"schedule"`
	Grace          *float64 `json:"grace"`
	MaxRuntime     *float64 `json:"max_runtime"`
	Repair         *Repair  `json:"repair"`
	Server         string   `json:"server"`
}

func orDefault[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func (r rawSpec) spec() (Spec, error) {
	name, target := strings.TrimSpace(r.Name), strings.TrimSpace(r.Target)
	kind := strings.ToLower(orDefault(&r.Type, "http"))
	if kind == "" {
		kind = "http"
	}
	if kind == "heartbeat" && target == "" {
		target = newToken()
	}
	if name == "" || target == "" {
		return Spec{}, errors.New("'name' and 'target' are required")
	}
	switch kind {
	case "http":
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			return Spec{}, fmt.Errorf("%s: http target must start with http:// or https://", name)
		}
	case "ping":
		if !hostRE.MatchString(target) {
			return Spec{}, fmt.Errorf("%s: invalid ping host", name)
		}
	case "plugin":
		if !pluginRE.MatchString(target) {
			return Spec{}, fmt.Errorf("%s: a plugin check's target is the plugin's id, then optionally a colon and its argument", name)
		}
	case "docker":
		if !containerRE.MatchString(target) {
			return Spec{}, fmt.Errorf("%s: a container check's target is the container's name", name)
		}
	case "heartbeat":
		if !tokenRE.MatchString(target) {
			return Spec{}, fmt.Errorf("%s: a heartbeat's token is 16 to 64 letters, digits, - or _", name)
		}
		if _, err := ParseSchedule(r.Schedule); err != nil {
			return Spec{}, fmt.Errorf("%s: %v", name, err)
		}
	case "tcp", "tls":
	default:
		return Spec{}, fmt.Errorf("%s: type must be one of %s", name, strings.Join(CheckTypes, ", "))
	}
	if r.Repair != nil {
		if err := r.Repair.validate(name); err != nil {
			return Spec{}, err
		}
	}
	server := strings.TrimSpace(r.Server)
	if server != "" && !serverRE.MatchString(server) {
		return Spec{}, fmt.Errorf("%s: %q is not a server's id", name, server)
	}
	interval := max(minInterval, orDefault(r.Interval, 60))
	if kind == "heartbeat" {
		return Spec{
			Name: name, Type: kind, Target: target, Paused: r.Paused, Repair: r.Repair, Server: server,
			// Its pings are looked at every half minute, and the first miss is the outage.
			Interval: heartbeatInterval, Timeout: 10, Retries: 0, RetryInterval: heartbeatInterval, VerifyTLS: true,
			Schedule:   strings.TrimSpace(r.Schedule),
			Grace:      min(7*86400, max(60, orDefault(r.Grace, 600))),
			MaxRuntime: min(7*86400, max(0, orDefault(r.MaxRuntime, 0))),
			PingCount:  defaultPingCount, PingInterval: defaultPingInterval,
		}, nil
	}
	return Spec{
		Name:           name,
		Type:           kind,
		Target:         target,
		Interval:       interval,
		Timeout:        min(60, max(1, orDefault(r.Timeout, 10))),
		Retries:        max(0, orDefault(r.Retries, 2)),
		ExpectedStatus: r.ExpectedStatus,
		Keyword:        r.Keyword,
		VerifyTLS:      orDefault(r.VerifyTLS, true),
		TLSWarnDays:    max(0, orDefault(r.TLSWarnDays, 14)),
		Paused:         r.Paused,
		RetryInterval:  min(interval, max(minInterval, orDefault(r.RetryInterval, defaultRetryInterval))),
		PingCount:      min(maxPingCount, max(1, orDefault(r.PingCount, defaultPingCount))),
		PingInterval:   min(maxPingInterval, max(minPingInterval, orDefault(r.PingInterval, defaultPingInterval))),
		Repair:         r.Repair,
		Server:         server,
	}, nil
}

// ParseSpec decodes and validates one check definition, filling in defaults.
func ParseSpec(data []byte) (Spec, error) {
	var raw rawSpec
	if err := json.Unmarshal(data, &raw); err != nil {
		return Spec{}, err
	}
	return raw.spec()
}

// Parse decodes a list (or {"services": [...]}) of service definitions, skipping invalid and duplicate entries.
func Parse(data []byte, warn func(string)) ([]Spec, error) {
	var wrapped struct {
		Services []json.RawMessage `json:"services"`
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		if err := json.Unmarshal(data, &wrapped); err != nil {
			return nil, err
		}
		items = wrapped.Services
	}
	var specs []Spec
	seen := map[string]bool{}
	for i, item := range items {
		spec, err := ParseSpec(item)
		if err != nil {
			warn(fmt.Sprintf("skipping service #%d: %v", i+1, err))
			continue
		}
		if seen[spec.Name] {
			warn("skipping duplicate service name: " + spec.Name)
			continue
		}
		seen[spec.Name] = true
		specs = append(specs, spec)
	}
	return specs, nil
}

// Load reads service definitions from a JSON file.
func Load(path string, warn func(string)) []Spec {
	data, err := os.ReadFile(path)
	if err != nil {
		warn(fmt.Sprintf("services file not readable (%s): %v", path, err))
		return nil
	}
	specs, err := Parse(data, warn)
	if err != nil {
		warn(fmt.Sprintf("could not parse services file %s: %v", path, err))
	}
	return specs
}
