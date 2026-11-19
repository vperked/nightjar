package remote_test

import (
	"context"
	"fmt"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/collect"
	"nightjar/internal/smart"
)

// An agent that has been taken over can report any number of disks and drives. The dashboard takes
// a bounded number from it, and still saves the alerts it raises, once per poll.
func TestAFloodOfDisksIsCapped(t *testing.T) {
	a := newAgent(t)
	flood := func(prefix string) {
		a.set(func(r *agent.Readings, _ *bool) {
			r.Disks, r.Drives = nil, nil
			for i := range 5000 {
				r.Disks = append(r.Disks, collect.Partition{Mountpoint: fmt.Sprintf("/%s/%d", prefix, i), Percent: 99})
				r.Drives = append(r.Drives, smart.Drive{Device: fmt.Sprintf("/dev/%s%d", prefix, i), Health: "failing"})
			}
		})
	}
	flood("a")
	db := openDB(t)
	m := newMonitor(t, db, &alerts{})
	ctx := context.Background()
	st, err := m.Add(ctx, "nas", a.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(st.Readings.Disks) + len(st.Readings.Drives); n > 512 {
		t.Fatalf("%d disks and drives kept from one agent", n)
	}
	if n := len(st.Problems); n == 0 || n > 512 {
		t.Fatalf("%d alerts from one agent", n)
	}

	// The alerts were saved: a restarted dashboard knows them and does not announce them again.
	again := &alerts{}
	m2 := newMonitor(t, db, again)
	m2.PollAll(ctx)
	if got := again.after(1); len(got) != 0 {
		t.Fatalf("after a restart, %d alerts announced again, such as %q", len(got), got[0])
	}

	// A fresh flood under other names replaces the old one's alerts rather than adding to them.
	flood("b")
	m.PollAll(ctx)
	if got, _ := m.Get(st.ID); len(got.Problems) > 512 {
		t.Fatalf("after another flood: %d alerts", len(got.Problems))
	}
}
