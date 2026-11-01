package domain

import (
	"testing"
	"time"
)

func TestCronCalendarAndDST(t *testing.T) {
	for _, tt := range []struct{ expression, zone, after, want string }{
		{"*/10 * * * *", "UTC", "2026-11-01T10:09:59Z", "2026-11-01T10:10:00Z"},
		{"0 9 * * 1-5", "Asia/Shanghai", "2026-11-01T10:00:00Z", "2026-11-02T01:00:00Z"},
		{"30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T05:31:00Z", "2026-11-02T06:30:00Z"},
		{"0 0 29 2 *", "UTC", "2025-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"0 0 1 * 7", "UTC", "2026-11-02T00:00:00Z", "2026-11-08T00:00:00Z"},
	} {
		t.Run(tt.expression+tt.after, func(t *testing.T) {
			c, err := ParseCron(tt.expression, tt.zone)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := time.Parse(time.RFC3339, tt.after)
			next, err := c.Next(after)
			if err != nil || next.UTC().Format(time.RFC3339) != tt.want {
				t.Fatalf("next=%s err=%v want=%s", next, err, tt.want)
			}
		})
	}
}

func TestCronRejectsMalformedAndImpossibleSchedules(t *testing.T) {
	for _, expression := range []string{"* * * *", "60 * * * *", "*/0 * * * *", "0 0 * * MON", "0 0 4-1 * *", "0 0 * 13 *", "@daily", "0 0 * * 8", "0 0 * * 1/2/3"} {
		if _, err := ParseCron(expression, "UTC"); err == nil {
			t.Errorf("accepted %q", expression)
		}
	}
	for _, zone := range []string{"", "Local", "Not/AZone"} {
		if _, err := ParseCron("* * * * *", zone); err == nil {
			t.Errorf("accepted zone %q", zone)
		}
	}
	c, _ := ParseCron("0 0 31 2 *", "UTC")
	if _, err := c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("accepted impossible calendar date")
	}
}
