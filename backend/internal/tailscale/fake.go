package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
)

// Fake is a pretend tailscale command for tests: a machine on a tailnet with two others, one of them
// offering to be an exit node. Give its Exec to a Manager.
type Fake struct {
	mu sync.Mutex
	// Backend is tailscaled's state; LoggedOut is a logout having happened.
	Backend   string
	LoggedOut bool
	AuthURL   string
	Prefs     map[string]any
	// Calls are the commands run, joined by spaces.
	Calls []string
	// AuthKey is the auth key the last login read from its file.
	AuthKey string
}

// NewFake is a machine logged in and connected.
func NewFake() *Fake {
	return &Fake{Backend: "Running", Prefs: map[string]any{
		"RouteAll": false, "ExitNodeID": "", "ExitNodeIP": "", "ExitNodeAllowLANAccess": false, "CorpDNS": true,
		"RunSSH": false, "WantRunning": true, "LoggedOut": false, "ShieldsUp": false, "Hostname": "", "AdvertiseRoutes": nil,
	}}
}

// Exec answers like tailscale would.
func (f *Fake) Exec(_ context.Context, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, strings.Join(args, " "))
	switch args[0] {
	case "status":
		return f.status(), nil
	case "debug":
		f.Prefs["LoggedOut"] = f.LoggedOut
		b, _ := json.Marshal(f.Prefs)
		return string(b), nil
	case "up":
		f.Backend = "Running"
	case "down":
		f.Backend = "Stopped"
	case "logout":
		f.Backend, f.LoggedOut = "NeedsLogin", true
	case "login":
		for _, a := range args[1:] {
			if path, ok := strings.CutPrefix(a, "--auth-key=file:"); ok {
				b, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				f.AuthKey, f.Backend, f.LoggedOut, f.AuthURL = string(b), "Running", false, ""
				return "", nil
			}
		}
		f.AuthURL = "https://login.tailscale.com/a/fake"
		return "To authenticate, visit:\n\n\t" + f.AuthURL + "\n", errors.New("exit status 1: timeout waiting for Tailscale service to enter a Running state")
	case "set":
		for _, a := range args[1:] {
			name, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
			on := v == "true"
			switch name {
			case "accept-routes":
				f.Prefs["RouteAll"] = on
			case "accept-dns":
				f.Prefs["CorpDNS"] = on
			case "ssh":
				f.Prefs["RunSSH"] = on
			case "shields-up":
				f.Prefs["ShieldsUp"] = on
			case "hostname":
				f.Prefs["Hostname"] = v
			case "exit-node":
				f.Prefs["ExitNodeIP"], f.Prefs["ExitNodeID"] = "", ""
				if v == "100.64.0.3" {
					f.Prefs["ExitNodeID"] = "nExit"
				} else if v != "" {
					return "", errors.New("exit status 1: invalid exit node")
				}
			case "exit-node-allow-lan-access":
				f.Prefs["ExitNodeAllowLANAccess"] = on
			case "advertise-routes":
				var routes []string
				if v != "" {
					routes = strings.Split(v, ",")
				}
				if old, _ := f.Prefs["AdvertiseRoutes"].([]string); len(old) > 0 && old[len(old)-1] == "::/0" {
					routes = append(routes, "0.0.0.0/0", "::/0")
				}
				f.Prefs["AdvertiseRoutes"] = routes
			case "advertise-exit-node":
				routes, _ := f.Prefs["AdvertiseRoutes"].([]string)
				var kept []string
				for _, r := range routes {
					if r != "0.0.0.0/0" && r != "::/0" {
						kept = append(kept, r)
					}
				}
				if on {
					kept = append(kept, "0.0.0.0/0", "::/0")
				}
				f.Prefs["AdvertiseRoutes"] = kept
			}
		}
	}
	return "", nil
}

// Pref is one of the settings in place.
func (f *Fake) Pref(name string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Prefs[name]
}

func (f *Fake) status() string {
	running := f.Backend == "Running"
	exitID, _ := f.Prefs["ExitNodeID"].(string)
	st := map[string]any{
		"Version":      "1.104.7-tfake",
		"BackendState": f.Backend,
		"AuthURL":      f.AuthURL,
		"Health":       []string{},
	}
	if f.Backend == "NeedsLogin" {
		st["Self"] = map[string]any{"ID": "", "HostName": "home"}
		return mustJSON(st)
	}
	st["Self"] = map[string]any{"ID": "nSelf", "HostName": "home", "DNSName": "home.tail1234.ts.net.", "OS": "linux", "UserID": 1, "TailscaleIPs": []string{"100.64.0.1", "fd7a:115c:a1e0::1"}, "Online": running}
	st["CurrentTailnet"] = map[string]any{"Name": "owner@example.com", "MagicDNSSuffix": "tail1234.ts.net", "MagicDNSEnabled": true}
	st["User"] = map[string]any{"1": map[string]any{"LoginName": "owner@example.com"}}
	st["Peer"] = map[string]any{
		"nodekey:a": map[string]any{"ID": "nLaptop", "HostName": "laptop", "OS": "macOS", "UserID": 1, "TailscaleIPs": []string{"100.64.0.2"}, "Online": running, "CurAddr": "192.168.1.20:41641", "RxBytes": 100, "TxBytes": 200},
		"nodekey:b": map[string]any{"ID": "nExit", "HostName": "vps", "OS": "linux", "UserID": 1, "Tags": []string{"tag:server"}, "TailscaleIPs": []string{"fd7a:115c:a1e0::3", "100.64.0.3"}, "Online": running, "Relay": "fra", "ExitNodeOption": true, "ExitNode": exitID == "nExit", "PrimaryRoutes": []string{"10.0.0.0/24", "0.0.0.0/0", "::/0"}},
	}
	return mustJSON(st)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
