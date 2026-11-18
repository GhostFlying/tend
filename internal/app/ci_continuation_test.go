package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/maintenance"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFault_CIWaitRestoresOriginalOwnerSourceAndVerifiesProcessing(t *testing.T) {
	for _, mode := range []string{"reply-source", "reply-batch", "notify-source", "notify-unrelated", "parent-paused"} {
		t.Run(mode, func(t *testing.T) { testOwnerCIContinuation(t, mode) })
	}
}

func testOwnerCIContinuation(t *testing.T, mode string) {
	h, rt := managerClaimHarness(t)
	ref := protocol.IssueDelivery{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: 7}
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Wait for CI", Project: "default", Intent: "Decide on the current candidate", Deliveries: []protocol.IssueDelivery{ref}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), historicalAdmittedIssueFixture{}))
	if mode == "parent-paused" {
		parent := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Parent responsibility", Project: "default", Intent: "Own the child scope"}}
		root := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, parent, nil), historicalAdmittedIssueFixture{}))
		reparent := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, ParentIssueID: root.IssueID, ExpectedParentRevision: rev(root.Revision)}
		issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", reparent))
		if issue.ParentIssueID != root.IssueID {
			t.Fatal("parent relation missing")
		}
	}
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Track the candidate"}, rev(issue.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, rt)
	// Provider snapshots are the external input. Wait, physical stop, wake,
	// repair, owner source reading and publication follow ordinary entry points.
	snapshot := func(state string) {
		t.Helper()
		err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), ref, protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "candidate", BaseSHA: "base", TargetSHA: "target", CI: &protocol.DeliveryCI{HeadSHA: "candidate", State: state}}, CheckedAt: h.clock.Now().UnixMilli()})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot("pending")
	op = h.runAs(as, protocol.OpRunsWaitExternal, protocol.WaitExternalPayload{WaitKey: "ci", Kind: "checks", IssueID: issue.IssueID, TargetID: ref.DeliveryID, Reason: "Await provider result", Recovery: "Handle the CI result"}, WaitExternal{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var wait protocol.ExternalWait
	if err := json.Unmarshal(op.Result, &wait); err != nil {
		t.Fatal(err)
	}
	stopOwnerClaim(t, h, as)
	snapshot("failure")
	h.clock.Advance(6 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	read := func() protocol.CIContinuation {
		t.Helper()
		var got protocol.CIContinuation
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			got, err = views.OwnerCIContinuation(t.Context(), tx.Q(), as.TaskID, as.RunID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	controller, native, probe := ciMaintenance(t, h)
	check := func() {
		t.Helper()
		req := protocol.MaintenanceRequest{RequestID: h.env("maintenance.command", protocol.Target{}, struct{}{}, nil).RequestID, ExpectedRevision: controller.Status().Revision}
		if _, err := controller.Command(t.Context(), "check", req); err != nil {
			t.Fatal(err)
		}
		if err := controller.Check(t.Context(), probe); err != nil {
			t.Fatal(err)
		}
		if s := controller.Status(); s.CIError != "" {
			t.Fatal(s.CIError)
		}
	}
	check()
	if len(controller.Status().Cases) != 0 {
		t.Fatal("normal CI wake opened a case", controller.Status().Cases)
	}
	f := read()
	if f.State != "notified" {
		t.Fatalf("normal wake: %+v", f)
	}
	// Inject the single missing business artifact. Keep the original result,
	// event, source message, action and all owner controls intact.
	err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `DELETE FROM background_sources WHERE message_id=? AND event_id=?`, f.SourceMessage, f.SourceEvent)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	next := ownerClaim(t, h, rt)
	if next.TaskID != as.TaskID || next.ExternalWaitContinuation == nil {
		t.Fatal(next)
	}
	check()
	if len(controller.Status().Cases) != 0 {
		t.Fatal("active owner opened a case", controller.Status().Cases)
	}
	if got := read(); got.State == "missing" {
		t.Fatal("working owner was classified as lost", got)
	}
	drain := func(capture bool) {
		t.Helper()
		for n := 0; n < 12; n++ {
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(as.Manager.ConversationID))
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
			turn := startHostedReview(t, h, as.Manager.ConversationID, delivery)
			if capture {
				principal := protocol.Principal{Kind: protocol.PrincipalRun, Role: protocol.RoleIssueManager, Subject: turn.Hosted.RunnerRunID, Thread: turn.ThreadID, ConversationID: as.Manager.ConversationID}
				cmd := BackgroundCommand{Action: "sources", MessageID: turn.MessageID, TurnID: turn.TurnID, DeliveryID: turn.RequestID}
				other := principal
				other.Subject = as.RunID
				if _, err := h.app.Execute(t.Context(), other, h.env(cmd.Operation(), protocol.Target{Type: "background", ID: turn.MessageID}, map[string]any{}, nil), cmd); protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("different Run read the source")
				}
				op := h.exec(principal, h.env(cmd.Operation(), protocol.Target{Type: "background", ID: turn.MessageID}, map[string]any{}, nil), cmd)
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				var page protocol.BackgroundSourcePage
				if err := json.Unmarshal(op.Result, &page); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, s := range page.Sources {
					if s.MessageID == f.SourceMessage && s.EventID == f.SourceEvent {
						found = true
					}
				}
				if !found {
					t.Fatalf("wrong owner source: %+v", page)
				}
				reply := protocol.CoordinationReply{Body: "The original candidate CI failed. Inspect the failure before integration.", Purpose: "progress", ReplyTo: turn.MessageID}
				if mode == "reply-source" {
					reply.ReplyTo = f.SourceMessage
				}
				target := protocol.Target{Type: "logical_turn", ID: turn.RequestID}
				if mode == "notify-source" || mode == "notify-unrelated" {
					notice := protocol.MessagingPublication{Body: reply.Body, Purpose: "progress", Reason: "Report verified work"}
					if mode == "notify-source" {
						notice.SourceMessageIDs = []string{f.SourceMessage}
					}
					op = hostedEffect(t, h, principal, turn, 1, target, nil, notice, PublishCommunication{Notify: true})
				} else {
					op = hostedEffect(t, h, principal, turn, 1, target, nil, reply, CoordinationReply{})
				}
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			finishHostedReview(t, h, turn)
			if capture {
				return
			}
		}
		t.Fatal("owner did not drain available inputs")
	}
	h.clock.Advance(3 * time.Minute)
	sweep := SweepIssueStatusNotifications{}
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(sweep.Operation(), protocol.Target{Type: "issues"}, struct{}{}, nil), sweep)
	drain(false)
	stopOwnerClaim(t, h, next)
	f = read()
	if f.State != "missing" {
		t.Fatalf("original wake is missing: %+v", f)
	}
	if mode == "parent-paused" {
		parent := admissionCurrent(t, h, issue.ParentIssueID)
		pause := protocol.IssueControlPayload{ExpectedAssignmentEpoch: parent.AssignmentEpoch, Reason: "Hold this responsibility"}
		op := h.exec(owner, h.env("issues.pause", protocol.Target{Type: "issue", ID: parent.IssueID}, pause, rev(parent.Revision)), CanonicalOwnerIssueCommand{Action: "pause"})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if v := read(); v.State != "unverified" {
			t.Fatal("parent hold allowed repair", v)
		}
		check()
		if len(controller.Status().Cases) != 0 {
			t.Fatal("parent hold opened a case")
		}
		return
	}
	// A subsequent, settled non-CI wait carries a different obligation.
	later := protocol.ExternalWait{WaitExternalPayload: protocol.WaitExternalPayload{Kind: "answer", WaitKey: "answer"}, WaitID: "later-answer-wait", RunID: next.RunID, TaskID: as.TaskID, Status: "continued", Stopped: true, WakeReason: "answer_closed"}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutExternalWait(t.Context(), later, h.clock.Now().UnixMilli()) }); err != nil {
		t.Fatal(err)
	}

	// A settled report on this Issue with no PR candidate is independent of this CI wait.
	rollbackReport := errors.New("restore prior reports")
	err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertArtifact(t.Context(), "report-artifact", as.TaskID, as.RunID, "result", `{}`, "digest", h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,status,body_json,artifact_id) VALUES(?,?,?,'report','{}','report-artifact')`, issue.IssueID, as.TaskID, f.SourceEvent); err != nil {
			return err
		}
		got, err := views.OwnerCIContinuation(t.Context(), tx.Q(), as.TaskID, as.RunID)
		if err != nil {
			return err
		}
		if got.State != "missing" {
			t.Errorf("unrelated historical report suppressed missing CI: %+v", got)
		}
		// The PR can also have an ended duty for a different candidate.
		for _, candidate := range []struct{ head, base, target, want string }{
			{"old-head", f.Base, f.Target, "missing"}, {f.Head, "old-base", f.Target, "missing"},
			{f.Head, f.Base, "old-target", "missing"}, {f.Head, f.Base, f.Target, "unverified"},
		} {
			if _, err := tx.Exec(t.Context(), `UPDATE issue_delivery_duties SET status='ended',body_json=json_object('candidate',json_object('delivery_id',?,'head_sha',?,'base_sha',?,'target_sha',?)) WHERE issue_id=? AND task_id=?`, ref.DeliveryID, candidate.head, candidate.base, candidate.target, issue.IssueID, as.TaskID); err != nil {
				return err
			}
			got, err = views.OwnerCIContinuation(t.Context(), tx.Q(), as.TaskID, as.RunID)
			if err != nil {
				return err
			}
			if got.State != candidate.want {
				t.Errorf("ended duty %v gave %s", candidate, got.State)
			}
		}

		return rollbackReport
	})
	if !errors.Is(err, rollbackReport) {
		t.Fatal(err)
	}

	// With the same missing source, independent real control facts veto repair.
	// Roll each injected state back to keep the original obligation and identity.
	for _, hold := range []struct {
		name, query string
		args        []any
	}{
		{"explicitly deferred delivery", "INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,status,body_json) VALUES(?,?,?,'deferred',json_object('candidate',json_object('delivery_id',?)))", []any{issue.IssueID, as.TaskID, f.SourceEvent, ref.DeliveryID}},
		{"paused Task", "UPDATE tasks SET control_mode='paused' WHERE task_id=?", []any{as.TaskID}},
		{"unknown owner delivery", "UPDATE outbox SET status='unknown' WHERE seq=(SELECT max(seq) FROM outbox WHERE destination=?)", []any{store.ConversationDestination(as.Manager.ConversationID)}},
		{"changed baseline", "UPDATE run_external_waits SET body_json=json_set(body_json,'$.observed_base','previous-base') WHERE run_id=?", []any{as.RunID}},
		{"pre-upgrade wait without original source", "UPDATE run_external_waits SET body_json=json_remove(body_json,'$.source_event') WHERE run_id=?", []any{as.RunID}},
	} {
		t.Run(hold.name, func(t *testing.T) {
			rollback := errors.New("restore original facts")
			err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				r, err := tx.Exec(t.Context(), hold.query, hold.args...)
				if err != nil {
					return err
				}
				n, err := r.RowsAffected()
				if err != nil || n != 1 {
					t.Fatalf("hold not injected: %d, %v", n, err)
				}
				v, err := views.OwnerCIContinuation(t.Context(), tx.Q(), as.TaskID, as.RunID)
				if err != nil {
					return err
				}
				if v.State != "unverified" {
					t.Errorf("hold allowed recovery: %+v", v)
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
		})
	}

	check()
	check()
	cases := controller.Status().Cases
	if len(cases) != 1 || cases[0].Subject == nil || cases[0].Subject.WaitID != wait.WaitID || len(controller.Status().Pending) != 1 {
		t.Fatal("missing handoff was not deduplicated", cases)
	}
	for range 2 {
		if err := controller.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if native.starts != 1 || controller.Status().Active == nil {
		t.Fatal("missing case never reached the maintenance Agent")
	}
	req := protocol.ResumeExternalWaitPayload{WaitID: wait.WaitID, Reason: "Restore the original CI notification", RestoreNotice: true}
	for range 2 {
		op := h.exec(owner, h.env(protocol.OpTasksResumeWait, taskTarget(as.TaskID), req, rev(h.task(as.TaskID).ControlRevision)), ResumeExternalWait{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	// A lost client receipt can be reconstructed from the original wait before
	// the maintenance Agent records its own acknowledgement.
	check()
	if v := controller.Status().Cases[0]; v.Intervention == "" || v.Status != "awaiting_verification" {
		t.Fatal("restoration receipt was not reconciled", v)
	}

	if _, err := controller.Command(t.Context(), "intervene", protocol.MaintenanceRequest{RequestID: h.env("maintenance.command", protocol.Target{}, struct{}{}, nil).RequestID, ExpectedRevision: controller.Status().Revision, CaseID: cases[0].ID, Reason: "Original resume-wait returned the same notified source " + f.SourceMessage}); err != nil {
		t.Fatal(err)
	}
	check()
	if s := controller.Status(); s.Cases[0].Status != "awaiting_verification" || len(s.Pending) != 0 {
		t.Fatal("intervention was confused with owner outcome", s.Cases)
	}
	if got := read(); got.State != "notified" || got.OwnerTurn != "" {
		t.Fatal("notification fabricated consumption", got)
	}
	// If the agent loses its reply and the repaired source later disappears, the
	// original wait still proves the restoration and must not permit replay.
	rollback := errors.New("restore effective reminder")
	err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM background_sources WHERE message_id=? AND event_id=?`, f.SourceMessage, f.SourceEvent); err != nil {
			return err
		}
		v, err := views.OwnerCIContinuation(t.Context(), tx.Q(), as.TaskID, as.RunID)
		if err != nil {
			return err
		}
		if !v.NoticeRestored || v.State != "unverified" {
			t.Errorf("original intervention was lost: %+v", v)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	rt.Sequence++
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	restored := ownerClaim(t, h, rt)
	if restored.TaskID != as.TaskID {
		t.Fatal("wrong owner", restored)
	}
	drain(true)
	check()
	if mode == "notify-unrelated" {
		if v := controller.Status().Cases[0]; v.Status != "awaiting_verification" || v.Verification != "" || read().State == "processed" {
			t.Fatal("unattributed notice completed the CI obligation", v)
		}
		return
	}
	if s := controller.Status(); s.Cases[0].Status != "resolved" || s.Cases[0].Verification == "" || s.Cases[0].Intervention == "" || len(s.Pending) != 0 || native.starts != 1 {
		t.Fatal("owner outcome not verified", s.Cases)
	}
	got := read()
	if got.State != "processed" || got.OwnerTurn == "" || got.WaitID != wait.WaitID || got.Head != "candidate" || got.Result != "failure" {
		t.Fatalf("original CI result was not verified: %+v", got)
	}
}

