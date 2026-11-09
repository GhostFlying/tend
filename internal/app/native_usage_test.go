package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func nativeSample(now int64) protocol.TokenUsageSample {
	return protocol.TokenUsageSample{RecordID: "ledger-record", Provider: "traex", NativeVersion: "test", SessionID: "native-session", Source: "native-ledger/traex/native-thread", RequestID: "resp_1", ThreadID: "native-thread", TurnID: "native-turn", Revision: 1, Settlement: "final", Profile: "traex-native-ledger", OccurredAt: usageNumber(now - 1000), Input: usageNumber(100), Output: usageNumber(10), Total: usageNumber(110)}
}

func TestResolvedChildUsageCannotCrossHostOwnership(t *testing.T) {
	h := newHarness(t)
	h.withHosts("linux-1", "linux-2")
	h.createTask(owner, createTaskPayload{Title: "local", Spec: testSpec})
	local, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no local run")
	}
	h.createTask(owner, createTaskPayload{Title: "foreign", Spec: testSpec})
	foreign, ok, _ := h.claim("linux-2")
	if !ok {
		t.Fatal("no foreign run")
	}
	for _, source := range []struct{ host, run, thread string }{{"linux-1", local.RunID, "local-root"}, {"linux-2", foreign.RunID, "foreign-thread"}} {
		payload := protocol.NativeUsageRegistration{RunID: source.run, ThreadID: source.thread, SessionID: "shared-session", Provider: "traex"}
		h.exec(runner(source.host), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: source.host}, payload, nil), RegisterNativeUsage{})
	}
	sample := nativeSample(h.clock.Now().UnixMilli())
	sample.ThreadID, sample.Source, sample.SessionID = "foreign-thread", "native-ledger/traex/foreign-thread", "shared-session"
	payload := protocol.UsageUpload{RunID: local.RunID, RootThreadID: "local-root", SessionID: sample.SessionID, Samples: []protocol.TokenUsageSample{sample}}
	op, err := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil), UploadNativeUsage{})
	if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
		t.Fatal("foreign attribution was admitted", op, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("rejected batch left usage facts", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageInventoryIgnoresHistoricalEventsWithoutThreadIdentity(t *testing.T) {
	h, as := messageRun(t)
	_, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "item.completed", Body: json.RawMessage(`{"type":"agentMessage","text":"done"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if sources, err := h.app.UsageTargets(t.Context(), runner("linux-1")); err != nil || len(sources) != 0 {
		t.Fatal(sources, err)
	}
}

func TestNativeSourceRegistrationRejectsForeignHostAndUsesStableRequest(t *testing.T) {
	h, as := messageRun(t)
	payload := protocol.NativeUsageRegistration{RunID: as.RunID, ThreadID: "native-thread", SessionID: "native-session", Provider: "traex"}
	for _, principal := range []protocol.Principal{owner, runner("other-host"), runPrincipal(as.RunID, as.Role)} {
		env := h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: principal.Subject}, payload, nil)
		if _, err := h.app.Execute(t.Context(), principal, env, RegisterNativeUsage{}); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("foreign source authority accepted", principal, err)
		}
	}
	env := h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil)
	for range 2 {
		op, err := h.app.Execute(t.Context(), runner("linux-1"), env, RegisterNativeUsage{})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_sources WHERE run_id=?`, as.RunID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal(count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultMainLocalSourceUsesCanonicalConversation(t *testing.T) {
	h, _ := messageRun(t)
	source := LocalUsageSource{ID: "main", Source: json.RawMessage(`{"thread_id":"native-thread","session_id":"native-session","provider":"traex"}`), Cursors: json.RawMessage(`{}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	refreshLocalUsageFixture(t, h.app, &source)
	if err := h.app.CommitLocalUsage(t.Context(), source, []protocol.TokenUsageSample{nativeSample(h.clock.Now().UnixMilli())}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, subject := range []string{"", "conversation:main"} {
			view, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", subject, 0, h.clock.Now())
			if err != nil {
				return err
			}
			if view.Totals.Total != 110 || len(view.Roles) != 1 || view.Roles[0].Name != "main" {
				t.Fatal(subject, view)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.StopLocalUsage(t.Context(), "", ""); err != nil {
		t.Fatal(err)
	}
	if sources, err := h.app.LocalUsageSources(t.Context(), "main"); err != nil || len(sources) != 1 || !sources[0].WriterStopped {
		t.Fatal(sources, err)
	}
}

func TestManagerPrivateSourceRegistrationUsesHistoricalHostAuthority(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate"}, rev(issue.Revision)), RequestIssueManager{})
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment == nil {
		t.Fatal(op, err)
	}
	as := *result.Assignment
	h.clock.Advance(time.Hour)
	registration := protocol.NativeUsageRegistration{RunID: as.RunID, ThreadID: "native-thread", SessionID: "native-session", Provider: "traex", Purpose: "internal_summary"}
	op = h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sample := nativeSample(h.clock.Now().UnixMilli())
	payload := protocol.UsageUpload{RunID: as.RunID, RootThreadID: sample.ThreadID, SessionID: sample.SessionID, Samples: []protocol.TokenUsageSample{sample}}
	op = h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil), UploadNativeUsage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("registered private summary was not uploadable", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var purpose string
		return tx.Q().QueryRowContext(t.Context(), `SELECT purpose FROM token_usage_sources WHERE run_id=? AND thread_id=?`, as.RunID, sample.ThreadID).Scan(&purpose)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServerLocalRunUsageConservesGlobalAndRunTotalsAcrossRestart(t *testing.T) {
	h, as := messageRun(t)
	source := LocalUsageSource{ID: "central", RunID: as.RunID, Source: json.RawMessage(`{"thread_id":"native-thread","session_id":"native-session","provider":"traex"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	sample := nativeSample(h.clock.Now().UnixMilli())
	for range 2 {
		refreshLocalUsageFixture(t, h.app, &source)
		if err := h.app.CommitLocalUsage(t.Context(), source, []protocol.TokenUsageSample{sample}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, subject := range []string{"", "run:" + as.RunID} {
			view, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", subject, 0, h.clock.Now())
			if err != nil {
				return err
			}
			if view.Totals.Total != 110 || len(view.Sessions) != 1 || view.Records[0].Attribution.RunID != as.RunID {
				t.Fatal(view)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeUsageUploadHistoricalDedupAndLegacyAlias(t *testing.T) {
	h, as := messageRun(t)
	register, _ := json.Marshal(map[string]string{"thread_id": "native-thread", "session_id": "native-session", "provider": "traex"})
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "usage.source", Body: register}}); err != nil {
		t.Fatal(err)
	}
	s := nativeSample(h.clock.Now().UnixMilli())
	old := s
	old.RecordID = ""
	old.SessionID = ""
	old.Source = "traex/response/native-thread"
	old.OccurredAt = nil
	if err := h.app.RecordTokenUsage(t.Context(), "main", old); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	upload := func(sample protocol.TokenUsageSample) {
		t.Helper()
		p := protocol.UsageUpload{RunID: as.RunID, RootThreadID: "native-thread", Samples: []protocol.TokenUsageSample{sample}, Coverage: "syncing"}
		op, err := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), UploadNativeUsage{})
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
	}
	upload(s)
	upload(s)
	read := func() protocol.TokenUsageView {
		t.Helper()
		var v protocol.TokenUsageView
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := read(); v.Totals.Requests != 1 || v.Totals.Total != 110 || len(v.Sessions) != 1 || v.Sessions[0].ID != "native-session" || v.Records[0].Attribution.TaskID != as.TaskID {
		t.Fatalf("legacy and canonical double count: %+v", v)
	}
	// Synthetic response IDs repeat in later turns; a distinct ledger identity
	// must preserve the second request even with the same response identifier.
	next := s
	next.RecordID = "second-record"
	next.TurnID = "second-turn"
	upload(next)
	if v := read(); v.Totals.Requests != 2 || v.Totals.Total != 220 {
		t.Fatal(v)
	}
	// Same immutable record with contradictory counters is quarantined.
	s.Total = usageNumber(999)
	upload(s)
	if v := read(); v.Conflicts != 1 || v.Totals.Total != 220 {
		t.Fatal(v)
	}
}

func TestNativeUsageUploadRejectsForeignHostAndUnboundSource(t *testing.T) {
	h, as := messageRun(t)
	sample := nativeSample(h.clock.Now().UnixMilli())
	for _, host := range []string{"linux-1", "linux-2"} {
		p := protocol.UsageUpload{RunID: as.RunID, RootThreadID: "unregistered", Samples: []protocol.TokenUsageSample{sample}}
		op, err := h.app.Execute(t.Context(), runner(host), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: host}, p, nil), UploadNativeUsage{})
		if err == nil && op.Error == nil {
			t.Fatal("accepted unbound source", host)
		}
	}
}

func TestNativeModelEnrichmentPreservesConsumptionAndRejectsNativeChanges(t *testing.T) {
	h, _ := messageRun(t)
	s := nativeSample(h.clock.Now().UnixMilli())
	s.Model = "frozen-model"
	s.ModelSource = "frozen-execution"
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{s}); err != nil {
		t.Fatal(err)
	}
	native := s
	native.Model = "native-model"
	native.ModelSource = "native-turn-context"
	for _, sample := range []protocol.TokenUsageSample{native, s, native} {
		if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
			t.Fatal(err)
		}
	}
	var total, conflicts int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		v, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		if err == nil {
			total = v.Totals.Total
			conflicts = v.Conflicts
			if len(v.Records) != 1 || v.Records[0].Model != "native-model" {
				t.Fatal(v)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if total != 110 || conflicts != 0 {
		t.Fatal(total, conflicts)
	}
	native.Model = "contradictory-native-model"
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{native}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT conflict FROM token_usage_current WHERE source=? AND request_id=?`, native.Source, native.RecordID).Scan(&conflicts)
	}); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 {
		t.Fatal("native model contradiction was overwritten")
	}
}

func TestUnmatchedLegacyOverlapPreservesOldContribution(t *testing.T) {
	h, _ := messageRun(t)
	native := nativeSample(h.clock.Now().UnixMilli())
	old := native
	old.RecordID = ""
	old.Source = "legacy-response"
	old.Total = usageNumber(90)
	old.Input = usageNumber(80)
	if err := h.app.RecordTokenUsage(t.Context(), "main", old); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{native}); err != nil {
		t.Fatal(err)
	}
	read := func() protocol.TokenUsageView {
		var v protocol.TokenUsageView
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := read(); v.Totals.Total != 90 || v.Conflicts != 1 {
		t.Fatal(v)
	}
	// A later ordinal can prove which request the synthetic legacy ID described.
	match := native
	match.RecordID = "later-ordinal"
	match.Total = usageNumber(90)
	match.Input = usageNumber(80)
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{match}); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.Totals.Total != 200 || v.Totals.Requests != 2 || v.Conflicts != 0 {
		t.Fatal(v)
	}
}

func TestSharedNativeSessionUsesRegisteredExecutionNotUploader(t *testing.T) {
	h, parent := messageRun(t)
	h.createTask(owner, createTaskPayload{Title: "child execution", Spec: testSpec})
	child, ok, _ := h.claim("linux-1")
	if !ok || child.RunID == parent.RunID {
		t.Fatal("missing second execution", child)
	}
	for _, source := range []struct {
		as     protocol.Assignment
		thread string
	}{{parent, "parent"}, {child, "child"}} {
		body, _ := json.Marshal(map[string]string{"thread_id": source.thread, "session_id": "shared", "provider": "traex"})
		if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(source.as.RunID, source.as.Role), source.as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "usage.source", Body: body}}); err != nil {
			t.Fatal(err)
		}
	}
	one := nativeSample(h.clock.Now().UnixMilli())
	one.RecordID = "parent-request"
	one.SessionID = "shared"
	one.ThreadID = "parent"
	one.Source = "native-ledger/traex/parent"
	two := one
	two.RecordID = "child-request"
	two.ThreadID = "child"
	two.Source = "native-ledger/traex/child"
	upload := func(as protocol.Assignment, root string, samples []protocol.TokenUsageSample) {
		t.Helper()
		input := protocol.UsageUpload{RunID: as.RunID, RootThreadID: root, SessionID: "shared", Samples: samples, Coverage: "caught_up"}
		op, err := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: "linux-1"}, input, nil), UploadNativeUsage{})
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
	}
	upload(parent, "parent", []protocol.TokenUsageSample{one, two})
	upload(child, "child", []protocol.TokenUsageSample{two})
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		all, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		if err != nil {
			return err
		}
		if all.Conflicts != 0 || all.Totals.Total != 220 || len(all.Sessions) != 1 || len(all.Sessions[0].Executions) != 2 {
			t.Fatal(all)
		}
		for _, as := range []protocol.Assignment{parent, child} {
			v, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "run:"+as.RunID, 0, h.clock.Now())
			if err != nil {
				return err
			}
			if v.Totals.Total != 110 || v.Records[0].Attribution.TaskID != as.TaskID {
				t.Fatal(v)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativePreviewRollsBackAndIncludesReleasedEarlierPages(t *testing.T) {
	h, _ := messageRun(t)
	if err := h.app.RegisterAgentThread(t.Context(), "main", "native-thread"); err != nil {
		t.Fatal(err)
	}
	sample := nativeSample(h.clock.Now().UnixMilli())
	sample.Model = "native-model"
	sample.ModelSource = "native-turn-context"
	old := sample
	old.RecordID = ""
	old.Source = "legacy-response"
	old.Input = usageNumber(80)
	old.Total = usageNumber(90)
	if err := h.app.RecordTokenUsage(t.Context(), "main", old); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
		t.Fatal(err)
	}
	match := sample
	match.RecordID = "matching-later-page"
	match.Input = usageNumber(80)
	match.Total = usageNumber(90)
	input := protocol.UsageUpload{RootThreadID: "native-thread", ConversationID: "main", SessionID: sample.SessionID, Samples: []protocol.TokenUsageSample{match}}
	var before int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := h.app.PreviewUsage(t.Context(), owner, input)
	if err != nil || preview.TokenDelta != 110 || preview.New != 1 || preview.Conflicts != 0 {
		t.Fatal(preview, err)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var after int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&after); err != nil {
			return err
		}
		if after != before {
			t.Fatal("preview committed facts")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload := protocol.UsageReconcile{RootThreadID: input.RootThreadID, ConversationID: "main", SessionID: input.SessionID, Samples: input.Samples}
	op, err := h.app.Execute(t.Context(), owner, h.env("token_usage.reconcile", protocol.Target{Type: "conversation", ID: "main"}, payload, nil), ReconcileConversationUsage{})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		if err == nil && (view.Totals.Total != 200 || view.Conflicts != 0) {
			t.Fatal(view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
