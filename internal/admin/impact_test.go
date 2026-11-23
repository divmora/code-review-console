package admin

import (
	"testing"
	"time"
)

// Issue #112: on 23 Nov the 30-day range ends on 23 Nov, not 24 Nov.
func TestImpactPeriodThrough(t *testing.T) {
	now := time.Date(2026, 11, 23, 22, 30, 0, 0, time.UTC)
	for rng, want := range map[string][2]string{
		"30d":  {"2026-10-25", "2026-11-23"},
		"90d":  {"2026-08-26", "2026-11-23"},
		"365d": {"2025-12-01", "2026-11-30"},
	} {
		from, to, _ := impactPeriod(rng, now)
		if got := [2]string{from.Format(time.DateOnly), to.AddDate(0, 0, -1).Format(time.DateOnly)}; got != want {
			t.Errorf("%s: %v, want %v", rng, got, want)
		}
	}
}
