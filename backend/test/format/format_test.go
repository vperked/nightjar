package format_test

import (
	"testing"

	"servermonitor/internal/format"
)

func TestBytes(t *testing.T) {
	cases := map[float64]string{0: "0 B", 512: "512.00 B", 2048: "2.00 KB", 5 * 1024 * 1024: "5.00 MB", 2000 * 1024 * 1024: "1.95 GB", 7000 << 30: "6.84 TB"}
	for in, want := range cases {
		if got := format.Bytes(in); got != want {
			t.Errorf("Bytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := map[float64]string{30: "30.0 seconds", 120: "2 minutes", 3661: "1 hour, 1 minute, 1 second", 90061: "1 day, 1 hour, 1 minute"}
	for in, want := range cases {
		if got := format.Duration(in); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
}
