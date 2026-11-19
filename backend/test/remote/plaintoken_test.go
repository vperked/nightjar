package remote_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/remote"
)

// A token that crossed the network over plain HTTP is no secret any more, and over HTTPS it is also
// the agent's key: moving an agent to HTTPS takes a new one.
func TestMovingToHTTPSNeedsANewToken(t *testing.T) {
	ctx := context.Background()
	m := newMonitor(t, openDB(t), &alerts{})
	plain := newAgent(t)
	st, err := m.Add(ctx, "nas", plain.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}

	// The agent, reinstalled for HTTPS with a fresh token.
	const fresh = "a-fresh-token-made-for-https-0123456789"
	readings := plain.r
	secure := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(readings)
	}))
	cert, _ := agent.Certificate(fresh)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	t.Cleanup(secure.Close)

	for _, kept := range []string{"", token} {
		if _, err := m.Update(ctx, st.ID, "nas", secure.URL, kept); !errors.Is(err, remote.ErrPlainToken) {
			t.Fatalf("to HTTPS keeping the plain token (%q): %v", kept, err)
		}
	}
	if s, _ := m.Get(st.ID); s.Address != plain.srv.URL {
		t.Fatalf("a refused move must change nothing: %+v", s)
	}
	moved, err := m.Update(ctx, st.ID, "nas", secure.URL, fresh)
	if err != nil || moved.Address != secure.URL || moved.Status != "online" {
		t.Fatalf("to HTTPS with a new token: %+v %v", moved, err)
	}
	// Renaming it later keeps the token, as before.
	if _, err := m.Update(ctx, st.ID, "Living room NAS", secure.URL, ""); err != nil {
		t.Fatalf("rename over HTTPS: %v", err)
	}
}
