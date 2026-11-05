package storage_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"nightjar/internal/storage"
)

func TestUnavailableStoreSaysSo(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "no", "such", "dir", "h.db"))
	if v, err := s.MetaValue("firewall.rules"); v != "" || !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("an unreadable value is not an unset one: %q %v", v, err)
	}
	// The analytics API sends these as a list, never null.
	if b := s.SampleBuckets("local", 0, 60); b == nil || len(b) != 0 {
		t.Fatalf("buckets of an unavailable store: %#v", b)
	}
	if b := s.SampleBuckets("local", 0, 0); b == nil {
		t.Fatal("buckets of no width are empty, not nil")
	}

	ok := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer ok.Close()
	if v, err := ok.MetaValue("unset"); v != "" || err != nil {
		t.Fatalf("an unset value: %q %v", v, err)
	}
	ok.SetMeta("k", "v")
	if v, err := ok.MetaValue("k"); v != "v" || err != nil {
		t.Fatalf("a set value: %q %v", v, err)
	}
}

func TestPruneDropsOldRepairsToo(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	s.AddRepair(storage.Repair{TS: 10, Service: "svc", Action: "restart"})
	s.AddRepair(storage.Repair{TS: 100, Service: "svc", Action: "restart"})
	s.Prune(50)
	if got := s.Repairs("", 10); len(got) != 1 || got[0].TS != 100 {
		t.Fatalf("repairs kept: %+v", got)
	}
}

func TestPeaksAreKeptPerMinute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	// A database from before the peaks were kept, with one minute in it.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE samples (host TEXT NOT NULL, ts REAL NOT NULL, up REAL NOT NULL,
		cpu REAL, cpu_max REAL, mem REAL, temp REAL, temp_max REAL, disk REAL, rx REAL, tx REAL, latency REAL);
		INSERT INTO samples (host, ts, up, mem, latency) VALUES ('nas', 0, 1, 70, 30)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := storage.Open(path)
	defer s.Close()
	if !s.Available() {
		t.Fatal("an older database opens")
	}
	f := func(v float64) *float64 { return &v }
	s.AddSamples([]storage.Sample{
		{Host: "nas", TS: 60, Up: 1, Mem: f(50), MemMax: f(98), Latency: f(10), LatencyMax: f(400)},
		{Host: "nas", TS: 120, Up: 1, Mem: f(50), MemMax: f(55), Latency: f(10), LatencyMax: f(20)},
	})
	b := s.SampleBuckets("nas", 0, 3600)
	if len(b) != 1 || b[0].MemMax == nil || *b[0].MemMax != 98 || b[0].LatencyMax == nil || *b[0].LatencyMax != 400 {
		t.Fatalf("one bucket holding the highest minute: %+v", b)
	}
	if b := s.SampleBuckets("nas", 0, 30); len(b) != 3 || b[0].MemMax == nil || *b[0].MemMax != 70 || *b[0].LatencyMax != 30 {
		t.Fatalf("a minute from before stands in with its average: %+v", b)
	}
}
