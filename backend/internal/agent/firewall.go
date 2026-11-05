package agent

// The dashboard manages the agent's firewall through these: the rules it keeps (see package
// firewall), defending against attacks on or off, and letting a blocked address back in. They take
// the token like readings do, and change things only over the HTTPS the token pins.

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
)

// Where the dashboard manages the firewall.
const (
	FirewallPath = "/v1/firewall"
	DefensePath  = "/v1/defense"
	BlockedPath  = "/v1/blocked"
)

// firewallWait is how long a firewall call may take to answer, longer than the dashboard waits.
const firewallWait = 45 * time.Second

// Files in the agent's state folder: the rules kept, and the defense switch as the dashboard set it.
var (
	firewallFile = filepath.Join(StateDir, "firewall.json")
	defenseFile  = filepath.Join(StateDir, "defense")
)

// Defending says whether the agent defends against attacks: as the dashboard last set it, else
// AGENT_DEFEND (on unless "off").
func Defending() bool {
	if b, err := os.ReadFile(defenseFile); err == nil {
		return strings.TrimSpace(string(b)) != "off"
	}
	return os.Getenv("AGENT_DEFEND") != "off"
}

func newFirewall(port uint16) *firewall.Manager {
	return &firewall.Manager{
		Open: agentOpen(port),
		Load: func() []byte { b, _ := os.ReadFile(firewallFile); return b },
		Save: func(b []byte) error { return os.WriteFile(firewallFile, b, 0o600) },
	}
}

// FirewallView is what GET FirewallPath answers: the machine's firewall, and the defense.
type FirewallView struct {
	Firewall firewall.Status   `json:"firewall"`
	Defense  *ddos.DefenseView `json:"defense,omitempty"`
}

func firewallRoutes(mux *http.ServeMux, s *Sampler, authorized func(*http.Request) bool, secure bool) {
	// guard answers for a request that may not go on: no token, plain HTTP for a change, or nothing
	// to change.
	guard := func(w http.ResponseWriter, r *http.Request, change bool) bool {
		switch {
		case !authorized(r):
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case change && !secure:
			http.Error(w, "this agent serves plain HTTP; reinstall it with HTTPS to manage its firewall from the dashboard", http.StatusForbidden)
		case s.firewall == nil:
			http.Error(w, "this agent does not manage its firewall", http.StatusNotFound)
		default:
			// A change may wait on iptables' lock, and reading the firewall back runs more commands:
			// longer than the server's write timeout, though the dashboard waits for it.
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(firewallWait))
			return true
		}
		return false
	}
	view := func(w http.ResponseWriter, r *http.Request) {
		v := FirewallView{Firewall: s.firewall.Status(r.Context())}
		if s.attacks != nil && s.attacks.Defender != nil {
			d := s.attacks.Defender.View()
			v.Defense = &d
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET "+FirewallPath, func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, false) {
			view(w, r)
		}
	})
	// keep is the dashboard asking, which must keep reaching the agent.
	keep := func(r *http.Request) []firewall.Reach {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return []firewall.Reach{{Address: host, Ports: []uint16{s.port}, What: "the dashboard"}}
		}
		return nil
	}
	// refuse answers a change the firewall turned down.
	refuse := func(w http.ResponseWriter, err error) {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, firewall.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
	}
	readRule := func(w http.ResponseWriter, r *http.Request) (firewall.Rule, bool) {
		var rule firewall.Rule
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&rule); err != nil {
			http.Error(w, "expected a rule", http.StatusBadRequest)
			return rule, false
		}
		return rule, true
	}
	mux.HandleFunc("POST "+FirewallPath+"/rules", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		rule, ok := readRule(w, r)
		if !ok {
			return
		}
		if _, err := s.firewall.Add(r.Context(), rule, keep(r)); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("PUT "+FirewallPath+"/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		rule, ok := readRule(w, r)
		if !ok {
			return
		}
		if _, err := s.firewall.Update(r.Context(), r.PathValue("id"), rule, keep(r)); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("PUT "+FirewallPath+"/order", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, `expected {"ids": [...]}`, http.StatusBadRequest)
			return
		}
		if err := s.firewall.Order(r.Context(), body.IDs); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("PUT "+FirewallPath+"/default", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			Default *string `json:"default"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Default == nil {
			http.Error(w, `expected {"default": "drop"} or {"default": ""}`, http.StatusBadRequest)
			return
		}
		if _, err := s.firewall.SetDefault(r.Context(), *body.Default); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("POST "+FirewallPath+"/default/keep", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			Keep *bool `json:"keep"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Keep == nil {
			http.Error(w, `expected {"keep": true} or {"keep": false}`, http.StatusBadRequest)
			return
		}
		if err := s.firewall.Keep(r.Context(), *body.Keep); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("DELETE "+FirewallPath+"/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		if err := s.firewall.Remove(r.Context(), r.PathValue("id")); err != nil {
			refuse(w, err)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("PUT "+DefensePath, func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			On *bool `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.On == nil {
			http.Error(w, `expected {"on": true} or {"on": false}`, http.StatusBadRequest)
			return
		}
		value := "on"
		if !*body.On {
			value = "off"
		}
		if err := os.WriteFile(defenseFile, []byte(value+"\n"), 0o600); err != nil {
			http.Error(w, "could not keep the setting: "+err.Error(), http.StatusInternalServerError)
			return
		}
		view(w, r)
	})
	mux.HandleFunc("DELETE "+BlockedPath+"/{address}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		if s.attacks == nil || s.attacks.Defender == nil || !s.attacks.Defender.Unblock(r.Context(), r.PathValue("address")) {
			http.Error(w, "that address is not blocked", http.StatusNotFound)
			return
		}
		view(w, r)
	})
}

// agentOpen is the port the firewall keeps open for the dashboard: the agent's.
func agentOpen(port uint16) []firewall.Open {
	return []firewall.Open{{Port: port, What: "this agent"}}
}

// UseFirewall gives a sampler a firewall to manage and an attack watch, as Serve does; for tests.
func (s *Sampler) UseFirewall(m *firewall.Manager, w *ddos.Watcher, port uint16) {
	if m != nil && m.Open == nil {
		m.Open = agentOpen(port)
	}
	s.firewall, s.attacks, s.port = m, w, port
}

// listenPort is the port of a listen address (":9101" → 9101).
func listenPort(listen string) uint16 {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(p, 10, 16)
	return uint16(n)
}
