package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a five-field numeric calendar schedule. Its inputs contain no clock.
// Day-of-month and day-of-week use the traditional OR when both are restricted.
type Cron struct {
	fields [5]map[int]bool
	wild   [5]bool
	zone   *time.Location
}

func ParseCron(expression string, zone *time.Location) (Cron, error) {
	var c Cron
	parts := strings.Fields(expression)
	if len(parts) != 5 || zone == nil || zone.String() == "" || zone.String() == "Local" {
		return c, fmt.Errorf("cron requires five numeric fields and an explicit IANA timezone")
	}
	var err error
	c.zone = zone
	for i, bounds := range [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}} {
		c.fields[i], err = cronField(parts[i], bounds[0], bounds[1])
		if err != nil {
			return c, fmt.Errorf("cron field %d: %w", i+1, err)
		}
		c.wild[i] = strings.HasPrefix(parts[i], "*")
	}
	if c.fields[4][7] {
		c.fields[4][0] = true
	}
	return c, nil
}

func cronField(s string, low, high int) (map[int]bool, error) {
	out := map[int]bool{}
	bad := func() (map[int]bool, error) { return nil, fmt.Errorf("invalid calendar field %q", s) }
	for _, item := range strings.Split(s, ",") {
		step := 1
		parts := strings.Split(item, "/")
		if len(parts) > 2 {
			return bad()
		}
		if len(parts) == 2 {
			var err error
			step, err = strconv.Atoi(parts[1])
			if err != nil || step < 1 || step > high-low+1 {
				return bad()
			}
		}
		start, end := low, high
		if parts[0] != "*" {
			rangeParts := strings.Split(parts[0], "-")
			var err error
			start, err = strconv.Atoi(rangeParts[0])
			if err != nil || len(rangeParts) > 2 {
				return bad()
			}
			end = start
			if len(parts) == 2 {
				end = high
			}
			if len(rangeParts) == 2 {
				end, err = strconv.Atoi(rangeParts[1])
				if err != nil {
					return bad()
				}
			}
		}
		if start < low || end > high || end < start {
			return bad()
		}
		for n := start; n <= end; n += step {
			out[n] = true
		}
	}
	return out, nil
}

// Next returns a strictly later calendar slot. Eight years covers leap-day gaps.
// Nonexistent wall times are skipped; a repeated wall minute uses its first
// occurrence, so computing from within the repeated hour cannot double-fire.
func (c Cron) Next(after time.Time) (time.Time, error) {
	local := after.In(c.zone)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for day := 0; day < 8*366; day++ {
		d := date.AddDate(0, 0, day)
		if !c.fields[3][int(d.Month())] {
			continue
		}
		dom, dow := c.fields[2][d.Day()], c.fields[4][int(d.Weekday())]
		matches := dom && dow
		if !c.wild[2] && !c.wild[4] {
			matches = dom || dow
		}
		if !matches {
			continue
		}
		for hour := 0; hour < 24; hour++ {
			if !c.fields[1][hour] {
				continue
			}
			for minute := 0; minute < 60; minute++ {
				if !c.fields[0][minute] {
					continue
				}
				t := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, c.zone)
				wall := func(v time.Time) bool {
					v = v.In(c.zone)
					return v.Year() == d.Year() && v.Month() == d.Month() && v.Day() == d.Day() && v.Hour() == hour && v.Minute() == minute
				}
				if !wall(t) || !t.After(after) {
					continue
				}
				// Compare both adjacent zone offsets; folds need not be one hour.
				start, end := t.ZoneBounds()
				_, offset := t.Zone()
				for _, probe := range []time.Time{start.Add(-time.Nanosecond), end} {
					_, other := probe.Zone()
					candidate := t.Add(time.Duration(offset-other) * time.Second)
					if candidate.Before(t) && wall(candidate) {
						t = candidate
					}
				}

				if t.After(after) {
					return t, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("cron has no calendar slot within eight years")
}
