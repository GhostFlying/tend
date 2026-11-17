//go:build linux

package updater

import "testing"

func TestLocalInstallationPhasesDoNotWaitForPollIntervals(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "await_readiness"}[wait], func(t *testing.T) {
			h := newHarness(t)
			h.a.unhealthy = wait
			h.upgrade(t)
			if err := h.s.advance(t.Context()); err != nil {
				t.Fatal(err)
			}
			phase := "committed"
			if wait {
				phase = "checking"
			}
			if op := h.s.Status().Operation; op.Phase != phase || h.p.starts != 1 || h.p.stops != 1 || h.a.sequence != 1 {
				t.Fatal("local steps waited or readiness was polled in a loop", op, h.p.starts, h.p.stops, h.a.sequence)
			}
			if wait {
				h.a.unhealthy = false
				if err := h.s.advance(t.Context()); err != nil {
					t.Fatal(err)
				}
				if h.s.Status().Operation.Phase != "committed" || h.p.starts != 1 || h.p.stops != 1 {
					t.Fatal("readiness repeated the replacement")
				}
			}
		})
	}
}
