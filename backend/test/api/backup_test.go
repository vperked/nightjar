package api_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

// Restoring a backup made without secrets puts a server's agent token back only when the backup has
// that server at the address it is at now: one saved before it moved must not get the token sent there.
func TestRestoreWithoutSecretsKeepsTokensOnlyAtTheSameAddress(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "b.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	token, err := acc.NewSession("test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir()}, ctx)
	cookie := &http.Cookie{Name: "nightjar_session", Value: token}

	db.SetMeta("remote.hosts", `[{"id":"nas","name":"NAS","address":"https://10.0.0.20:9101","token":"nas-token-nas-token"},`+
		`{"id":"pi","name":"Pi","address":"https://10.0.0.30:9101","token":"pi-token-pi-token"}]`)
	body := `{"nightjar":"test","secrets":false,"meta":{"remote.hosts":[` +
		`{"id":"nas","name":"NAS","address":"https://10.0.0.20:9101"},` +
		`{"id":"pi","name":"Pi","address":"https://192.168.1.30:9101"}]}}`
	if rec := serve(s, "POST", "/api/backup/restore", body, "127.0.0.1:5000", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	hosts := db.Meta("remote.hosts")
	if !strings.Contains(hosts, "nas-token-nas-token") {
		t.Fatalf("the token of the server at the same address is lost: %s", hosts)
	}
	if strings.Contains(hosts, "pi-token-pi-token") {
		t.Fatalf("a token went to the address the backup had for its server: %s", hosts)
	}
}
