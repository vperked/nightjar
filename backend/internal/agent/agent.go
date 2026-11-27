// Package agent is the small program that runs on a server the dashboard watches from elsewhere.
// It samples the machine every few seconds and answers the dashboard's polls with the latest
// readings: CPU, memory, temperatures, fans, GPU, disks, drives, network and uptime. Over the HTTPS
// its token pins, it also lets the dashboard manage the machine's firewall, SSH logins, Tailscale and
// software updates, and replace the agent with the dashboard's own build.
package agent

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nightjar/internal/collect"
	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
	"nightjar/internal/smart"
	"nightjar/internal/sshd"
	"nightjar/internal/tailscale"
)

// Version is the wire format. The dashboard refuses readings from a newer one it cannot read.
const Version = 1

// DefaultPort is where an agent listens unless told otherwise.
const DefaultPort = 9101

// Path is where readings are served.
const Path = "/v1/readings"

// Readings are one sample of a machine.
type Readings struct {
	Version int    `json:"version"`
	Agent   string `json:"agent"` // the agent's build
	// Build is the SHA-256 of the agent's program, and CanUpdate whether it can replace it, for the
	// dashboard to keep it on its own build.
	Build     string  `json:"build,omitempty"`
	CanUpdate bool    `json:"can_update,omitempty"`
	Time      float64 `json:"time"` // when the sample was taken, unix seconds

	System collect.System `json:"system"`
	CPU    CPU            `json:"cpu"`
	Memory collect.Memory `json:"memory"`
	Swap   collect.Memory `json:"swap"`
	// Temperature is the CPU's, in °C; nil when the machine has no sensor for it.
	Temperature *float64            `json:"temperature"`
	Fans        []collect.Fan       `json:"fans"`
	GPU         *collect.GPU        `json:"gpu"`
	Disks       []collect.Partition `json:"disks"`
	Network     collect.Bandwidth   `json:"network"`
	DiskIO      DiskIO              `json:"disk_io"`
	// Drives are SMART readings, refreshed every few minutes; empty without smartmontools.
	Drives      []smart.Drive `json:"drives"`
	DrivesError string        `json:"drives_error,omitempty"`
	// DrivesPending is the first SMART read since the agent started not done yet: Drives is empty
	// for now, not because the drives are gone.
	DrivesPending bool `json:"drives_pending,omitempty"`
	// Attacks is the agent's own watch for denial-of-service attacks on this machine.
	Attacks *ddos.View `json:"attacks,omitempty"`
	// Locks are the locked ports another program holds now (see firewall.Lock), for the dashboard to
	// alert on.
	Locks []firewall.LockState `json:"locks,omitempty"`
}

type CPU struct {
	Percent float64   `json:"percent"`
	Cores   []float64 `json:"cores"`
	Count   int       `json:"count"`
	Load    []float64 `json:"load"`
	FreqMHz float64   `json:"freq_mhz"`
}

type DiskIO struct {
	ReadPerSec  float64 `json:"read_per_sec"`
	WritePerSec float64 `json:"write_per_sec"`
}

// ---------- sampling ----------

// Sampler keeps the machine's latest readings. Its methods are safe for concurrent use.
type Sampler struct {
	build     string
	sensors   *collect.Sensors
	gpu       *collect.GPUReader
	bandwidth *collect.BandwidthMeter
	drives    *smart.Reader
	// attacks watches this machine for denial-of-service attacks; nil when not running.
	attacks *ddos.Watcher
	// firewall keeps the rules set from the dashboard; nil when not running. port is the one the
	// agent listens on, which a rule must not close to the dashboard.
	firewall *firewall.Manager
	port     uint16
	// sshd manages who may log in over SSH; nil when not running.
	sshd *sshd.Manager
	// tailscale manages the machine's Tailscale; nil when not running.
	tailscale *tailscale.Manager
	// updates keeps the machine's software up to date; nil when not running.
	updates *Updates

	// dashboards are the addresses that asked for readings with the token, and when they last did:
	// never blocked while defending.
	dashboards map[string]time.Time

	mu      sync.Mutex
	latest  *Readings
	lastIO  collect.IO
	lastAt  time.Time
	drivesT time.Time
	// drivesBusy is SMART being read, in the background; drivesRead is a read having finished.
	drivesBusy, drivesRead bool
}

func NewSampler(build string) *Sampler {
	return &Sampler{build: build, sensors: collect.NewSensors(), gpu: &collect.GPUReader{}, bandwidth: &collect.BandwidthMeter{}, drives: smart.NewReader()}
}

