// Package services runs uptime checks (HTTP(S), TCP, ping, TLS expiry) and tracks each service's status.
package services

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var CheckTypes = []string{"http", "tcp", "ping", "tls"}

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

// CheckHTTP is up when the status is expected (default < 400) and the keyword, if any, is present.
func CheckHTTP(s Spec) Result {
	client := &http.Client{
		Timeout:   s.timeout(),
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: !s.VerifyTLS}},
	}
	start := time.Now()
	resp, err := client.Get(s.Target)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		var ne net.Error
		switch {
		case errors.As(err, &certErr):
			return down("TLS error: " + certErr.Err.Error())
		case errors.As(err, &ne) && ne.Timeout():
			return down(fmt.Sprintf("timed out after %gs", s.Timeout))
		}
		return down("request failed: " + err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	r := up(start, fmt.Sprintf("HTTP %d", resp.StatusCode))

	ok := resp.StatusCode < 400
	if len(s.ExpectedStatus) > 0 {
		ok = false
		for _, c := range s.ExpectedStatus {
			ok = ok || c == resp.StatusCode
		}
	}
	if !ok {
		r.Up = false
		return r
	}
	if s.Keyword != "" && !strings.Contains(string(body), s.Keyword) {
		r.Up, r.Detail = false, fmt.Sprintf("keyword %q not found", s.Keyword)
	}
	return r
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

var pingTimeRE = regexp.MustCompile(`time[=<]([\d.]+)`)

// CheckPing sends one ICMP echo with the system ping.
func CheckPing(s Spec) Result {
	ping, err := exec.LookPath("ping")
	if err != nil {
		return down("ping command not available")
	}
	if !hostRE.MatchString(s.Target) {
		return down("invalid host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout()+2*time.Second)
	defer cancel()
	wait := strconv.Itoa(max(1, int(s.Timeout)))
	start := time.Now()
	out, err := exec.CommandContext(ctx, ping, "-c", "1", "-W", wait, s.Target).Output()
	if ctx.Err() != nil {
		return down(fmt.Sprintf("timed out after %gs", s.Timeout))
	}
	if err != nil {
		return down("no reply")
	}
	r := up(start, "reply received")
	if m := pingTimeRE.FindSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(string(m[1]), 64); err == nil {
			r.LatencyMS = &v
		}
	}
	return r
}

// CheckTLS is down when the certificate is invalid or expires within TLSWarnDays.
func CheckTLS(s Spec) Result {
	host, port, err := splitHostPort(s.Target, 443)
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
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return downAfter(start, "no certificate presented")
	}
	days := int(time.Until(certs[0].NotAfter).Hours() / 24)
	if days < s.TLSWarnDays {
		return downAfter(start, fmt.Sprintf("certificate expires in %d day(s)", days))
	}
	return up(start, fmt.Sprintf("certificate valid for %d day(s)", days))
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
	case "tcp", "tls":
	default:
		return Spec{}, fmt.Errorf("%s: type must be one of %s", name, strings.Join(CheckTypes, ", "))
	}
	return Spec{
		Name:           name,
		Type:           kind,
		Target:         target,
		Interval:       max(minInterval, orDefault(r.Interval, 60)),
		Timeout:        min(60, max(1, orDefault(r.Timeout, 10))),
		Retries:        max(0, orDefault(r.Retries, 2)),
		ExpectedStatus: r.ExpectedStatus,
		Keyword:        r.Keyword,
		VerifyTLS:      orDefault(r.VerifyTLS, true),
		TLSWarnDays:    max(0, orDefault(r.TLSWarnDays, 14)),
		Paused:         r.Paused,
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
