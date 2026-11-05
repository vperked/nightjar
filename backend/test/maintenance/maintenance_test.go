package maintenance_test

import (
	"testing"
	"time"

	"nightjar/internal/maintenance"
	"nightjar/internal/storage"
)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.Local) } // Nov 1 2026 is a Sunday

func TestWeeklyWindowAcrossMidnight(t *testing.T) {
	w := maintenance.Window{Title: "Backups", Days: []int{0}, From: "23:30", To: "01:00", Checks: []string{"nas"}}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if !w.Active(at(1, 23, 45)) || !w.Active(at(2, 0, 30)) || w.Active(at(2, 1, 1)) || w.Active(at(1, 22, 0)) {
		t.Fatal("Sunday 23:30 to Monday 01:00")
	}
	s, ok := w.At(at(2, 2, 0))
	if !ok || time.Unix(int64(s.Start), 0) != at(8, 23, 30) {
		t.Fatalf("next is the Sunday after: %v %v", ok, time.Unix(int64(s.Start), 0))
	}
	if !w.Covers("nas") || w.Covers("plex") {
		t.Fatal("covers only its checks")
	}
}

func TestManager(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := maintenance.New(store)
	now := at(3, 2, 15)
	m.Now = func() time.Time { return now }
	for _, bad := range []maintenance.Window{
		{Title: ""},
		{Title: "x", Start: 10, End: 5},
		{Title: "x", Days: []int{9}, From: "01:00", To: "02:00"},
		{Title: "x", Days: []int{1}, From: "1am", To: "02:00"},
		{Title: "x", Start: 1, End: 1 + 15*86400},
	} {
		if _, err := m.Save(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	nasUpdate, err := m.Save(maintenance.Window{Title: "Updating the NAS", Checks: []string{"nas"}, Start: float64(at(3, 2, 0).Unix()), End: float64(at(3, 3, 0).Unix()), Public: true})
	if err != nil || nasUpdate.ID == "" {
		t.Fatal(err)
	}
	if !m.Holds("check:nas", now) || m.Holds("check:plex", now) || m.Holds("unit:sonarr.service", now) {
		t.Fatal("a window over one check holds only that check's alerts")
	}
	everything, _ := m.Save(maintenance.Window{Title: "Moving house", Start: float64(at(3, 2, 10).Unix()), End: float64(at(3, 2, 20).Unix())})
	if !m.Holds("unit:sonarr.service", now) || !m.Holds("check:plex", now) {
		t.Fatal("a window over everything holds every alert")
	}
	if up := m.Upcoming(now, 7); len(up) != 2 || !up[0].On {
		t.Fatalf("upcoming: %+v", up)
	}
	m.Rename("nas", "nas-box")
	if _, on := m.For("nas-box", now); !on {
		t.Fatal("a renamed check stays in its window")
	}
	if err := m.Delete(everything.ID); err != nil || m.Holds("unit:sonarr.service", now) {
		t.Fatal("delete")
	}
	if len(maintenance.New(store).List()) != 1 {
		t.Fatal("windows are kept in the store")
	}
}

func TestWeeklyWindowKeepsItsWallClockOnDaylightSavingDays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	w := maintenance.Window{Title: "Backups", Days: []int{0}, From: "04:00", To: "04:30"}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	// Clocks went forward at 2:00 on Sunday 8 March 2026 and back at 2:00 on Sunday 1 November 2026.
	for _, day := range []time.Time{time.Date(2026, 3, 8, 0, 30, 0, 0, ny), time.Date(2026, 11, 1, 0, 30, 0, 0, ny)} {
		s, ok := w.At(day)
		start, end := time.Unix(int64(s.Start), 0).In(ny), time.Unix(int64(s.End), 0).In(ny)
		if !ok || start.Hour() != 4 || start.Minute() != 0 || end.Hour() != 4 || end.Minute() != 30 || start.Day() != day.Day() {
			t.Fatalf("%s: window ran %s to %s, want 04:00 to 04:30", day.Format("Jan 2"), start, end)
		}
	}
	overnight := maintenance.Window{Title: "Night", Days: []int{6}, From: "23:00", To: "05:00"}
	overnight.Validate()
	s, _ := overnight.At(time.Date(2026, 3, 7, 12, 0, 0, 0, ny))
	if end := time.Unix(int64(s.End), 0).In(ny); end.Day() != 8 || end.Hour() != 5 {
		t.Fatalf("a window past midnight into the change ends at 05:00 the next day, got %s", end)
	}
}

func TestRenameLeavesCopiesAlone(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := maintenance.New(store)
	if _, err := m.Save(maintenance.Window{Title: "NAS", Checks: []string{"nas", "plex"}, Start: 100, End: 200}); err != nil {
		t.Fatal(err)
	}
	before := m.List()
	m.Rename("nas", "storage")
	if got := before[0].Checks[0]; got != "nas" {
		t.Fatalf("a list taken before the rename changed under its holder: %q", got)
	}
	if got := m.List()[0].Checks[0]; got != "storage" {
		t.Fatalf("renamed check: %q", got)
	}
}
