package docker_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/docker"
)

// engine serves the bits of Docker's API the client uses, from a socket in a temporary folder.
func engine(t *testing.T, containers map[string]map[string]any) (*docker.Client, *[]string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	var restarted []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		list := []map[string]any{}
		for name, c := range containers {
			list = append(list, map[string]any{"Names": []string{"/" + name}, "Image": "lscr.io/" + name, "State": c["State"].(map[string]any)["Status"], "Status": c["Status"]})
		}
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("GET /containers/{name}/json", func(w http.ResponseWriter, r *http.Request) {
		c, ok := containers[r.PathValue("name")]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(c)
	})
	mux.HandleFunc("POST /containers/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
		restarted = append(restarted, r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return docker.New(socket), &restarted
}

func state(status string, extra map[string]any) map[string]any {
	st := map[string]any{"Status": status, "StartedAt": "2026-11-01T10:00:00Z", "FinishedAt": "2026-11-03T02:00:00Z"}
	for k, v := range extra {
		st[k] = v
	}
	return map[string]any{"State": st, "RestartCount": 0, "Status": "Up 2 days"}
}

func TestContainers(t *testing.T) {
	unhealthy := state("running", map[string]any{"Health": map[string]any{"Status": "unhealthy", "Log": []map[string]any{{"Output": "curl: (7) Failed to connect\n"}}}})
	containers := map[string]map[string]any{
		"sonarr":   state("running", map[string]any{"Health": map[string]any{"Status": "healthy"}}),
		"radarr":   state("exited", map[string]any{"ExitCode": 137, "OOMKilled": true}),
		"bazarr":   unhealthy,
		"tautulli": state("restarting", map[string]any{"ExitCode": 1}),
	}
	c, restarted := engine(t, containers)
	ctx := context.Background()
	list, err := c.List(ctx)
	if err != nil || len(list) != 4 || list[0].Name != "bazarr" {
		t.Fatalf("list: %v %+v", err, list)
	}
	for name, want := range map[string]string{
		"sonarr":   "container healthy, started",
		"radarr":   "container radarr stopped",
		"bazarr":   "container bazarr is unhealthy: curl: (7) Failed to connect",
		"tautulli": "container tautulli keeps restarting",
		"lidarr":   "container lidarr does not exist",
	} {
		up, detail := c.Check(ctx, name)
		if up != (name == "sonarr") || !strings.Contains(detail, want) {
			t.Errorf("%s: %v %q, want %q", name, up, detail, want)
		}
	}
	if _, detail := c.Check(ctx, "radarr"); !strings.Contains(detail, "too much memory") {
		t.Errorf("an OOM kill says so: %q", detail)
	}
	// Up, but restarting over and over.
	for i, n := range []int{1, 2, 3, 5} {
		containers["sonarr"]["RestartCount"] = n
		up, detail := c.Check(ctx, "sonarr")
		if i == 3 && (up || !strings.Contains(detail, "restarted 5 times")) {
			t.Fatalf("flapping: %v %q", up, detail)
		}
	}
	if err := c.Restart(ctx, "radarr"); err != nil || len(*restarted) != 1 {
		t.Fatalf("restart: %v %v", err, *restarted)
	}
	if up, detail := docker.New(filepath.Join(t.TempDir(), "none.sock")).Check(ctx, "x"); up || !strings.Contains(detail, "not reachable") {
		t.Fatalf("no docker: %q", detail)
	}
}
