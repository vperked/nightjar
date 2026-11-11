package storage_test

import (
	"fmt"
	"testing"

	"nightjar/internal/storage"
)

// Each day runs from its own start to the next one's, so a 23-hour day (the clocks going forward)
// or a 25-hour one puts every check in the day it was made on.
func TestDaysUseEachDaysOwnStart(t *testing.T) {
	s := storage.Open(":memory:")
	defer s.Close()
	// Day 0 is 23 hours long; day 1 is 25.
	starts := []float64{0, 23 * 3600, 48 * 3600}
	end := 72 * 3600.0
	for _, c := range []struct {
		ts float64
		up bool
	}{{100, true}, {23*3600 - 1, false}, {23 * 3600, true}, {48*3600 - 1, true}, {60 * 3600, true}, {72 * 3600, true}, {-1, true}} {
		s.Record("svc", c.ts, c.up, nil, "")
	}
	s.Record("other", 200, true, nil, "")
	got := s.Days("svc", starts, end)
	want := []storage.Day{{Start: 0, Checks: 2, Ups: 1}, {Start: 23 * 3600, Checks: 2, Ups: 2}, {Start: 48 * 3600, Checks: 1, Ups: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("days %+v, want %+v", got, want)
	}
	if got := s.Days("none", starts, end); len(got) != 0 {
		t.Fatalf("a check without results has no days: %+v", got)
	}
}

// When there are more notifications than the limit, the newest are kept, still oldest first: the
// callers explain what happened just before the end of the range.
func TestNotificationsBetweenKeepsTheNewest(t *testing.T) {
	s := storage.Open(":memory:")
	defer s.Close()
	for ts := 1.0; ts <= 5; ts++ {
		s.AddNotification(storage.Notification{TS: ts, Area: "uptime", Severity: "critical", Title: fmt.Sprint(ts), Status: "sent"})
	}
	got := s.NotificationsBetween(0, 10, 2)
	if len(got) != 2 || got[0].TS != 4 || got[1].TS != 5 {
		t.Fatalf("got %+v", got)
	}
	if all := s.NotificationsBetween(2, 4, 10); len(all) != 3 || all[0].TS != 2 || all[2].TS != 4 {
		t.Fatalf("got %+v", all)
	}
}

// A restore's meta values and settings are saved together; an empty setting removes it.
func TestRestoreSavesMetaAndSettings(t *testing.T) {
	s := storage.Open(":memory:")
	s.SaveSettings(map[string]string{"A": "1", "B": "2"})
	if err := s.Restore(map[string]string{"k": `{"x":1}`}, map[string]string{"A": "", "B": "3"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); s.Meta("k") != `{"x":1}` || got["A"] != "" || got["B"] != "3" {
		t.Fatalf("meta %q, settings %v", s.Meta("k"), got)
	}
	s.Close()
	if err := s.Restore(map[string]string{"k": "{}"}, nil); err == nil {
		t.Fatal("a closed store restores nothing, and says so")
	}
}
