// Package remote watches other servers through the agent running on each, polling its readings (CPU,
// memory, temperatures, disks, drives). It keeps an hour of history for the dashboard and alerts when a
// server goes offline, runs hot, fills a disk or has a failing drive. Whether something merely answers
// (a router's page, a port, a ping) is an uptime check's job, in package services.
package remote

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/analytics"
	"nightjar/internal/discord"
	"nightjar/internal/logx"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

var log = logx.Component("Servers")

const hostsKey = "remote.hosts"

// Kinds of host. Only agents are watched now; KindCheck is how a server watched with no agent was
// saved before those became uptime checks, kept so it can be moved (see LegacyChecks).
const (
	KindAgent = "agent"
	KindCheck = "check"
)

// Host is one watched server as saved. Token is kept here and never sent to the browser.
type Host struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is KindAgent (also when empty, as saved before there were kinds), or KindCheck on a
	// server saved before servers without an agent became uptime checks.
	Kind string `json:"kind,omitempty"`
	// Address is the agent's base URL (https://192.168.0.20:9101).
	Address string `json:"address"`
	Token   string `json:"token,omitempty"`
	Check   *Check `json:"check,omitempty"`
	// SSH is how to log in to it, when set up.
	SSH *SSH `json:"ssh,omitempty"`
	// SSHCheck is how the last login with it went.
	SSHCheck *SSHCheck `json:"ssh_check,omitempty"`
	Added    float64   `json:"added"`
	// Alerts are the problems already announced, so a restart does not announce them again.
	Alerts []string `json:"alerts,omitempty"`
}

// Check is how a server without an agent was watched, before those became uptime checks.
type Check struct {
	// Type is "http" (any answer under 500 counts, so a sign-in page does), "tcp" (the port accepts
	// a connection) or "ping".
	Type   string `json:"type"`
	Target string `json:"target"` // https://nas.local, nas.local:22 or nas.local
	// SelfSigned accepts a certificate no authority signed; left out, certificates are verified.
	SelfSigned bool `json:"self_signed,omitempty"`
}

// Point is one poll's headline numbers, for the history charts; nil while the host was offline,
// so the charts show a gap.
type Point struct {
	T    float64  `json:"t"`
	CPU  *float64 `json:"cpu"`
	Mem  *float64 `json:"mem"`
	Temp *float64 `json:"temp"`
}

func (h Host) isCheck() bool { return h.Kind == KindCheck && h.Check != nil }

// State is a host as the dashboard shows it.
type State struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Added   float64 `json:"added"`
	Kind    string  `json:"kind"`
	// SSH is the saved login, and SSHCommand the command to log in the same way by hand.
	SSH        *SSH      `json:"ssh,omitempty"`
	SSHCommand string    `json:"ssh_command,omitempty"`
	SSHCheck   *SSHCheck `json:"ssh_check,omitempty"`
	// Update is how keeping its agent on this dashboard's build is going.
	Update *AgentUpdate `json:"update,omitempty"`
	// Status is "waiting" before the first answer, then "online" or "offline".
	Status   string          `json:"status"`
	LastSeen float64         `json:"last_seen,omitempty"`
	Error    string          `json:"error,omitempty"`
	Readings *agent.Readings `json:"readings"`
	History  []Point         `json:"history"`
	// Problems are the alerts in force now, in words.
	Problems []string `json:"problems"`
}

// Limits are the thresholds alerts use; the monitor's own CPU temperature, memory and disk settings.
type Limits struct{ Temp, Memory, Disk float64 }

// Options configure a Monitor; zero values suit a real machine.
type Options struct {
	Store  *storage.Store
	Alerts discord.Sender // nil sends none
	Limits func() Limits
	// Interval is how often each host is polled.
	Interval time.Duration
	HTTP     *http.Client
	Now      func() time.Time
	// Analytics records each poll for the analytics page; nil records nothing.
	Analytics *analytics.Recorder
	// SSHDir is Nightjar's own folder for SSH keys and the host keys it has seen; empty uses only
	// the monitor user's ~/.ssh and makes no keys.
	SSHDir string
	// AutoUpdate says whether agents are kept on the dashboard's build; nil means always.
	AutoUpdate func() bool
	// AgentBuild is the agent build for an architecture with its SHA-256; nil uses the builds inside
	// the dashboard.
	AgentBuild func(arch string) ([]byte, string, error)
	// Exec runs ssh and ssh-keygen, with stdin and env when not nil; nil runs the real ones.
	Exec func(ctx context.Context, stdin []byte, env []string, name string, args ...string) (stdout, stderr string, err error)
	// AskPass is the program ssh asks for a password (the nightjar program, by default).
	AskPass string
}

