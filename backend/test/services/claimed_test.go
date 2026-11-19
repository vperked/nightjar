package services_test

import (
	"testing"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// A check renamed while it runs, its old name taken by a new check that then runs too: each
// result goes to the check its run was claimed from, not to whichever has the name now.
func TestAResultGoesToTheCheckItWasClaimedFrom(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	old := m.ClaimRuns(1000)
	if len(old) != 1 {
		t.Fatal("a new check runs")
	}
	m.Save("api", parseSpec(t, `{"name":"api-prod","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	m.Save("", parseSpec(t, `{"name":"api","type":"tcp","target":"127.0.0.1:2","interval":60,"retries":0}`))
	fresh := m.ClaimRuns(1001)
	if len(fresh) != 1 || fresh[0].Spec.Target != "127.0.0.1:2" {
		t.Fatalf("the new check runs beside the renamed one: %+v", fresh)
	}

	spec, _, recorded := m.Report(old[0], services.Result{Up: true}, 1005)
	if !recorded || spec.Name != "api-prod" {
		t.Fatalf("the old run reports to the renamed check: %q %v", spec.Name, recorded)
	}
	spec, change, recorded := m.Report(fresh[0], services.Result{Detail: "refused"}, 1006)
	if !recorded || spec.Name != "api" || spec.Target != "127.0.0.1:2" || change != services.StatusDown {
		t.Fatalf("the new run reports to the new check: %+v %q %v", spec, change, recorded)
	}
	if got := m.State("api-prod"); got.Status != services.StatusUp || got.LastCheck != 1005 {
		t.Fatalf("api-prod has its own result: %+v", got)
	}
	if got := m.State("api"); got.Status != services.StatusDown || got.LastCheck != 1006 {
		t.Fatalf("api has its own result: %+v", got)
	}
	if due := m.ClaimRuns(1100); len(due) != 2 {
		t.Fatalf("both run again: %+v", due)
	}
}

// A check deleted while it runs, and a new one made with its name: the old run's result is
// dropped, not recorded (nor alerted, nor repaired) as the new check's.
func TestADeletedChecksResultIsNotTheNewOnes(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor(nil, store)
	m.Save("", parseSpec(t, `{"name":"web","type":"tcp","target":"127.0.0.1:1","interval":60}`))
	old := m.ClaimRuns(1000)
	m.Delete("web")
	m.Save("", parseSpec(t, `{"name":"web","type":"tcp","target":"127.0.0.1:2","interval":60}`))
	if _, _, recorded := m.Report(old[0], services.Result{Detail: "refused"}, 1005); recorded {
		t.Fatal("the deleted check's result is dropped")
	}
	if got := m.State("web"); got.LastCheck != 0 || got.LastResult != nil {
		t.Fatalf("the new check has no result yet: %+v", got)
	}
	if due := m.ClaimRuns(1006); len(due) != 1 || due[0].Spec.Target != "127.0.0.1:2" {
		t.Fatalf("the new check runs: %+v", due)
	}
}
