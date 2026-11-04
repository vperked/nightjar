package report_test

import (
	"testing"
	"time"

	"nightjar/internal/report"
)

func TestWhen(t *testing.T) {
	w, err := report.ParseWhen("Mon 09:00")
	if err != nil {
		t.Fatal(err)
	}
	// Tuesday 3 November 2026: the last was Monday the 2nd, the next Monday the 9th.
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.Local)
	if got := w.Last(now); !got.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)) {
		t.Fatalf("last %v", got)
	}
	if got := w.Next(now); !got.Equal(time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local)) {
		t.Fatalf("next %v", got)
	}
	// On Monday before nine, the last was a week ago.
	if got := w.Last(time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local)); !got.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)) {
		t.Fatalf("last before nine %v", got)
	}
	for _, bad := range []string{"", "Monday", "Mon 9am", "Xyz 09:00", "Mon 25:00"} {
		if _, err := report.ParseWhen(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