// Monitor polls every host. Its methods are safe for concurrent use.
type Monitor struct {
	opt Options

	mu    sync.Mutex
	hosts []*entry
	// legacy are servers saved as watched with no agent, waiting to be moved to uptime checks.
	legacy []Host
	// outbox delivers alerts one at a time, in order, off the polls' lock.
	outbox chan func()

	// clients are the HTTPS clients pinned to each token's key, kept for their connections.
	clientsMu sync.Mutex
	clients   map[string]*http.Client
}

type entry struct {
	host     Host
	status   string
	fails    int
	lastSeen time.Time
	err      string
	readings *agent.Readings
	// update is how keeping its agent on the dashboard's build is going; updateTried is when it was
	// last sent one, and updating whether it is being sent one now.
	update      AgentUpdate
	updateTried time.Time
	updating    bool
	history     []Point
	// over counts consecutive polls past a limit, by alert key, so one spike does not alert.
	over map[string]int
	// problems are the alerts in force: key → words.
	problems map[string]string
}

// History is how many points each host keeps: an hour at the default interval.
const History = 360

// offlineAfter is how many failed polls in a row make a host offline.
const offlineAfter = 3

func New(opt Options) *Monitor {
	opt.Interval = cmp.Or(opt.Interval, 10*time.Second)
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Limits == nil {
		opt.Limits = func() Limits { return Limits{Temp: 80, Memory: 90, Disk: 90} }
	}
	m := &Monitor{opt: opt, outbox: make(chan func(), 256), clients: map[string]*http.Client{}}
	go func() {
		for send := range m.outbox {
			send()
		}
	}()
	var saved []Host
	if raw := opt.Store.Meta(hostsKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			log.Warn("Saved servers unreadable", "err", err)
		}
	}
	for _, h := range saved {
		if h.isCheck() {
			m.legacy = append(m.legacy, h)
			continue
		}
		h.Kind = cmp.Or(h.Kind, KindAgent)
		m.hosts = append(m.hosts, newEntry(h))
	}
	return m
}

// MoveLegacyChecks turns servers saved as watched with no agent into the uptime checks they always
// were: same name, target and answer, their stored minutes copied into the check's history. Each is
// moved once, at the first start that finds it, and then forgotten here.
func (m *Monitor) MoveLegacyChecks(checks *services.Monitor) {
	m.mu.Lock()
	legacy := slices.Clone(m.legacy)
	m.mu.Unlock()
	for _, h := range legacy {
		name := h.Name
		for i := 2; slices.ContainsFunc(checks.Specs(), func(s services.Spec) bool { return s.Name == name }); i++ {
			name = fmt.Sprintf("%s %d", h.Name, i)
		}
		raw := map[string]any{"name": name, "type": h.Check.Type, "target": h.Check.Target, "verify_tls": !h.Check.SelfSigned}
		if h.Check.Type == "http" {
			// It counted any answer under 500 as up, sign-in and missing pages included.
			raw["expected_status"] = []int{200, 401, 403, 404}
		}
		data, _ := json.Marshal(raw)
		spec, err := services.ParseSpec(data)
		if err != nil {
			log.Warn("Could not move a server watched with no agent", "name", h.Name, "err", err)
			continue
		}
		// History first, so the check starts with it.
		if err := m.opt.Store.RecordSamples(spec.Name, m.opt.Store.SampleBuckets(h.ID, 0, 60)); err != nil {
			log.Warn("Could not copy a server's history to its check", "name", h.Name, "err", err)
		}
		if err := checks.Save("", spec); err != nil {
			log.Warn("Could not move a server watched with no agent", "name", h.Name, "err", err)
			continue
		}
		m.mu.Lock()
		m.legacy = slices.DeleteFunc(m.legacy, func(l Host) bool { return l.ID == h.ID })
		err = m.saveLocked()
		m.mu.Unlock()
		m.opt.Analytics.Forget(h.ID)
		m.opt.Store.DeleteSamples(h.ID)
		if err != nil {
			log.Warn("Moved server still listed", "name", h.Name, "err", err)
		}
		log.Info("Server watched with no agent is now an uptime check", "name", spec.Name, "type", spec.Type, "target", spec.Target)
	}
}

func newEntry(h Host) *entry {
	e := &entry{host: h, status: "waiting", over: map[string]int{}, problems: map[string]string{}}
	for _, k := range h.Alerts {
		e.problems[k] = ""
	}
	return e
}

