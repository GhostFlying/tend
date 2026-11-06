//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func freshBoundedRunnerGrant(h *harness, old protocol.BoundedRunnerMaintenanceGrant, id string) protocol.BoundedRunnerMaintenanceGrant {
	raw, _ := json.Marshal(old)
	var grant protocol.BoundedRunnerMaintenanceGrant
	_ = json.Unmarshal(raw, &grant)
	grant.Plan.Authorization.RequestID = id
	grant.Plan.Authorization.ExpectedGeneration = h.s.Status().Generation
	grant.Plan.Authorization.ExpectedPhase = h.s.Status().Bounded.Phase
	grant.Plan.Authorization.ExpiresAtMillis = h.now.Add(time.Hour).UnixMilli()
	grant.Plan.ExpectedControlRevision = old.ControlRevision
	grant.ControlRevision = old.ControlRevision + 1
	return grant
}

func commitBoundedRunner(t *testing.T, h *harness, grant protocol.BoundedRunnerMaintenanceGrant) State {
	t.Helper()
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if h.s.Status().Operation.Phase == "committed" {
			return h.s.Status()
		}
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("bounded runner did not commit")
	return State{}
}

func TestBoundedCancelledWindowRequiresNewSourcePlan(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	h.a.deny = true
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "apply"); err != nil {
		t.Fatal(err)
	}
	cancel := freshBoundedRunnerGrant(h, grant, "req_00000000000000000000000002")
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	for _, action := range []string{"apply", "force", "extend", "takeover"} {
		fresh := freshBoundedRunnerGrant(h, cancel, "req_00000000000000000000000003")
		if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), fresh, action); err == nil || !reflect.DeepEqual(before, h.s.Status()) {
			t.Fatalf("cancelled window admitted %s without a new plan: %v", action, err)
		}
	}
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), cancel, "cancel"); err != nil || !reflect.DeepEqual(before, h.s.Status()) {
		t.Fatal("original cancellation replay changed its terminal fact", err)
	}
}

func TestBoundedServerCancelledWindowRequiresNewPlan(t *testing.T) {
	h, p, _ := committedFixture(t)
	cancel := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	if err := h.s.CancelBoundedMaintenance(t.Context(), p, cancel); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	for _, action := range []string{"apply", "force", "extend", "takeover"} {
		fresh := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF2")
		if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, fresh, action); err == nil || !reflect.DeepEqual(before, h.s.Status()) {
			t.Fatalf("cancelled server window admitted %s: %v", action, err)
		}
	}
}

func TestBoundedExtensionRetainsOnlyUnconsumedForcePermission(t *testing.T) {
	for _, action := range []string{"force", "apply"} {
		t.Run(action, func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			process := &boundedRunnerUncertainStop{fakeProcess: h.p}
			h.s.opts.Process = process
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, action); err != nil {
				t.Fatal(err)
			}
			_ = h.s.Step(t.Context())
			if action == "apply" && h.s.Status().Operation.Phase == "stopping" {
				_ = h.s.Step(t.Context())
			}
			fresh := freshBoundedRunnerGrant(h, grant, "req_00000000000000000000000002")
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), fresh, "extend"); err != nil {
				t.Fatal(err)
			}
			boundedHistoryAdvance(h, grant.Plan.Authorization.Timeouts.StopMillis+1)
			_ = h.s.Step(t.Context())
			want := 0
			if action == "force" {
				want = 1
			}
			if process.killCalls != want {
				t.Fatalf("%s+extend changed force scope: kill calls=%d want=%d", action, process.killCalls, want)
			}
		})
	}
}

func TestBoundedReplacementArchivesCompletePreviousWindow(t *testing.T) {
	h, firstGrant := boundedRunnerFixture(t)
	first := commitBoundedRunner(t, h, firstGrant)
	next := freshBoundedRunnerGrant(h, firstGrant, "req_00000000000000000000000002")
	next.RecoveryAuthorizationID = next.Plan.Authorization.RequestID
	next.Plan.RunnerGeneration++
	next.Plan.RunnerInstanceID = first.Process.LaunchID
	next.Plan.Source = first.Process
	next.Plan.Journal.Version = 17
	next.Plan.Target.UpgradeID, next.Plan.Target.ProcessInstanceID = "second-replacement", "second-successor"
	next.Plan.Authorization.Source, next.Plan.Authorization.Target = next.Plan.Source, next.Plan.Target
	next.Plan.Authorization.DataIdentity = &next.Plan.Journal
	next.Plan.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(next.Plan)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), next, "force"); err != nil {
		t.Fatal(err)
	}
	archived, exists := h.s.Status().BoundedHistory[firstGrant.RecoveryAuthorizationID]
	if !exists || !reflect.DeepEqual(archived, *first.Bounded) {
		t.Fatal("a new replacement discarded the complete prior window and physical exit")
	}
	h.s.Close()
	var err error
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "reconcile"} {
		out, err := BoundedCommand(t.Context(), h.cfg, action, "", "", firstGrant.RecoveryAuthorizationID, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		var read struct {
			Bounded protocol.BoundedMaintenanceState `json:"bounded"`
		}
		if err := json.Unmarshal(raw, &read); err != nil || !reflect.DeepEqual(read.Bounded, *first.Bounded) {
			t.Fatal("old request no longer reads its original complete window", action, err)
		}
	}
}

type boundedRestartCapture struct {
	*fakeProcess
	environments []map[string]string
}

func (p *boundedRestartCapture) Start(ctx context.Context, binary string, cfg Config, env map[string]string) (Identity, error) {
	copy := map[string]string{}
	for k, v := range env {
		copy[k] = v
	}
	p.environments = append(p.environments, copy)
	return p.fakeProcess.Start(ctx, binary, cfg, env)
}

