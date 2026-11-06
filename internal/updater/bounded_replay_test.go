//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBoundedServerRecordedReplayDoesNotRenewExpiredAuthority(t *testing.T) {
	h, p, _ := committedFixture(t)
	auth := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	boundedHistoryAdvance(h, auth.ExpiresAtMillis-h.now.UnixMilli()+1)
	root := t.TempDir()
	planFile, authFile := filepath.Join(root, "plan.json"), filepath.Join(root, "authorization.json")
	if err := writeJSON(planFile, CommittedHandoffPlan{Maintenance: p}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(authFile, auth); err != nil {
		t.Fatal(err)
	}
	for _, reopened := range []bool{false, true} {
		if reopened {
			h.s.Close()
			var err error
			h.s, err = Open(h.cfg, h.opts)
			if err != nil {
				t.Fatal(err)
			}
		}
		before := h.s.Status()
		rawBefore, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		stop := boundedCancelServe(t, h)
		_, err = BoundedCommand(t.Context(), h.cfg, "force", planFile, authFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase)
		stop()
		if err != nil {
			t.Fatal("original expired request did not return its recorded result", err)
		}
		for _, mode := range []string{"payload", "action", "new_expired_request"} {
			changed := auth
			action := "force"
			switch mode {
			case "payload":
				changed.Reason = "changed original request payload"
			case "action":
				action = "extend"
			case "new_expired_request":
				changed.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
				changed.ExpectedGeneration = before.Generation
				changed.ExpectedPhase = before.Maintenance.Phase
				action = "extend"
			}
			if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, changed, action); err == nil {
				t.Fatal("replay widened expired authority", mode)
			}
		}
		rawAfter, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "state.json"))
		if err != nil || string(rawBefore) != string(rawAfter) || !reflect.DeepEqual(before, h.s.Status()) || h.p.starts != 0 || h.p.stops != 0 {
			t.Fatal("recorded replay renewed its budget, changed durable facts or executed a process", err)
		}
	}
}