// noteDashboard remembers an address that asked with the token.
func (s *Sampler) noteDashboard(remoteAddr string) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dashboards == nil {
		s.dashboards = map[string]time.Time{}
	}
	s.dashboards[host] = time.Now()
	for addr, at := range s.dashboards {
		if time.Since(at) > 24*time.Hour {
			delete(s.dashboards, addr)
		}
	}
}

// trusted are the addresses never to block: the dashboards of the last day, and AGENT_TRUSTED.
func (s *Sampler) trusted() []string {
	s.mu.Lock()
	out := make([]string, 0, len(s.dashboards))
	for addr := range s.dashboards {
		out = append(out, addr)
	}
	s.mu.Unlock()
	return append(out, strings.FieldsFunc(os.Getenv("AGENT_TRUSTED"), func(r rune) bool { return r == ',' || r == ' ' })...)
}

// Interval is how often the machine is sampled; CPU usage is averaged over it.
const Interval = 5 * time.Second

// drivesEvery spaces out SMART reads, which wake nothing (sleeping drives are skipped) but are slow.
const drivesEvery = 5 * time.Minute

// Run samples until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	collect.PerCore() // the first call only starts the measurement
	s.bandwidth.Read()
	t := time.NewTicker(Interval)
	defer t.Stop()
	s.Sample(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sample(ctx)
		}
	}
}

// Sample takes one reading now.
func (s *Sampler) Sample(ctx context.Context) {
	now := time.Now()
	r := &Readings{Version: Version, Agent: s.build, Time: float64(now.UnixMilli()) / 1000, System: collect.SystemInfo()}
	if cores, err := collect.PerCore(); err == nil {
		r.CPU.Cores = cores
		for _, c := range cores {
			r.CPU.Percent += c
		}
		if len(cores) > 0 {
			r.CPU.Percent /= float64(len(cores))
		}
	}
	r.CPU.Count, r.CPU.Load, r.CPU.FreqMHz = collect.CPUCount(), collect.LoadAverage(), collect.Freq().Current
	r.Memory, _ = collect.VirtualMemory()
	r.Swap, _ = collect.Swap()
	r.Temperature = s.sensors.CPUTemperature()
	r.Fans = s.sensors.Fans()
	if s.gpu.Available() {
		r.GPU = s.gpu.Read()
	}
	r.Disks = collect.Partitions(true)
	r.Network, _ = s.bandwidth.Read()

	if io, err := collect.DiskIO(); err == nil {
		s.mu.Lock()
		if secs := now.Sub(s.lastAt).Seconds(); !s.lastAt.IsZero() && secs > 0 && io.ReadBytes >= s.lastIO.ReadBytes && io.WriteBytes >= s.lastIO.WriteBytes {
			r.DiskIO = DiskIO{float64(io.ReadBytes-s.lastIO.ReadBytes) / secs, float64(io.WriteBytes-s.lastIO.WriteBytes) / secs}
		}
		s.lastIO, s.lastAt = io, now
		s.mu.Unlock()
	}

	s.mu.Lock()
	readDrives := now.Sub(s.drivesT) >= drivesEvery && !s.drivesBusy
	if readDrives {
		s.drivesT, s.drivesBusy = now, true
	}
	r.DrivesPending = !s.drivesRead
	s.mu.Unlock()
	if readDrives {
		// SMART can take up to a minute. Read on its own, it holds up no sample: the first one, after
		// every start and self-update, is served at once instead of the dashboard getting no answer
		// for that long and calling the server offline. The drives show from the sample after.
		go func() {
			dctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			s.drives.Read(dctx, now, smart.Limits{HDD: 50, SSD: 70}) // kept by the reader, read below
			s.mu.Lock()
			s.drivesBusy, s.drivesRead = false, true
			s.mu.Unlock()
		}()
	}
	r.Drives, r.DrivesError = s.drives.Drives()
	if s.attacks != nil {
		v := s.attacks.View()
		v.Methods = nil // the dashboard knows them
		v.History = v.History[:min(5, len(v.History))]
		r.Attacks = &v
	}
	if s.firewall != nil {
		r.Locks = s.firewall.Taken()
	}
	if r.Drives == nil {
		r.Drives = []smart.Drive{}
	}
	if r.Fans == nil {
		r.Fans = []collect.Fan{}
	}
	if r.Disks == nil {
		r.Disks = []collect.Partition{}
	}

	s.mu.Lock()
	s.latest = r
	s.mu.Unlock()
}