func TestBoundedCommittedRunnerUsesExistingCrashSupervision(t *testing.T) {
	for _, kind := range []string{"normal", "lost_start", "lost_start_none", "lost_start_multiple", "live_source", "live_sibling", "changed_binary"} {
		t.Run(kind, func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			first := commitBoundedRunner(t, h, grant)
			process := &boundedRestartCapture{fakeProcess: h.p}
			h.s.opts.Process = process
			h.opts.Process = process
			if kind != "live_source" {
				delete(h.p.live, first.Process.PID)
			}
			if kind == "live_sibling" {
				sibling := first.Process
				sibling.PID, sibling.StartTicks = 100, 100
				h.p.live[sibling.PID] = sibling
			}
			boundedHistoryAdvance(h, time.Hour.Milliseconds()+1)
			err := h.s.Step(t.Context())
			if kind == "live_source" {
				if err != nil || h.p.starts != 1 || len(process.environments) != 0 || !reflect.DeepEqual(first.Operation, h.s.Status().Operation) {
					t.Fatal("healthy committed process was replaced by supervision", err)
				}
				return
			}
			if kind == "live_sibling" {
				if err == nil || h.p.starts != 1 || len(process.environments) != 0 {
					t.Fatal("supervision started while the original launch still had a writer", err)
				}
				return
			}
			if err != nil || h.s.Status().Operation.Kind != "restart" {
				t.Fatal("confirmed crash did not create an independent restart attempt", err)
			}
			if prior, ok := h.s.Status().OperationHistory[first.Operation.ID]; !ok || !reflect.DeepEqual(prior, *first.Operation) {
				t.Fatal("crash supervision discarded the original committed operation")
			}
			if kind == "changed_binary" {
				if err := os.Chmod(first.Current.Binary, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(first.Current.Binary, []byte("changed installed binary"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			lost := kind == "lost_start" || kind == "lost_start_none" || kind == "lost_start_multiple"
			h.p.ambiguousStart = lost
			err = h.s.Step(t.Context())
			if kind == "changed_binary" {
				if err == nil || len(process.environments) != 0 {
					t.Fatal("changed binary reached restart", err)
				}
				return
			}
			if lost {
				if !errors.Is(err, ErrUnknown) {
					t.Fatal("start outcome did not remain unknown", err)
				}
				if kind == "lost_start_none" {
					for pid, candidate := range h.p.live {
						if candidate.LaunchID == h.s.Status().Operation.LaunchID {
							delete(h.p.live, pid)
						}
					}
				}
				if kind == "lost_start_multiple" {
					for _, candidate := range h.p.live {
						if candidate.LaunchID == h.s.Status().Operation.LaunchID {
							candidate.PID, candidate.StartTicks = 200, 200
							h.p.live[candidate.PID] = candidate
							break
						}
					}
				}
				attempt := h.s.Status().Operation.LaunchID
				h.s.Close()
				h.s, err = Open(h.cfg, h.opts)
				if err != nil {
					t.Fatal(err)
				}
				if kind != "lost_start" {
					for range 2 {
						if err := h.s.Step(t.Context()); !errors.Is(err, ErrUnknown) || h.p.starts != 2 || len(process.environments) != 1 || h.s.Status().Operation.LaunchID != attempt {
							t.Fatal("ambiguous restart repeated or changed its original launch", err)
						}
					}
					return
				}
				if err := h.s.Step(t.Context()); err != nil {
					t.Fatal("original restart was not reconciled", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after := h.s.Status()
			if h.p.starts != 2 || len(process.environments) != 1 || after.Process.LaunchID == first.Process.LaunchID || after.Process == first.Process {
				t.Fatal("supervision did not use exactly one fresh process identity")
			}
			for _, key := range []string{"TEND_UPDATER_MAINTENANCE", "TEND_BOUNDED_RECOVERY_REQUEST_ID", "TEND_UPGRADE_ID"} {
				if process.environments[0][key] != "" {
					t.Fatal("ordinary restart inherited force startup authority", key)
				}
			}
			receipt := ProcessReceipt(after)
			if receipt == nil || !receipt.CommittedConfirmed || receipt.Binding != grant.Plan.Target || receipt.CurrentProcessInstanceID != after.Process.LaunchID || receipt.CurrentSHA256 != grant.Plan.Target.ArtifactSHA256 {
				t.Fatal("restart lost the immutable binding or actual successor receipt", receipt)
			}
			if !reflect.DeepEqual(after.Bounded, first.Bounded) || filepath.Clean(after.Current.Binary) != filepath.Clean(first.Current.Binary) {
				t.Fatal("ordinary supervision reset the old force window or installed bytes")
			}
		})
	}
}

type boundedLostKill struct{ *boundedRunnerUncertainStop }

func (p *boundedLostKill) Crash(context.Context, Identity, time.Duration) error {
	p.killCalls++
	return ErrUnknown
}

func TestBoundedForceExtensionNeverReplaysPriorKill(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	process := &boundedLostKill{boundedRunnerUncertainStop: &boundedRunnerUncertainStop{fakeProcess: h.p}}
	h.s.opts.Process = process
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	_ = h.s.Step(t.Context())
	boundedHistoryAdvance(h, grant.Plan.Authorization.Timeouts.StopMillis+1)
	_ = h.s.Step(t.Context())
	if process.killCalls != 1 {
		t.Fatal("original force did not issue its one permitted kill")
	}
	fresh := freshBoundedRunnerGrant(h, grant, "req_00000000000000000000000002")
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), fresh, "extend"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_ = h.s.Step(t.Context())
	}
	if process.killCalls != 1 || h.p.starts != 0 {
		t.Fatal("new budget repeated the original uncertain kill")
	}
}
