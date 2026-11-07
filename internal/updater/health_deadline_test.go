//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type deadlineAuthority struct {
	*fakeAuthority
	health func(context.Context, Binding) (Health, error)
}

type deadlineProcess struct {
	*fakeProcess
	inspectErr error
}

func (p deadlineProcess) Inspect(context.Context, int) (Identity, error) {
	return Identity{}, p.inspectErr
}

func TestHealthDeadlineSurvivesReopenAndAllowsOnlyOriginalLateHealth(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.a.unconfirmed = true
	if err := h.s.Step(t.Context()); err == nil || strings.Contains(err.Error(), "health deadline exceeded") {
		t.Fatal("first read before expiry lost original result", err)
	}
	original := h.s.Status()
	window := *original.Operation.HealthWindow
	h.now = h.now.Add(11 * time.Second)
	h.boot += 11000
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("unconfirmed health did not time out")
	}
	timeout := h.s.Status().Operation.HealthWindow.TimedOutAtMillis
	h.reopen(t)
	h.a.unconfirmed = false
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("one late sample fabricated commitment")
	}
	if err := h.s.Step(t.Context()); err != nil {
		t.Fatal("two real late samples cannot reconcile original candidate", err)
	}
	after := h.s.Status()
	if after.Operation.Phase != "committed" || after.Operation.Binding != original.Operation.Binding || after.Process != original.Process || after.Operation.HealthWindow.DeadlineMillis != window.DeadlineMillis || after.Operation.HealthWindow.DeadlineMono != window.DeadlineMono || after.Operation.HealthWindow.TimedOutAtMillis != timeout || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("late read renewed/replaced original operation", after.Operation)
	}
}

func TestHealthDeadlineIncludesInspectAndConfigurationErrors(t *testing.T) {
	for _, failure := range []string{"inspect", "configuration", "launch_identity"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, "checking")
			h.now = h.now.Add(11 * time.Second)
			h.boot += 11000
			switch failure {
			case "inspect":
				h.s.opts.Process = deadlineProcess{h.p, ErrUnknown}
			case "configuration":
				h.cfg.ServiceConfig = filepath.Join(t.TempDir(), "configuration")
				h.s.cfg.ServiceConfig = h.cfg.ServiceConfig
				if err := os.WriteFile(h.cfg.ServiceConfig, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				h.s.state.Operation.Configuration = &StagedConfiguration{Target: protocol.RunnerConfiguration{SHA256: h.s.Status().Operation.Candidate.Artifact.SHA256}}
			case "launch_identity":
				id := h.p.live[h.s.Status().Operation.CandidateProcess.PID]
				id.LaunchID = "unrelated"
				h.p.live[id.PID] = id
			}
			if err := h.s.Step(t.Context()); err == nil || !strings.Contains(err.Error(), "health deadline exceeded") {
				t.Fatal("early error hid elapsed original deadline", err)
			}
			if h.s.Status().Operation.Phase != "checking" {
				t.Fatal("failed read changed installation outcome")
			}
		})
	}
}

func TestHealthReadReconcilesOriginalOwnedExitDespiteAuthorityFailure(t *testing.T) {
	h := newHarness(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	h.bundle = makeBundle(t, raw)
	h.cfg.Args = []string{"-test.run=^TestBoundedRunnerDelayedExitHelper$"}
	h.s.cfg.Args = h.cfg.Args
	process := &boundedOwnedExitProcess{fakeProcess: h.p, t: t, firstExit: true, returnIdentity: true}
	h.s.opts.Process = process
	h.upgrade(t)
	h.until(t, "checking")
	op := h.s.Status().Operation
	proof := awaitOwnedLaunchExit(t, h.s.cfg, op.LaunchID, op.Candidate.Artifact.SHA256)
	h.a.failHealth = true
	h.now = h.now.Add(11 * time.Second)
	h.boot += 11000
	if err := h.s.Step(t.Context()); err == nil || !strings.Contains(err.Error(), "health deadline exceeded") {
		t.Fatal("physical exit did not retain elapsed deadline", err)
	}
	after := h.s.Status().Operation
	if after.Phase != "launch_exited" || after.LaunchExit == nil || *after.LaunchExit != *proof || after.Binding != op.Binding || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("original child wait was lost behind failed authority", after)
	}
}

func (a deadlineAuthority) Health(ctx context.Context, b Binding) (Health, error) {
	return a.health(ctx, b)
}

func TestHealthDeadlineIncludesUnconfirmedAndAuthorityErrors(t *testing.T) {
	for _, failure := range []string{"unconfirmed", "authority", "nil", "binding"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, "checking")
			h.now = h.now.Add(11 * time.Second)
			h.boot += 11000
			switch failure {
			case "nil":
				h.s.opts.Authority = nil
			case "authority":
				h.a.failHealth = true
			case "unconfirmed":
				h.a.unconfirmed = true
			case "binding":
				h.s.opts.Authority = deadlineAuthority{fakeAuthority: h.a, health: func(context.Context, Binding) (Health, error) {
					return Health{ServerConfirmed: true, ServerRevision: 1}, nil
				}}
			}
			before := h.s.Status()
			err := h.s.Step(t.Context())
			if err == nil || !strings.Contains(err.Error(), "health deadline exceeded") {
				t.Fatalf("expired %s did not report the fixed health deadline: %v", failure, err)
			}
			if after := h.s.Status(); after.Operation.Phase != "checking" || after.Process != before.Process || h.p.starts != 1 || h.p.stops != 1 {
				t.Fatal("deadline changed the original candidate or performed another process action", after.Operation)
			}
		})
	}
}

func TestHealthReadUsesRemainingBudgetAndRechecksAfterReturn(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.now = h.now.Add(9 * time.Second)
	h.boot += 9000
	h.s.opts.Authority = deadlineAuthority{fakeAuthority: h.a, health: func(ctx context.Context, _ Binding) (Health, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 1100*time.Millisecond {
			t.Error("health read is not bounded by the original remaining second")
		}
		h.now = h.now.Add(2 * time.Second)
		h.boot += 2000
		return Health{}, errors.New("original read failed after expiry")
	}}
	if err := h.s.Step(t.Context()); err == nil || !strings.Contains(err.Error(), "health deadline exceeded") || !strings.Contains(err.Error(), "original read failed") {
		t.Fatal("read crossing the deadline hid its elapsed budget or original cause", err)
	}
}
