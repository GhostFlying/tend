package mainthread

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
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

type frozenEnvironmentSession struct {
	*environmentTestSession
	closed int
	pin    protocol.ExtensionExecutionPin
}

func (s *frozenEnvironmentSession) EnvironmentPin() protocol.ExtensionExecutionPin { return s.pin }

func (s *frozenEnvironmentSession) Close() error { s.closed++; return nil }

func TestFrozenEnvironmentRestorationWaitsForIdleBeforeReopening(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	th, err := e.m.waitThread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.stop()
	zero := int64(0)
	category, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "categories.put", Target: protocol.Target{Type: "category", ID: "planner"}, ExpectedControlRevision: &zero, Payload: []byte(`{"title":"Planner","state":"active"}`)}, app.PutAgentCategory{})
	if err != nil || category.Status != protocol.OpSucceeded {
		t.Fatal(category, err)
	}
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "fixture", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "native", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "fixture", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := e.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "hosted.configure", Target: protocol.Target{Type: "hosted_configuration"}, ExpectedControlRevision: &zero, Payload: []byte(fmt.Sprintf(`{"revision":1,"hash":%q}`, prepared.Hash()))}, app.AcceptHostedConfiguration{Prepared: prepared})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	frozen, err := prepared.Select("planner", agentconfig.Main)
	if err != nil {
		t.Fatal(err)
	}
	frozen.AppliedModel = "original-model"
	frozen.Extensions = protocol.ExtensionExecutionPin{ReleaseID: "original"}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertHostedExecution(ctx, store.HostedExecution{ID: "original-repair", ConversationID: "main", Selection: frozen})
	}); err != nil {
		t.Fatal(err)
	}
	current := frozen
	current.Extensions.ReleaseID = "latest"
	e.m.named = &current
	s := &frozenEnvironmentSession{environmentTestSession: &environmentTestSession{idle: true}, pin: current.Extensions}
	e.m.mu.Lock()
	e.m.session, e.m.thread = s, th
	e.m.archiveStarting, e.m.archiveTeardown = false, false
	e.m.active = &turn{}
	e.m.mu.Unlock()
	e.m.workMu.Lock()
	defer e.m.workMu.Unlock()
	if _, _, err := e.m.turnModel(ctx, th, "original-repair"); protocol.CodeOf(err) != protocol.CodeInvalidTransition || s.closed != 0 {
		t.Fatal(err)
	}
	e.m.active = nil
	e.m.maintenanceBlocked = true
	if _, _, err := e.m.turnModel(ctx, th, "original-repair"); protocol.CodeOf(err) != protocol.CodeInvalidTransition || s.closed != 0 {
		t.Fatal(err)
	}
	e.m.maintenanceBlocked = false
	s.idle = false
	if _, _, err := e.m.turnModel(ctx, th, "original-repair"); protocol.CodeOf(err) != protocol.CodeInvalidTransition || s.closed != 0 {
		t.Fatal(err)
	}
	s.idle = true
	if _, _, err := e.m.turnModel(ctx, th, "original-repair"); protocol.CodeOf(err) != protocol.CodeInvalidTransition || s.closed != 1 {
		t.Fatal(err, s.closed)
	}
	if e.m.session != nil || e.m.thread != nil {
		t.Fatal("old process still admits input")
	}
}
