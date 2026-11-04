package storage_test

import (
	"path/filepath"
	"testing"
	"time"

	"nightjar/internal/storage"
)

func TestUptimeSeriesAndPrune(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	lat := 5.0
	for _, p := range []struct {
		ts float64
		up bool
	}{{10, true}, {20, false}, {30, true}, {40, true}} {
		s.Record("svc", p.ts, p.up, &lat, "d")
	}
	// Two buckets over 10..50: the first holds the failure at 20, the second only successes.
	b := s.Buckets("svc", 10, 50, 2)
	if len(b) != 2 || b[0].TS != 10 || b[0].Up || b[0].Fails != 1 || b[0].Detail != "d" || *b[0].LatencyMS != 5 || !b[1].Up || b[1].Fails != 0 {
		t.Fatalf("buckets %+v", b)
	}
	if b := s.Buckets("svc", 10, 40, 3); len(b) != 3 {
		t.Fatalf("a check exactly at the end belongs to the last bucket, got %d buckets", len(b))
	}
	if b := s.Buckets("svc", 0, 100, 1000); len(b) != 4 {
		t.Fatalf("narrow buckets should return every check, got %d", len(b))
	}
	if pts := s.Series("svc", 0, 2); len(pts) != 2 || pts[0].TS != 30 || pts[1].TS != 40 {
		t.Fatalf("series %+v", pts)
	}
	if n := s.Prune(25); n != 2 {
		t.Fatalf("pruned %d", n)
	}
}

func TestUnusableStoreDegradesQuietly(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "no", "such", "dir", "h.db"))
	if s.Available() {
		t.Fatal("expected unavailable")
	}
	s.Record("svc", 1, true, nil, "")
	if s.Counts("svc", 300, 10) != nil || s.Buckets("svc", 0, 10, 5) != nil {
		t.Fatal("unavailable store has no history")
	}
}

func TestCounts(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	now := float64(time.Now().Unix())
	slot := float64(int64(now)/300) * 300
	lat := 20.0
	s.Record("svc", slot+1, true, nil, "")
	s.Record("svc", slot+2, false, nil, "boom")
	s.Record("svc", slot-300, true, &lat, "")
	s.Record("svc", now-86400*40, true, nil, "") // outside the window
	got := map[float64]storage.Count{}
	for _, c := range s.Counts("svc", 300, 8640) {
		got[c.TS] = c
	}
	if len(got) != 2 || got[slot] != (storage.Count{TS: slot, Checks: 2, Ups: 1, FailDetail: "boom"}) ||
		got[slot-300] != (storage.Count{TS: slot - 300, Checks: 1, Ups: 1, LatencyN: 1, LatencySum: 20}) {
		t.Fatalf("counts %+v", got)
	}
}

func TestNotificationHistory(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	add := func(ts float64, area, sev, title string) int64 {
		return s.AddNotification(storage.Notification{TS: ts, Area: area, Severity: sev, Title: title, Status: "sent", Channels: []string{"Discord"}})
	}
	add(100, "uptime", "critical", "Service Down: Plex")
	add(200, "torrents", "ok", "Download Complete")
	id := add(300, "services", "warning", "sonarr_100% keeps restarting")

	all, total := s.Notifications(storage.NotificationQuery{})
	if total != 3 || len(all) != 3 || all[0].ID != id || all[0].Channels[0] != "Discord" {
		t.Fatalf("newest first, got %d %+v", total, all)
	}
	if got, _ := s.Notifications(storage.NotificationQuery{Search: "plex"}); len(got) != 1 {
		t.Fatalf("search should match titles case-insensitively, got %+v", got)
	}
	if got, _ := s.Notifications(storage.NotificationQuery{Search: "100%"}); len(got) != 1 {
		t.Fatalf("a %% in the search is matched literally, got %+v", got)
	}
	if got, n := s.Notifications(storage.NotificationQuery{Severities: []string{"critical", "warning"}}); n != 2 || len(got) != 2 {
		t.Fatalf("severity filter, got %d", n)
	}
	if got, _ := s.Notifications(storage.NotificationQuery{Before: id, Limit: 1}); len(got) != 1 || got[0].Title != "Download Complete" {
		t.Fatalf("paging, got %+v", got)
	}

	c := s.NotificationCounts()
	if c.Unread != 3 || c.Alerts != 2 || c.Worst != "critical" || c.Latest != id {
		t.Fatalf("counts, got %+v", c)
	}
	s.MarkNotificationsRead([]int64{all[2].ID}, false, true)
	if c := s.NotificationCounts(); c.Unread != 2 || c.Worst != "warning" {
		t.Fatalf("reading the critical one, got %+v", c)
	}
	s.MarkNotificationsRead(nil, true, true)
	if c := s.NotificationCounts(); c.Unread != 0 || c.Worst != "" {
		t.Fatalf("all read, got %+v", c)
	}

	s.PruneNotifications(250)
	if _, n := s.Notifications(storage.NotificationQuery{}); n != 1 {
		t.Fatalf("prune should drop older ones, got %d", n)
	}
}
