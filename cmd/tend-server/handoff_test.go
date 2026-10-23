package main

import (
	"encoding/json"
	"testing"
)

func TestSummaryIdleConfigDefaultsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		body    string
		want    int64
		invalid bool
	}{
		{`{}`, 50, false}, {`{"summary_idle_minutes":null}`, 50, false},
		{`{"summary_idle_minutes":0}`, 0, false}, {`{"summary_idle_minutes":1}`, 1, false},
		{`{"summary_idle_minutes":10080}`, 10080, false}, {`{"summary_idle_minutes":-1}`, 0, true},
		{`{"summary_idle_minutes":10081}`, 0, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var c mainAgentConfig
			if err := json.Unmarshal([]byte(tc.body), &c); err != nil {
				t.Fatal(err)
			}
			got, err := summaryIdleMinutes(c.SummaryIdleMinutes)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("idle=%d err=%v", got, err)
			}
		})
	}
}
