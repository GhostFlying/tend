//go:build linux

package updater

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestBoundedCancelledSourceKeepsOnlyExistingSupervision(t *testing.T) {
	for _, kind := range []string{"owned_live", "owned_crash", "owned_lost_start", "owned_sibling", "owned_changed_source", "owned_changed_binary", "manual_live", "manual_gone", "uncommitted_source"} {
		t.Run(kind, func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			manual := kind == "manual_live" || kind == "manual_gone"
			if manual {
				h.s.state.Process, h.s.state.Current = Identity{}, Version{}
				if err := h.s.save(); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "uncommitted_source" {
				h.s.state.Operation = &Operation{ID: "unfinished-source", Kind: "upgrade", Phase: "checking", Candidate: h.s.state.Current, CandidateProcess: grant.Plan.Source, LaunchID: grant.Plan.Source.LaunchID}
				if err := h.s.save(); err != nil {
					t.Fatal(err)
				}
				grant.Plan.Authorization.ExpectedGeneration = h.s.Status().Generation
				grant.Plan.Authorization.ExpectedPhase = "checking"
			}
			process := &boundedRestartCapture{fakeProcess: h.p}
			h.s.opts.Process, h.opts.Process = process, process
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "apply"); err != nil {
				t.Fatal(err)
			}
			cancel := freshBoundedRunnerGrant(h, grant, "req_00000000000000000000000002")
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil {
				t.Fatal(err)
			}
			cancelled := h.s.Status()
			if kind != "owned_live" && kind != "manual_live" && kind != "owned_changed_source" {
				delete(h.p.live, grant.Plan.Source.PID)
			}
			if kind == "owned_sibling" {
				sibling := grant.Plan.Source
				sibling.PID, sibling.StartTicks = 100, 100
				h.p.live[sibling.PID] = sibling
			}
			if kind == "owned_changed_source" {
				changed := grant.Plan.Source
				changed.SHA256 = grant.Plan.Target.ArtifactSHA256
				h.p.live[changed.PID] = changed
			}
			if kind == "owned_changed_binary" {
				if err := os.WriteFile(cancelled.Current.Binary, []byte("changed source executable"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			h.p.ambiguousStart = kind == "owned_lost_start"
			var observedErr error
			for range 4 {
				if err := h.s.Step(t.Context()); err != nil {
					observedErr = err
					if kind == "owned_lost_start" && errors.Is(err, ErrUnknown) {
						h.s.Close()
						h.s, err = Open(h.cfg, h.opts)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if manual || kind == "uncommitted_source" || kind == "owned_sibling" || kind == "owned_changed_source" || kind == "owned_changed_binary" {
				if observedErr == nil || h.p.starts != 0 || h.p.stops != 0 {
					t.Fatal("unowned or uncertain source reached restart", observedErr)
				}
				if manual {
					h.s.Close()
					var err error
					h.s, err = Open(h.cfg, h.opts)
					if err != nil {
						t.Fatal(err)
					}
					_ = h.s.Step(t.Context())
					if h.p.starts != 0 || h.s.Status().Process != (Identity{}) {
						t.Fatal("reopen adopted an unmanaged source")
					}
				}
				return
			}
			if kind == "owned_live" {
				if observedErr != nil || h.p.starts != 0 || h.s.Status().Process != grant.Plan.Source {
					t.Fatal("cancellation changed the owned live source", observedErr)
				}
				return
			}
			if kind == "owned_lost_start" && !errors.Is(observedErr, ErrUnknown) {
				t.Fatal("lost restart acknowledgement was not retained", observedErr)
			}
			after := h.s.Status()
			if h.p.starts != 1 || after.Operation.Kind != "restart" || after.Operation.Phase != "committed" || after.Current != cancelled.Current || after.Process.SHA256 != grant.Plan.Source.SHA256 || after.Process.LaunchID == grant.Plan.Source.LaunchID {
				t.Fatal("cancelled source did not complete one same-version supervised restart")
			}
			if archived, ok := after.OperationHistory[cancelled.Operation.ID]; !ok || !reflect.DeepEqual(archived, *cancelled.Operation) {
				t.Fatal("original cancellation operation was discarded")
			}
			if archived, ok := after.BoundedHistory[grant.RecoveryAuthorizationID]; !ok || !reflect.DeepEqual(archived, *cancelled.Bounded) {
				t.Fatal("original cancellation window was discarded")
			}
			beforeReplay := h.s.Status()
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil || !reflect.DeepEqual(beforeReplay, h.s.Status()) {
				t.Fatal("original cancellation replay changed the new supervised process", err)
			}
			h.s.Close()
			var err error
			h.s, err = Open(h.cfg, h.opts)
			if err != nil {
				t.Fatal(err)
			}
			beforeReplay = h.s.Status()
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil || !reflect.DeepEqual(beforeReplay, h.s.Status()) {
				t.Fatal("reopened cancellation replay changed the new supervised process", err)
			}
			changed := cancel
			changed.Plan.Authorization.Reason = "different payload"
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), changed, "cancel"); err == nil {
				t.Fatal("archived cancellation accepted changed payload")
			}
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "force"); err == nil || !reflect.DeepEqual(beforeReplay, h.s.Status()) {
				t.Fatal("archived cancellation accepted another action", err)
			}
		})
	}
}

func TestBoundedCancelledRestartRechecksSourceAfterReopen(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		t.Run(map[bool]string{false: "original_process", true: "same_launch_sibling"}[sibling], func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "apply"); err != nil {
				t.Fatal(err)
			}
			cancel := freshBoundedRunnerGrant(h, grant, "req_00000000000000000000000002")
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil {
				t.Fatal(err)
			}
			delete(h.p.live, grant.Plan.Source.PID)
			if err := h.s.Step(t.Context()); err != nil || h.p.starts != 0 {
				t.Fatal("restart intent was not persisted before launch", err)
			}
			h.s.Close()
			var err error
			h.s, err = Open(h.cfg, h.opts)
			if err != nil {
				t.Fatal(err)
			}
			live := grant.Plan.Source
			if sibling {
				live.PID, live.StartTicks = 100, 100
			}
			h.p.live[live.PID] = live
			if err := h.s.Step(t.Context()); err == nil || h.p.starts != 0 {
				t.Fatal("durable restart launched beside a live source", err)
			}
		})
	}
}
