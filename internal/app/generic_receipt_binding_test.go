package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_GenericReceiptReaderPreservesOriginalNativeOwnerBinding(t *testing.T) {
	for _, tc := range []struct {
		name, unavailable                string
		native, existing, legacySnapshot bool
	}{
		{name: "new_action", native: true},
		{name: "existing_missing_mode", native: true, existing: true},
		{name: "original_false_mode", existing: true},
		{name: "legacy_saved_false_binding", existing: true, legacySnapshot: true},
		{name: "missing_original_mode", native: true, existing: true, unavailable: "mode"},
		{name: "changed_original_pin", native: true, existing: true, unavailable: "pin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			h, r, _ := issueOwnerHarness(t)
			r.Capabilities = append(r.Capabilities, protocol.CapabilityNativeUsage)
			if tc.native {
				r.Capabilities = append(r.Capabilities, protocol.CapabilityIssueOwnerNativeTools)
			}
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue := createCanonicalOwnerTestIssue(t, h)
			requested := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Original native owner responsibility"}, rev(issue.Revision)), RequestIssueOwner{})
			if requested.Status != protocol.OpSucceeded {
				t.Fatal(requested)
			}
			var original protocol.Assignment
			for range 6 {
				original = ownerClaim(t, h, r)
				if original.Manager != nil && original.Manager.IssueID == issue.IssueID {
					break
				}
				if original.Manager == nil {
					t.Fatal(original)
				}
				stopOwnerClaim(t, h, original)
			}
			if original.Manager == nil || original.Manager.IssueID != issue.IssueID || original.Manager.NativeOwnerTools != tc.native {
				t.Fatal("original native mode not accepted", original)
			}
			pin := *original.Execution
			selection, named, err := h.app.HostedRuntimeSelection(ctx, original.Manager.ConversationID)
			if err != nil || !named || selection.NativeOwnerTools != tc.native {
				t.Fatal(selection, named, err)
			}
			binding := selection.BindingInstance("coordinator")
			if err := h.app.SetMainThread(ctx, binding, "original-thread"); err != nil {
				t.Fatal(err)
			}
			if tc.legacySnapshot || tc.unavailable != "" {
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					if tc.legacySnapshot {
						if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: h.ids.New(protocol.PrefixRequest), ConversationID: original.Manager.ConversationID, IssueID: issue.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
							return err
						}
					}
					if tc.unavailable == "pin" {
						_, err := tx.Exec(ctx, `UPDATE run_agent_executions SET pin_json=json_set(pin_json,'$.profile.model','different-original-model') WHERE run_id=?`, original.RunID)
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE plan_actions SET trigger_json=json_remove(trigger_json,'$.native_owner_tools') WHERE action_id=?`, original.ActionID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if op := h.runAs(original, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"disconnected":true}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.runAs(original, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process really stopped", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if tc.existing {
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE plan_actions SET trigger_json=json_remove(trigger_json,'$.native_owner_tools') WHERE task_id=? AND status='pending' AND json_extract(trigger_json,'$.receipt_only')=1`, original.TaskID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var reader protocol.Assignment
			for range 6 {
				reader = ownerClaim(t, h, r)
				if reader.TaskID == original.TaskID {
					break
				}
				if reader.Manager == nil {
					t.Fatal(reader)
				}
				stopOwnerClaim(t, h, reader)
			}
			if reader.TaskID != original.TaskID || reader.Manager == nil || !reader.Manager.ReceiptOnly || reader.Manager.RequireReceiptReader || *reader.Execution != pin {
				t.Fatal("generic reader changed original responsibility", reader)
			}
			if tc.unavailable != "" {
				if _, _, err := h.app.HostedRuntimeSelection(ctx, reader.Manager.ConversationID); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
					t.Fatal("unavailable original binding was guessed", err)
				}
				return
			}
			// Temporary capability loss must not replace the original binding.
			r.Capabilities = slices.DeleteFunc(r.Capabilities, func(c string) bool { return c == protocol.CapabilityIssueOwnerNativeTools })
			r.Sequence++
			if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			selected, named, err := h.app.HostedRuntimeSelection(ctx, reader.Manager.ConversationID)
			if err != nil || !named || !selected.ReceiptOnly || selected.RequireReceiptReader || selected.NativeOwnerTools != tc.native || selected.BindingInstance("coordinator") != binding {
				t.Fatal("generic reader lost original accepted binding mode", selected, named, err)
			}
			id, found, err := h.app.MainThread(ctx, selected.BindingInstance("coordinator"))
			if err != nil || !found || id != "original-thread" {
				t.Fatal("reader cannot find original binding", id, found, err)
			}
			for _, method := range []string{"start", "fork", "turn"} {
				if _, err := h.app.QueueManagerNative(ctx, reader.RunID, method, json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("receipt identity restored business permission", method, err)
				}
			}
			if h.run(original.RunID).ObservedState != protocol.RunFailed || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, original.RunID) != 1 {
				t.Fatal("reader rewrote original unknown history")
			}
		})
	}
}