// ---------- the list ----------

// List reports every host with its latest readings and the last `points` of history.
func (m *Monitor) List(points int) []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.hosts))
	for _, e := range m.hosts {
		out = append(out, e.state(points))
	}
	return out
}

// Get reports one host with its whole history.
func (m *Monitor) Get(id string) (State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.find(id); e != nil {
		return e.state(History), true
	}
	return State{}, false
}

func (e *entry) state(points int) State {
	s := State{ID: e.host.ID, Name: e.host.Name, Address: e.host.Address, Added: e.host.Added, Kind: e.host.Kind, SSH: e.host.SSH, SSHCommand: SSHCommand(e.host), SSHCheck: e.host.SSHCheck, Status: e.status, Error: e.err,
		Readings: e.readings, History: slices.Clone(e.history[max(0, len(e.history)-points):]), Problems: []string{}}
	if s.History == nil {
		s.History = []Point{}
	}
	if e.update.State != "" {
		u := e.update
		s.Update = &u
	}
	if !e.lastSeen.IsZero() {
		s.LastSeen = float64(e.lastSeen.Unix())
	}
	keys := make([]string, 0, len(e.problems))
	for k := range e.problems {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if w := e.problems[k]; w != "" {
			s.Problems = append(s.Problems, w)
		}
	}
	return s
}

func (e *entry) addPoint(p Point) {
	e.history = append(e.history, p)
	if len(e.history) > History {
		e.history = slices.Delete(e.history, 0, len(e.history)-History)
	}
}

func (m *Monitor) find(id string) *entry {
	for _, e := range m.hosts {
		if e.host.ID == id {
			return e
		}
	}
	return nil
}

var (
	ErrNotFound = errors.New("no such server")
	ErrExists   = errors.New("that server is already added")
)

// Add checks that the agent answers with token, then saves the host and returns it.
func (m *Monitor) Add(ctx context.Context, name, address, token string) (State, error) {
	address, err := NormalizeAddress(address)
	if err != nil {
		return State{}, err
	}
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	if token == "" {
		return State{}, errors.New("enter the token the agent was installed with")
	}
	m.mu.Lock()
	for _, e := range m.hosts {
		if e.host.Address == address {
			m.mu.Unlock()
			return State{}, ErrExists
		}
	}
	m.mu.Unlock()
	r, err := m.fetch(ctx, address, token)
	if err != nil {
		m.forgetUnusedClient(token)
		return State{}, err
	}
	h := Host{ID: newID(), Name: cmp.Or(name, r.System.Hostname, address), Kind: KindAgent, Address: address, Token: token, Added: float64(m.opt.Now().Unix())}
	if err := validName(h.Name); err != nil {
		m.forgetUnusedClient(token)
		return State{}, err
	}
	e := newEntry(h)
	m.mu.Lock()
	m.hosts = append(m.hosts, e)
	m.recordLocked(e, r, nil)
	err = m.saveLocked()
	st := e.state(History)
	m.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	log.Info("Server added", "name", h.Name, "address", address)
	return st, nil
}

