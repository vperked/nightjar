package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// The dashboard config names the server's time zone, which weekly maintenance and quiet hours
// follow, with its offset now.
func TestConfigTimeZone(t *testing.T) {
	s, _ := newServer(t, "", false)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	var body struct {
		TimeZone *struct {
			Name   string `json:"name"`
			Offset int    `json:"offset"`
		} `json:"time_zone"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.TimeZone == nil {
		t.Fatalf("no time zone in %s (%v)", rec.Body, err)
	}
	if body.TimeZone.Name == "" || body.TimeZone.Name == "Local" {
		t.Fatalf("zone name %q should be a real name or an offset", body.TimeZone.Name)
	}
	if _, want := time.Now().Zone(); body.TimeZone.Offset != want {
		t.Fatalf("offset %d, want %d", body.TimeZone.Offset, want)
	}
}
