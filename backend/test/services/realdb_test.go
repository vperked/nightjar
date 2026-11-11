//go:build realdb

package services_test

import (
	"os"
	"testing"

	"nightjar/internal/services"
)

// The database checks against real servers, which the normal tests only fake. Run them with
//
//	NIGHTJAR_TEST_MYSQL=127.0.0.1:3306 NIGHTJAR_TEST_POSTGRES=db.lan NIGHTJAR_TEST_REDIS=127.0.0.1:6379 \
//	  go test -tags realdb -run RealDatabases -v ./test/services/
//
// Each one set must be up and answering; NIGHTJAR_TEST_REDIS_PASSWORD is sent with AUTH when set.
func TestRealDatabases(t *testing.T) {
	for _, c := range []struct{ kind, env string }{{"mysql", "NIGHTJAR_TEST_MYSQL"}, {"postgres", "NIGHTJAR_TEST_POSTGRES"}, {"redis", "NIGHTJAR_TEST_REDIS"}} {
		t.Run(c.kind, func(t *testing.T) {
			target := os.Getenv(c.env)
			if target == "" {
				t.Skip(c.env + " is not set")
			}
			spec := services.Spec{Name: c.kind, Type: c.kind, Target: target, Timeout: 5}
			if c.kind == "redis" {
				spec.Password = os.Getenv("NIGHTJAR_TEST_REDIS_PASSWORD")
			}
			r := services.Run(spec)
			t.Logf("%s %s: up=%v %q", c.kind, target, r.Up, r.Detail)
			if !r.Up {
				t.Errorf("%s at %s is down: %s", c.kind, target, r.Detail)
			}
		})
	}
}