// Update renames a host or points it elsewhere; an empty token keeps the saved one. A new address
// or token is checked against the agent first.
func (m *Monitor) Update(ctx context.Context, id, name, address, token string) (State, error) {
	address, err := NormalizeAddress(address)
	if err != nil {
		return State{}, err
	}
	name, token = strings.TrimSpace(name), strings.TrimSpace(token)
	if err := validName(name); err != nil {
		return State{}, err
	}
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return State{}, ErrNotFound
	}
	if e.host.isSSHOnly() {
		m.mu.Unlock()
		return State{}, errors.New("this server has no agent; change its address under SSH")
	}
	for _, o := range m.hosts {
		if o != e && o.host.Address == address {
			m.mu.Unlock()
			return State{}, ErrExists
		}
	}
	token = cmp.Or(token, e.host.Token)
	moved := address != e.host.Address || token != e.host.Token
	m.mu.Unlock()
	var r *agent.Readings
	if moved {
		if r, err = m.fetch(ctx, address, token); err != nil {
			m.forgetUnusedClient(token)
			return State{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.find(id) != e {
		m.forgetUnusedClientLocked(token)
		return State{}, ErrNotFound
	}
	if token != e.host.Token {
		m.forgetClient(e.host.Token)
	}
	if e.host.SSH != nil && e.host.SSH.Host == "" && address != e.host.Address {
		// Its SSH login follows the address, to a server it has not tried yet.
		e.host.SSHCheck = nil
	}
	e.host.Name, e.host.Address, e.host.Token = name, address, token
	if r != nil {
		m.recordLocked(e, r, nil)
	}
	if err := m.saveLocked(); err != nil {
		return State{}, err
	}
	return e.state(History), nil
}

// Remove stops watching a host.
func (m *Monitor) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.hosts, func(e *entry) bool { return e.host.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	log.Info("Server removed", "name", m.hosts[i].host.Name)
	m.forgetClient(m.hosts[i].host.Token)
	m.opt.Analytics.Forget(m.hosts[i].host.ID)
	m.hosts = slices.Delete(m.hosts, i, i+1)
	return m.saveLocked()
}

func (m *Monitor) saveLocked() error {
	hosts := make([]Host, 0, len(m.hosts))
	for _, e := range m.hosts {
		h := e.host
		h.Alerts = nil
		for k := range e.problems {
			h.Alerts = append(h.Alerts, k)
		}
		slices.Sort(h.Alerts)
		hosts = append(hosts, h)
	}
	hosts = append(hosts, m.legacy...)
	raw, _ := json.Marshal(hosts)
	return m.opt.Store.SetMeta(hostsKey, string(raw))
}

func validName(name string) error {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\n\r\t") {
		return errors.New("give the server a name of up to 64 characters")
	}
	return nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewToken makes a token for a new agent.
func NewToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NormalizeAddress turns what someone typed ("192.168.0.20", "nas:9101", "http://nas:9101/") into
// the agent's base URL, adding the default port and, without a scheme, HTTPS (how agents serve).
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("enter the server's address, e.g. 192.168.0.20")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
		return "", fmt.Errorf("%q is not a server address; enter a host name or IP, with :port if the agent uses another", strings.TrimSpace(s))
	}
	port := u.Port()
	if port == "" {
		port = strconv.Itoa(agent.DefaultPort)
	} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("%s is not a port", port)
	}
	return u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port), nil
}

// ---------- polling ----------

// Run polls every host each interval until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.opt.Interval)
	defer t.Stop()
	m.PollAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.PollAll(ctx)
		}
	}
}

// PollAll asks every host for its readings at once and waits for them.
func (m *Monitor) PollAll(ctx context.Context) {
	m.mu.Lock()
	hosts := slices.Clone(m.hosts)
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range hosts {
		m.mu.Lock()
		h := e.host
		m.mu.Unlock()
		wg.Go(func() {
			if h.isSSHOnly() {
				m.pollSSH(ctx, e, h)
				return
			}
			r, err := m.fetch(ctx, h.Address, h.Token)
			if ctx.Err() != nil {
				return // shutting down is not the host's fault
			}
			m.record(e, r, err)
		})
	}
	wg.Wait()
}

// fetch asks an agent for its readings.
func (m *Monitor) fetch(ctx context.Context, address, token string) (*agent.Readings, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+agent.Path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.client(address, token).Do(req)
	if err != nil {
		switch msg := err.Error(); {
		case errors.Is(err, agent.ErrWrongKey):
			return nil, fmt.Errorf("%s answered over HTTPS, but %v: is something else on that port?", address, agent.ErrWrongKey)
		case strings.Contains(msg, "HTTP response to HTTPS client"):
			return nil, fmt.Errorf("the agent at %s serves plain HTTP: use http:// in its address", strings.TrimPrefix(address, "https://"))
		}
		return nil, fmt.Errorf("no answer from %s: %s", address, plainError(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusBadRequest && strings.HasPrefix(address, "http://"):
		// Go's HTTPS server answers a plain request with 400 "Client sent an HTTP request to an HTTPS server".
		return nil, fmt.Errorf("the agent at %s serves HTTPS: use https:// in its address", strings.TrimPrefix(address, "http://"))
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("the agent turned the token down; it must match the one it was installed with")
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, errors.New("the agent is still taking its first reading; try again in a few seconds")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s answered HTTP %d; is that the agent's port?", address, resp.StatusCode)
	}
	var r agent.Readings
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("%s did not answer like an agent: %v", address, err)
	}
	if r.Version > agent.Version {
		return nil, fmt.Errorf("the agent (%s) is newer than this dashboard; update the dashboard", r.Agent)
	}
	return &r, nil
}

// client is how to reach an agent: plain HTTP as configured, or HTTPS that accepts only the key
// the token gives the real agent.
func (m *Monitor) client(address, token string) *http.Client {
	if !strings.HasPrefix(address, "https://") {
		return m.opt.HTTP
	}
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	if c := m.clients[token]; c != nil {
		return c
	}
	c := &http.Client{Timeout: m.opt.HTTP.Timeout, Transport: &http.Transport{TLSClientConfig: agent.ClientTLS(token), MaxIdleConnsPerHost: 1, IdleConnTimeout: time.Minute}}
	m.clients[token] = c
	return c
}

func (m *Monitor) forgetClient(token string) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	if c := m.clients[token]; c != nil {
		c.CloseIdleConnections()
		delete(m.clients, token)
	}
}

