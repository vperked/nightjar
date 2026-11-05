package remote_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/collect"
	"nightjar/internal/ddos"
	"nightjar/internal/remote"
)

func TestAgentsAreKeptOnTheDashboardsBuild(t *testing.T) {
	var mu sync.Mutex
	build, canUpdate, posts := "old", true, 0
	var got []byte
	var gotHash string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == agent.UpdatePath {
			posts++
			got, _ = io.ReadAll(r.Body)
			gotHash = r.Header.Get(agent.BuildHeader)
			build = gotHash // it restarts on the new build
			w.Write([]byte("updated; restarting\n"))
			return
		}
		json.NewEncoder(w).Encode(agent.Readings{Version: agent.Version, Agent: "test", Time: 1, Build: build, CanUpdate: canUpdate,
			System: collect.System{Hostname: "nas", Architecture: "arm64"}, CPU: agent.CPU{Percent: 1, Count: 1}})
	}))
	defer srv.Close()

	auto := true
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}, AutoUpdate: func() bool { return auto },
		AgentBuild: func(arch string) ([]byte, string, error) {
			if arch != "arm64" {
				t.Errorf("asked for the %s build of an arm64 agent", arch)
			}
			return []byte("NEW BUILD"), "new", nil
		}})
	ctx := context.Background()
	st, err := m.Add(ctx, "nas", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(state string) remote.State {
		for range 100 {
			if h, _ := m.Get(st.ID); h.Update != nil && h.Update.State == state {
				return h
			}
			time.Sleep(10 * time.Millisecond)
		}
		h, _ := m.Get(st.ID)
		t.Fatalf("waited for %q, got %+v", state, h.Update)
		return h
	}
	waitFor("updated")
	mu.Lock()
	if string(got) != "NEW BUILD" || gotHash != "new" || posts != 1 {
		t.Fatalf("sent %q with hash %q, %d times", got, gotHash, posts)
	}
	mu.Unlock()

	// On the new build now: nothing more is sent.
	m.PollAll(ctx)
	m.PollAll(ctx)
	mu.Lock()
	if posts != 1 {
		t.Fatalf("sent again to an agent already on the build: %d", posts)
	}
	// An agent installed before it could update itself is told to be reinstalled, once.
	build, canUpdate = "older", false
	mu.Unlock()
	m.PollAll(ctx)
	if h := waitFor("manual"); h.Update.Detail == "" {
		t.Fatal("say how to make it update itself")
	}

	// Turned off, agents are left as they are.
	auto = false
	mu.Lock()
	build, canUpdate = "older", true
	mu.Unlock()
	m.PollAll(ctx)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("updated with updates turned off: %d", posts)
	}
}

func TestAServerUnderAttackIsAnnouncedOnceAndWhenItEnds(t *testing.T) {
	a := newAgent(t)
	sent := &alerts{}
	m := newMonitor(t, openDB(t), sent)
	ctx := context.Background()
	st, err := m.Add(ctx, "nas", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	attack := ddos.Attack{ID: "a1", Start: 1000, Methods: []ddos.Finding{{Method: ddos.SYNFlood, Value: 9000}}}
	a.set(func(r *agent.Readings, _ *bool) {
		r.Attacks = &ddos.View{Attacking: true, Current: &attack}
	})
	m.PollAll(ctx)
	m.PollAll(ctx)
	got := sent.after(1)
	if len(got) != 1 || got[0] != "critical: Under attack: nas" {
		t.Fatalf("announced once: %v", got)
	}
	if h, _ := m.Get(st.ID); !slices.Contains(h.Problems, "Under attack: SYN flood") {
		t.Fatalf("its page says so: %v", h.Problems)
	}
	attack.End = 1252
	a.set(func(r *agent.Readings, _ *bool) {
		r.Attacks = &ddos.View{History: []ddos.Attack{attack}}
	})
	m.PollAll(ctx)
	if got := sent.after(2); len(got) != 2 || got[1] != "ok: Attack over: nas" {
		t.Fatalf("over: %v", got)
	}
}
