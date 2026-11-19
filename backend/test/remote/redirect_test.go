package remote_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nightjar/internal/agent"
)

// An agent that has been taken over cannot send the dashboard's requests, token and all, somewhere
// else with a redirect: they stop at the agent, over plain HTTP and HTTPS alike.
func TestAgentRedirectsAreNotFollowed(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(elsewhere.Close)
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	plain := httptest.NewServer(redirect)
	t.Cleanup(plain.Close)
	secure := httptest.NewUnstartedServer(redirect)
	cert, _ := agent.Certificate(token)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	t.Cleanup(secure.Close)

	m := newMonitor(t, openDB(t), &alerts{})
	for _, url := range []string{plain.URL, secure.URL} {
		if _, err := m.Add(context.Background(), "nas", url, token); err == nil {
			t.Errorf("%s: a redirecting agent was added", url)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the redirect was followed %d times", n)
	}
}