// Latest is the newest sample, nil before the first.
func (s *Sampler) Latest() *Readings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// ---------- serving ----------

// Handler serves readings to whoever presents the token as "Authorization: Bearer <token>", and
// takes a new build of the agent from them (self nil turns that off; secure is whether it is HTTPS).
func Handler(s *Sampler, token string, self *Self, secure bool) http.Handler {
	mux := http.NewServeMux()
	authorized := func(r *http.Request) bool {
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return token != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
	}
	mux.HandleFunc("POST "+UpdatePath, updateHandler(self, func() string { return s.updates.Busy() }, authorized, secure))
	firewallRoutes(mux, s, authorized, secure)
	sshdRoutes(mux, s, authorized, secure)
	tailscaleRoutes(mux, s, authorized, secure)
	updatesRoutes(mux, s, authorized, secure)
	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.noteDashboard(r.RemoteAddr)
		latest := s.Latest()
		if latest == nil {
			http.Error(w, "still taking the first reading", http.StatusServiceUnavailable)
			return
		}
		if self != nil {
			copied := *latest
			copied.Build, copied.CanUpdate = self.Build(), secure && self.CanUpdate()
			latest = &copied
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(latest)
	})
	// A plain answer at / tells someone who opens the port in a browser what it is.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("nightjar agent: add this server in the dashboard under Servers\n"))
	})
	return mux
}

// MinToken is the shortest token an agent accepts.
const MinToken = 24

var ErrShortToken = errors.New("the token must be at least 24 characters; the dashboard's Add server makes one")

// NewHTTPServer wraps the agent's handler in a server where every connection has an end in sight:
// one that sends nothing, sends its body slowly, or idles between requests is let go, so a few
// thousand of them cannot fill the agent's small memory allowance. A self-update gives its own long
// body read more time.
func NewHTTPServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
}

// Serve runs the agent on listen until ctx ends, over HTTPS with the token's key unless plain is set.
func Serve(ctx context.Context, listen, token, build string, plain bool) error {
	if len(token) < MinToken {
		return ErrShortToken
	}
	var tlsConfig *tls.Config
	if !plain {
		var err error
		if tlsConfig, err = serverTLS(token); err != nil {
			return err
		}
	}
	s := NewSampler(build)
	// The same attack watch the dashboard runs on its own machine, reported with the readings.
	webLogs, _ := filepath.Glob("/var/log/nginx/*access*.log")
	// It defends too, in iptables chains of its own, unless switched off from the dashboard or with
	// AGENT_DEFEND=off; AGENT_TRUSTED names more addresses or networks never to block.
	s.attacks = &ddos.Watcher{WebLogs: webLogs, Defender: &ddos.Defender{On: Defending, Trusted: s.trusted}}
	attacksDone := make(chan struct{})
	go func() {
		defer close(attacksDone)
		s.attacks.Run(ctx)
	}()
	// The rules set from the dashboard, put back in place: iptables forgets them on a reboot.
	s.port = listenPort(listen)
	s.firewall = newFirewall(s.port)
	go s.firewall.Start(ctx)
	// Keys-only SSH left waiting to be kept when the agent stopped goes back now.
	s.sshd = &sshd.Manager{}
	sshdDone := make(chan struct{})
	go func() {
		defer close(sshdDone)
		s.sshd.Run(ctx)
	}()
	// A Tailscale change that could cut the dashboard off goes back unless kept in time.
	s.tailscale = &tailscale.Manager{}
	tailscaleDone := make(chan struct{})
	go func() {
		defer close(tailscaleDone)
		s.tailscale.Run(ctx)
	}()
	// What to keep up to date on this machine, kept and scheduled by the agent itself.
	s.updates = startUpdates()
	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		s.updates.Run(ctx)
	}()
	go s.Run(ctx)
	self, err := FindSelf()
	if err != nil {
		self = nil // it still serves readings; it just cannot be updated
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
	}
	srv := NewHTTPServer(Handler(s, token, self, !plain))
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// An update under way is waited for (as long as systemd lets the agent take to stop): its
	// command runs on in a unit of its own regardless, but its outcome is only kept if this waits.
	<-updatesDone
	s.updates.Manager.Wait(30 * time.Second)
	// The defence takes its chains and blocks out as its watch stops: nothing would lift them later.
	// Keys-only SSH still waiting to be kept goes back as the agent stops, too.
	wait, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, done := range []chan struct{}{attacksDone, sshdDone, tailscaleDone} {
		select {
		case <-done:
		case <-wait.Done():
		}
	}
	return nil
}
