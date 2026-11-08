package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/testkit"
)

type environmentTestSession struct {
	harness.Session
	idle    bool
	adopted int
}

type originalThreadOnlySession struct {
	harness.Session
	resumes, starts int
}

func (*originalThreadOnlySession) EnvironmentRequiresOriginalThread() bool { return true }
func (s *originalThreadOnlySession) ResumeThread(context.Context, string, harness.ThreadConfig) (harness.Thread, error) {
	s.resumes++
	return nil, harness.ErrRejected
}
func (s *originalThreadOnlySession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts++
	return nil, errors.New("unexpected native migration")
}

func TestEnvironmentResumeRefusalCannotMigrateNativeIdentity(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := e.m.waitThread(ctx); err != nil {
		t.Fatal(err)
	}
	session := &originalThreadOnlySession{}
	if _, err := e.m.openThread(ctx, session); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("environment resume refusal entered ordinary recovery", err)
	}
	if session.resumes != 1 || session.starts != 0 {
		t.Fatal("adoption replaced the original thread")
	}
}

type environmentHandoffSession struct{ *handoffTestSession }

func (*environmentHandoffSession) EnvironmentRequiresOriginalThread() bool { return true }

func TestEnvironmentResumeKeepsToolsetUpgradeQuarantined(t *testing.T) {
	f := newHandoffFixture(t)
	quarantined := false
	f.session.resumeHook = func(_ context.Context, _ string, cfg harness.ThreadConfig) {
		f.e.m.mu.Lock()
		quarantined = f.e.m.maintenanceBlocked
		f.e.m.mu.Unlock()
		if cfg.OnToolCall == nil || cfg.OnToolCall(t.Context(), harness.ToolCall{ThreadID: f.source.ID(), Tool: "obsolete-tool"}).Success {
			t.Error("old-schema callback reached the new parser during adoption")
		}
	}
	target, err := f.e.m.openThread(t.Context(), &environmentHandoffSession{f.session})
	if err != nil || !quarantined || target.ID() == f.source.ID() {
		t.Fatal("resume-only startup bypassed normal toolset maintenance", err)
	}
}

func TestEnvironmentConsultationCannotUseMainHandoffContext(t *testing.T) {
	for _, matches := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching_schema", false: "changed_schema"}[matches], func(t *testing.T) {
			f := newHandoffFixture(t)
			f.e.m.named = &hostedconfig.Selection{Purpose: agentconfig.Consultation}
			f.e.m.cfg.MaintenanceEnabled = false
			hash, err := f.e.m.currentToolsetHash()
			if err != nil {
				t.Fatal(err)
			}
			if !matches {
				hash = "previous-consultation-schema"
			}
			if err = f.e.app.SetMainThreadWithToolset(t.Context(), f.e.m.bindingInstance(), f.source.ID(), hash); err != nil {
				t.Fatal(err)
			}
			before, forks, _ := f.session.counts()
			thread, err := f.e.m.openThread(t.Context(), &environmentHandoffSession{f.session})
			if matches && (err != nil || thread.ID() != f.source.ID()) {
				t.Fatal("consultation did not resume its original thread", err)
			}
			if !matches && err == nil {
				t.Fatal("changed consultation schema bypassed quarantine")
			}
			after, afterForks, _ := f.session.counts()
			if after != before || afterForks != forks {
				t.Fatal("consultation entered global Main history migration")
			}
		})
	}
}

func (*environmentTestSession) EnvironmentPending() bool { return true }
func (s *environmentTestSession) EnvironmentQuiescent(context.Context, string) (bool, error) {
	return s.idle, nil
}
func (s *environmentTestSession) AdoptEnvironment(context.Context) error { s.adopted++; return nil }

func TestEnvironmentUpdateWaitsForConfirmedIdleAndClosesAdmission(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	th, err := e.m.waitThread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.m.mu.Lock()
	session := &environmentTestSession{Session: e.m.session, idle: true}
	e.m.session = session
	e.m.active = &turn{}
	e.m.mu.Unlock()
	if changed, err := e.m.environmentRestart(ctx, session, th); err != nil || changed {
		t.Fatal("running turn allowed adoption", err)
	}
	e.m.mu.Lock()
	e.m.active = nil
	e.m.maintenanceBlocked = true
	e.m.mu.Unlock()
	if changed, err := e.m.environmentRestart(ctx, session, th); err != nil || changed {
		t.Fatal("active maintenance allowed adoption", err)
	}
	e.m.mu.Lock()
	e.m.maintenanceBlocked = false
	e.m.mu.Unlock()
	e.m.workMu.Lock()
	changed, err := e.m.environmentRestart(ctx, session, th)
	e.m.workMu.Unlock()
	if err != nil || changed {
		t.Fatal("delivery gate allowed adoption", err)
	}
	session.idle = false
	if changed, err = e.m.environmentRestart(ctx, session, th); err != nil || changed {
		t.Fatal("unsettled native operation allowed adoption", err)
	}
	session.idle = true
	if changed, err = e.m.environmentRestart(ctx, session, th); err != nil || !changed || session.adopted != 1 {
		t.Fatal("confirmed idle did not adopt", err)
	}
	e.m.mu.Lock()
	detached := e.m.session == nil && e.m.thread == nil
	e.m.mu.Unlock()
	if !detached {
		t.Fatal("new delivery can still enter old native session")
	}
}
