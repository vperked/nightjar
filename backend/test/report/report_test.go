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
	// On the days the clocks change, the report still comes at 09:00 by the clock.
	if london, err := time.LoadLocation("Europe/London"); err == nil {
		sun, _ := report.ParseWhen("Sun 09:00")
		for _, day := range []int{29, 25} { // 29 March 2026 springs forward, 25 October falls back
			month := map[int]time.Month{29: time.March, 25: time.October}[day]
			got := sun.Last(time.Date(2026, month, day, 12, 0, 0, 0, london))
			if want := time.Date(2026, month, day, 9, 0, 0, 0, london); !got.Equal(want) {
				t.Errorf("on %d %s: last %v, want %v", day, month, got, want)
			}
		}
	}
	for _, bad := range []string{"", "Monday", "Mon 9am", "Xyz 09:00", "Mon 25:00"} {
		if _, err := report.ParseWhen(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