// forgetUnusedClient drops the client made for a token no saved host has, as after an Add or Update
// whose check failed; otherwise every mistyped token would keep one for good.
func (m *Monitor) forgetUnusedClient(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetUnusedClientLocked(token)
}

func (m *Monitor) forgetUnusedClientLocked(token string) {
	if !slices.ContainsFunc(m.hosts, func(e *entry) bool { return e.host.Token == token }) {
		m.forgetClient(token)
	}
}

// plainError drops Go's request framing ("Get \"http://…\": ") from a network error.
func plainError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused (is the agent running, on that port?)"
	case strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "i/o timeout") || errors.Is(err, context.DeadlineExceeded):
		return "timed out (a firewall is blocking the port: on the server, or on a cloud server in the provider's firewall too)"
	case strings.Contains(msg, "no such host"):
		return "no such host"
	}
	return msg
}

func (m *Monitor) record(e *entry, r *agent.Readings, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordLocked(e, r, err)
}

// recordLocked takes in one poll's outcome and raises or clears alerts.
func (m *Monitor) recordLocked(e *entry, r *agent.Readings, err error) {
	if !slices.Contains(m.hosts, e) {
		return // removed while its poll was out
	}
	now := m.opt.Now()
	m.opt.Analytics.Add(e.host.ID, agentReading(r, err))
	if err != nil {
		e.fails++
		e.err = err.Error()
		if e.fails >= offlineAfter {
			if e.status == "online" {
				e.addPoint(Point{T: float64(now.UnixMilli()) / 1000}) // the gap starts here
			}
			e.status = "offline"
			m.raise(e, "offline", discord.Critical, "Server offline: "+e.host.Name,
				fmt.Sprintf("%s has not answered for %s: %s", e.host.Name, (time.Duration(e.fails)*m.opt.Interval).Round(time.Second), e.err),
				"Offline: "+e.err)
		}
		return
	}
	e.fails, e.err, e.status, e.lastSeen, e.readings = 0, "", "online", now, r
	m.clear(e, "offline", "Server back online: "+e.host.Name, e.host.Name+" is answering again.")
	m.considerUpdateLocked(e, r)
	m.attackLocked(e, r.Attacks)
	e.addPoint(Point{T: r.Time, CPU: &r.CPU.Percent, Mem: &r.Memory.Percent, Temp: r.Temperature})

	lim := m.opt.Limits()
	// CPU temperature, two polls in a row over; clears 5° under.
	if t := r.Temperature; t != nil && lim.Temp > 0 {
		m.threshold(e, "temp", *t, lim.Temp, 5, 2, discord.Warning,
			fmt.Sprintf("%s: CPU at %.0f°C", e.host.Name, *t), fmt.Sprintf("The CPU is at %.0f°C, over the %.0f°C limit.", *t, lim.Temp),
			fmt.Sprintf("CPU at %.0f°C", *t),
			fmt.Sprintf("%s: CPU temperature back to %.0f°C", e.host.Name, *t))
	}
	if lim.Memory > 0 {
		mem := r.Memory.Percent
		m.threshold(e, "memory", mem, lim.Memory, 5, 3, discord.Warning,
			fmt.Sprintf("%s: memory at %.0f%%", e.host.Name, mem), fmt.Sprintf("Memory use is %.0f%%, over the %.0f%% limit.", mem, lim.Memory),
			fmt.Sprintf("Memory at %.0f%%", mem),
			fmt.Sprintf("%s: memory back to %.0f%%", e.host.Name, mem))
	}
	seen := map[string]bool{}
	if lim.Disk > 0 {
		for _, d := range r.Disks {
			key := "disk:" + d.Mountpoint
			seen[key] = true
			m.threshold(e, key, d.Percent, lim.Disk, 2, 1, discord.Warning,
				fmt.Sprintf("%s: %s is %.0f%% full", e.host.Name, d.Mountpoint, d.Percent),
				fmt.Sprintf("%s on %s is %.0f%% full, over the %.0f%% limit.", d.Mountpoint, e.host.Name, d.Percent, lim.Disk),
				fmt.Sprintf("%s %.0f%% full", d.Mountpoint, d.Percent),
				fmt.Sprintf("%s: %s back to %.0f%% full", e.host.Name, d.Mountpoint, d.Percent))
		}
	}
	for _, d := range r.Drives {
		key := "drive:" + d.Device
		seen[key] = true
		name := cmp.Or(d.Model, d.Device)
		switch d.Health {
		case "failing", "warning":
			sev := discord.Warning
			if d.Health == "failing" {
				sev = discord.Critical
			}
			state := "needs attention"
			if d.Health == "failing" {
				state = "is failing"
			}
			why := strings.Join(d.Problems, "; ")
			m.raise(e, key, sev, fmt.Sprintf("%s: drive %s %s", e.host.Name, d.Device, state), fmt.Sprintf("%s (%s): %s", name, d.Device, cmp.Or(why, d.Health)),
				fmt.Sprintf("Drive %s %s", d.Device, state))
		case "good":
			m.clear(e, key, fmt.Sprintf("%s: drive %s is healthy again", e.host.Name, d.Device), name+" passes its checks again.")
		}
	}
	// A disk or drive that disappeared from the readings has nothing left to alert about.
	for k := range e.problems {
		if (strings.HasPrefix(k, "disk:") || strings.HasPrefix(k, "drive:")) && !seen[k] {
			delete(e.problems, k)
			m.saveLocked()
		}
	}
}

