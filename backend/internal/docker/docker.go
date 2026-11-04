// Package docker talks to the Docker engine over its socket: the containers there are, whether
// one is running and healthy, and restarting one. Many media servers run Plex and the *arr apps
// in containers rather than as systemd services, so a check can watch a container and a repair
// can restart it.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Client is safe for concurrent use. A nil Client has no Docker.
type Client struct {
	Socket string
	http   *http.Client

	mu       sync.Mutex
	restarts map[string][]restart // per container, its restart counts lately
}

type restart struct {
	at    time.Time
	count int
}

func New(socket string) *Client {
	if socket == "" {
		return nil
	}
	return &Client{
		Socket:   socket,
		restarts: map[string][]restart{},
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}},
	}
}

// ErrUnavailable means Docker cannot be reached.
var ErrUnavailable = errors.New("docker is not reachable")

// ErrNotFound means there is no container by that name.
var ErrNotFound = errors.New("no container by that name")

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	if c == nil {
		return ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrUnavailable, c.Socket, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 300:
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(body, &e)
		return fmt.Errorf("docker: %s", strings.TrimSpace(cmpOr(e.Message, resp.Status)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Container is one container as the dashboard lists it.
type Container struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`  // running, exited, restarting, paused, created, dead
	Status string `json:"status"` // Docker's words: "Up 3 days (healthy)"
	Health string `json:"health,omitempty"`
}

// List returns every container, running or not, by name.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	var raw []struct {
		Names  []string
		Image  string
		State  string
		Status string
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json?all=1", &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		ct := Container{Name: name, Image: r.Image, State: r.State, Status: r.Status}
		switch {
		case strings.Contains(r.Status, "(healthy)"):
			ct.Health = "healthy"
		case strings.Contains(r.Status, "(unhealthy)"):
			ct.Health = "unhealthy"
		case strings.Contains(r.Status, "(health: starting)"):
			ct.Health = "starting"
		}
		out = append(out, ct)
	}
	slices.SortFunc(out, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// State is what inspecting a container says about whether it is well.
type State struct {
	Status       string    // running, exited, restarting, paused, created, dead
	ExitCode     int       //
	Error        string    //
	OOMKilled    bool      //
	StartedAt    time.Time //
	FinishedAt   time.Time //
	Health       string    // healthy, unhealthy, starting, or "" without a health check
	HealthLog    string    // the last health check's output
	RestartCount int
}

// Inspect reads one container's state.
func (c *Client) Inspect(ctx context.Context, name string) (State, error) {
	var raw struct {
		RestartCount int
		State        struct {
			Status     string
			ExitCode   int
			Error      string
			OOMKilled  bool
			StartedAt  time.Time
			FinishedAt time.Time
			Health     *struct {
				Status string
				Log    []struct{ Output string }
			}
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", &raw); err != nil {
		return State{}, err
	}
	st := State{Status: raw.State.Status, ExitCode: raw.State.ExitCode, Error: raw.State.Error, OOMKilled: raw.State.OOMKilled,
		StartedAt: raw.State.StartedAt, FinishedAt: raw.State.FinishedAt, RestartCount: raw.RestartCount}
	if h := raw.State.Health; h != nil {
		st.Health = h.Status
		if n := len(h.Log); n > 0 {
			st.HealthLog = strings.TrimSpace(h.Log[n-1].Output)
		}
	}
	return st, nil
}

// Restart restarts a container, giving it ten seconds to stop.
func (c *Client) Restart(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/restart?t=10", nil)
}

// flapWindow and flapAt: restarting this often counts as down, even while it is up between them.
const (
	flapWindow = 10 * time.Minute
	flapAt     = 3
)

// restartsLately records the container's restart count and says how many restarts the last ten
// minutes held.
func (c *Client) restartsLately(name string, count int, now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	log := append(c.restarts[name], restart{now, count})
	log = slices.DeleteFunc(log, func(r restart) bool { return now.Sub(r.at) > flapWindow })
	c.restarts[name] = log
	return log[len(log)-1].count - log[0].count
}

func since(t time.Time, now time.Time) string {
	d := now.Sub(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Check says whether a container is well: running, not unhealthy, and not restarting over and
// over. The detail always names the container, so incidents can tell a crash from a slow page.
func (c *Client) Check(ctx context.Context, name string) (bool, string) {
	now := time.Now()
	st, err := c.Inspect(ctx, name)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, fmt.Sprintf("container %s does not exist", name)
	case err != nil:
		return false, err.Error()
	}
	recent := c.restartsLately(name, st.RestartCount, now)
	switch st.Status {
	case "running":
	case "restarting":
		return false, fmt.Sprintf("container %s keeps restarting (exit code %d)", name, st.ExitCode)
	case "exited", "dead":
		why := fmt.Sprintf("exit code %d", st.ExitCode)
		if st.OOMKilled {
			why = "killed for using too much memory"
		} else if st.Error != "" {
			why = st.Error
		}
		return false, fmt.Sprintf("container %s stopped %s: %s", name, since(st.FinishedAt, now), why)
	default:
		return false, fmt.Sprintf("container %s is %s", name, st.Status)
	}
	if st.Health == "unhealthy" {
		detail := fmt.Sprintf("container %s is unhealthy", name)
		if line := lastLine(st.HealthLog); line != "" {
			detail += ": " + line
		}
		return false, detail
	}
	if recent >= flapAt {
		return false, fmt.Sprintf("container %s restarted %d times in the last ten minutes", name, recent)
	}
	detail := fmt.Sprintf("container running, started %s", since(st.StartedAt, now))
	if st.Health != "" {
		detail = fmt.Sprintf("container %s, started %s", st.Health, since(st.StartedAt, now))
	}
	return true, detail
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}
