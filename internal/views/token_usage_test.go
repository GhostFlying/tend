package views

import (
	"testing"
	"time"
)

func TestUsageWindowUsesFixedTimezoneAndMondayBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	for _, test := range []struct {
		kind       string
		offset     int
		start, end string
	}{
		{"day", 0, "2026-11-01T00:00:00+08:00", "2026-11-02T00:00:00+08:00"},
		{"week", 0, "2026-10-26T00:00:00+08:00", "2026-11-02T00:00:00+08:00"},
		{"month", -1, "2026-10-01T00:00:00+08:00", "2026-11-01T00:00:00+08:00"},
	} {
		start, end, err := UsageWindow(test.kind, test.offset, now)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := time.Parse(time.RFC3339, test.start)
		b, _ := time.Parse(time.RFC3339, test.end)
		if start == nil || *start != a.UnixMilli() || end != b.UnixMilli() {
			t.Fatalf("%s: %v %d", test.kind, start, end)
		}
	}
}