// Only the native maintenance model, provider and HTTP transport are replaced.
// The controller, CI diagnosis, repair, owner and publication use their normal
// state transitions. The model's selected scoped CLI operation runs in the test.
type ciMaintenanceNative struct{ starts int }

func (*ciMaintenanceNative) NewThread(context.Context, protocol.MaintenanceBackend, string, string) (maintenance.NativeThread, error) {
	return maintenance.NativeThread{ID: "maintenance-thread"}, nil
}
func (*ciMaintenanceNative) Read(context.Context, protocol.MaintenanceBackend, string, maintenance.NativeReadMode) (maintenance.NativeState, error) {
	return maintenance.NativeState{}, nil
}
func (n *ciMaintenanceNative) Start(context.Context, protocol.MaintenanceBackend, maintenance.NativeThread, string, string) (string, error) {
	n.starts++
	return "maintenance-turn", nil
}
func (*ciMaintenanceNative) Interrupt(context.Context, protocol.MaintenanceBackend, string, string) error {
	return nil
}

func ciMaintenance(t *testing.T, h *harness) (*maintenance.Controller, *ciMaintenanceNative, maintenance.Probe) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		err := h.store.Read(r.Context(), func(tx *store.ReadTx) error {
			page, e := views.CanonicalIssues(r.Context(), tx.Q(), views.CanonicalIssueFilter{})
			if e != nil {
				return e
			}
			return json.NewEncoder(w).Encode(page)
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("GET /api/v1/issues/{issue}", func(w http.ResponseWriter, r *http.Request) {
		err := h.store.Read(r.Context(), func(tx *store.ReadTx) error {
			v, e := views.CanonicalIssue(r.Context(), tx.Q(), r.PathValue("issue"), 0)
			if e != nil {
				return e
			}
			return json.NewEncoder(w).Encode(v)
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("GET /api/v1/tasks/{task}/ci-continuation/{run}", func(w http.ResponseWriter, r *http.Request) {
		err := h.store.Read(r.Context(), func(tx *store.ReadTx) error {
			v, e := views.OwnerCIContinuation(r.Context(), tx.Q(), r.PathValue("task"), r.PathValue("run"))
			if e != nil {
				return e
			}
			return json.NewEncoder(w).Encode(v)
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	instructions := filepath.Join(dir, "instructions")
	if err := os.WriteFile(instructions, []byte("Read the original CI source and restore only its eligible owner notice."), 0600); err != nil {
		t.Fatal(err)
	}
	native := new(ciMaintenanceNative)
	next := 0
	controller, err := maintenance.Open(t.Context(), maintenance.Config{StateDir: dir, Workspace: dir, InstructionsFile: instructions, Backend: protocol.MaintenanceBackend{Agent: "codex", Socket: filepath.Join(dir, "native.sock"), Sandbox: "workspace-write", ApprovalPolicy: "never"}}, maintenance.Options{Native: native, Now: h.clock.Now, ID: func() string { next++; return strconv.Itoa(next) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	return controller, native, maintenance.Probe{URL: srv.URL, HTTP: srv.Client(), TokenFile: instructions}
}