// agentReading is one agent poll in analytics' terms; Disk is the fullest partition.
func agentReading(r *agent.Readings, err error) analytics.Reading {
	if err != nil || r == nil {
		return analytics.Reading{}
	}
	x := analytics.Reading{Up: true, CPU: &r.CPU.Percent, Mem: &r.Memory.Percent, Temp: r.Temperature, Rx: &r.Network.RecvPerSec, Tx: &r.Network.SentPerSec}
	for _, d := range r.Disks {
		if x.Disk == nil || d.Percent > *x.Disk {
			v := d.Percent
			x.Disk = &v
		}
	}
	return x
}

// threshold raises key after `polls` readings in a row at or over limit, and clears it once the
// value falls `margin` under.
func (m *Monitor) threshold(e *entry, key string, v, limit, margin float64, polls int, sev discord.Severity, title, desc, words, recovered string) {
	_, active := e.problems[key]
	switch {
	case v >= limit:
		e.over[key]++
		if e.over[key] >= polls {
			m.raise(e, key, sev, title, desc, words)
		}
	case v < limit-margin:
		e.over[key] = 0
		if active {
			m.clear(e, key, recovered, "")
		}
	default:
		e.over[key] = 0
		if active {
			e.problems[key] = words // still over the clearing line: keep it, with fresh numbers
		}
	}
}

// raise announces a problem once; while it lasts only its words are refreshed.
func (m *Monitor) raise(e *entry, key string, sev discord.Severity, title, desc, words string) {
	_, known := e.problems[key]
	e.problems[key] = words
	if known {
		return
	}
	m.saveLocked()
	log.Warn(title, "server", e.host.Name)
	m.send(e, title, desc, sev)
}

func (m *Monitor) clear(e *entry, key, title, desc string) {
	if _, ok := e.problems[key]; !ok {
		return
	}
	delete(e.problems, key)
	e.over[key] = 0
	m.saveLocked()
	log.Info(title, "server", e.host.Name)
	m.send(e, title, cmp.Or(desc, title+"."), discord.OK)
}

// send queues an alert: a slow channel must not hold up the polls, which hold the lock.
func (m *Monitor) send(e *entry, title, desc string, sev discord.Severity) {
	m.sendAs(e, title, desc, sev, "Servers")
}

// sendAs is send with the alert's category, which picks its area in the notification rules.
func (m *Monitor) sendAs(e *entry, title, desc string, sev discord.Severity, category string) {
	if m.opt.Alerts == nil {
		return
	}
	s := discord.About(m.opt.Alerts, "host:"+e.host.ID)
	fields := []discord.Field{{Name: "Server", Value: e.host.Name, Inline: true}, {Name: "Address", Value: e.host.Address, Inline: true}}
	select {
	case m.outbox <- func() { s.SendEmbed(title, desc, sev, fields, category) }:
	default:
		log.Warn("Too many alerts waiting to be sent; dropped one", "title", title)
	}
}
