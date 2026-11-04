package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nightjar/internal/agentbin"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
)

func TestTheDashboardHandsOutTheAgent(t *testing.T) {
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "script-token", FrontendDir: t.TempDir()}, ctx)
	get := func(path string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if signedIn {
			req.Header.Set("Authorization", "Bearer script-token")
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/api/agent/download?arch=amd64", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("downloading needs signing in: %d", rec.Code)
	}
	if rec := get("/api/agent/download?arch=sparc", true); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown architecture: %d", rec.Code)
	}
	builds := agentbin.Builds()
	if len(builds) == 0 {
		rec := get("/api/agent/download?arch=amd64", true)
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusNotFound || body["error"] != agentbin.ErrNotBuilt.Error() {
			t.Fatalf("without builds it says how to make them: %d %v", rec.Code, body)
		}
		t.Skip("no agent builds embedded; `make agent` adds them")
	}
	rec := get("/api/agent/download?arch="+builds[0].Arch, true)
	if rec.Code != http.StatusOK || int64(rec.Body.Len()) != builds[0].Size || !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x7fELF")) {
		t.Fatalf("download: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="`+builds[0].File+`"` {
		t.Fatalf("saved as: %q", cd)
	}
}
