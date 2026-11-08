package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIncrementalIssueContextQueuedExecutionMatchesOrdinaryView(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		name := "ready"
		if capacity {
			name = "host_capacity"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			cfg := testConfig()
			cfg.Hosts[0].Slots = 1
			if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue := createCanonicalOwnerTestIssue(t, h)
			if capacity {
				first := createCanonicalTaskForTest(t, h, issue)
				as, ok, _ := h.claim("linux-1")
				if !ok || as.TaskID != first {
					t.Fatal("capacity fixture did not allocate original worker", as)
				}
				issue = canonicalTaskIssue(t, h, issue.IssueID)
			}
			task := createCanonicalTaskForTest(t, h, issue)
			ordinary := h.executionTask(task).Execution
			code := protocol.QueueReady
			if capacity {
				code = protocol.QueueHostCapacity
			}
			if ordinary == nil || ordinary.State != protocol.ExecutionQueued || ordinary.QueueReason == nil || ordinary.QueueReason.Code != code {
				t.Fatal("ordinary queue fixture", ordinary)
			}
			items, _ := finishIncrementalContext(t, h, issue.IssueID, "", 1)
			for _, item := range items {
				if item.ID == task && item.Task != nil {
					if !reflect.DeepEqual(item.Task.Execution, ordinary) {
						t.Fatalf("incremental queue differs from authoritative execution: got=%+v want=%+v", item.Task.Execution, ordinary)
					}
					return
				}
			}
			t.Fatal("queued task missing")
		})
	}
}

func TestIncrementalIssueContextReviewUnlinkRetainsCurrentLinksAndFinalTombstone(t *testing.T) {
	h, _, issue, task, _, _ := sharedReviewFixture(t)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: task}))
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"}))
	unrelated := createCanonicalOwnerTestIssue(t, h)
	_, unrelatedCursor := finishIncrementalContext(t, h, unrelated.IssueID, "", 20)
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: "pr", TaskID: task}))
	items, cursor := finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
	reviews := 0
	for _, item := range items {
		if item.Kind == "review" {
			reviews++
			if item.ID != review.ReviewID || item.Removed || item.Review == nil {
				t.Fatal("remaining Issue-level link disappeared", item)
			}
		}
	}
	if reviews != 1 {
		t.Fatal("shared review update missing or duplicated", items)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: "pr"}))
	items, cursor = finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
	reviews = 0
	for _, item := range items {
		if item.Kind == "review" {
			reviews++
			if item.ID != review.ReviewID || !item.Removed || item.Review == nil || item.Review.ReviewID != review.ReviewID {
				t.Fatal("last unlink lacks reference tombstone", item)
			}
		}
	}
	if reviews != 1 {
		t.Fatal("last unlink missing or duplicated", items)
	}
	unchanged := readIncrementalContext(t, h, issue.IssueID, cursor, 20)
	if len(unchanged.Items) != 0 {
		t.Fatal("tombstone replayed without another event", unchanged)
	}
	other, _ := finishIncrementalContext(t, h, unrelated.IssueID, unrelatedCursor, 20)
	if len(other) != 0 {
		t.Fatal("review unlink crossed unrelated Issue scope", other)
	}
}

func incrementalDutyItem(t *testing.T, h *harness, issue, task, cursor string) (protocol.IssueContextDelivery, string) {
	t.Helper()
	items, next := finishIncrementalContext(t, h, issue, cursor, 2)
	var found *protocol.IssueContextDelivery
	for _, item := range items {
		if item.Delivery != nil {
			if item.ID != task || found != nil {
				t.Fatal("unrelated or duplicate duty in delta", items)
			}
			found = item.Delivery
		}
	}
	if found == nil {
		t.Fatalf("actual delivery mutation missing from delta: %+v", items)
	}
	current := deliveryDutyForTest(t, h, issue, task)
	if found.Revision != current.Revision || found.Status != current.Status || found.ExecutionTaskID != current.ExecutionTaskID {
		t.Fatal("duty delta differs from current ledger", found, current)
	}
	return *found, next
}

func TestIncrementalIssueContextDeliveryWritersExposeActualMutations(t *testing.T) {
	for _, required := range []bool{true, false} {
		name := "accepted_creation"
		if !required {
			name = "waived_creation"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Publish actual result", Spec: testSpec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: required, Reason: "Exercise the chosen frozen result policy"}}
			op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created taskResult
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			task := created.TaskID
			_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
			if required {
				acceptDeliveryTaskForTest(t, h, task)
			} else {
				as, ok, _ := h.claim("linux-1")
				if !ok || as.TaskID != task || as.Role != protocol.RoleImplement {
					t.Fatal(as)
				}
				if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			duty, _ := incrementalDutyItem(t, h, issue.IssueID, task, cursor)
			if duty.Status != "preparing" || duty.Revision != 1 {
				t.Fatal(duty)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var count int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE object_type='issue' AND object_id=? AND kind='issue.delivery_updated' AND json_extract(body_json,'$.source_task_id')=?`, issue.IssueID, task).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					t.Fatal("completion replay duplicated the duty change", count)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("manual_prepare", func(t *testing.T) {
		h, _, issue, task, candidate, acceptance := sharedReviewFixture(t)
		importSharedReviewForTest(t, h, issue, task, acceptance)
		setPRPolicyForTest(t, h, issue.IssueID, "manual")
		_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
		op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "prepare", Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Present the real reviewed candidate for manual approval"}})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		duty, _ := incrementalDutyItem(t, h, issue.IssueID, task, cursor)
		if duty.Status != "awaiting_owner" {
			t.Fatal(duty)
		}
	})
	t.Run("manual_defer", func(t *testing.T) {
		h, issue, task, candidate := deliveryFixture(t)
		d := prepareDeliveryForTest(t, h, issue.IssueID, task, candidate)
		_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
		answerDeliveryForTest(t, h, d, "defer", "")
		duty, cursor := incrementalDutyItem(t, h, issue.IssueID, task, cursor)
		if duty.Status != "deferred" {
			t.Fatal(duty)
		}
		if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "prepare", Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Read the unchanged owner deferral"}}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if items, _ := finishIncrementalContext(t, h, issue.IssueID, cursor, 20); len(items) != 0 {
			t.Fatal("unchanged deferred prepare emitted another change", items)
		}
	})
	t.Run("separate_execution_binding", func(t *testing.T) {
		h, issue, task, candidate := deliveryFixture(t)
		d := prepareDeliveryForTest(t, h, issue.IssueID, task, candidate)
		answerDeliveryForTest(t, h, d, "approve", "")
		_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
		if op := createDeliveryExecutionForTest(t, h, issue.IssueID, d); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		duty, _ := incrementalDutyItem(t, h, issue.IssueID, task, cursor)
		if duty.Status != "executing" || duty.ExecutionTaskID == "" {
			t.Fatal(duty)
		}
	})
	t.Run("live_prepare_bind", func(t *testing.T) {
		h, _, issue, impl, candidate := liveReviewedDeliveryFixture(t)
		_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
		prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
		_, cursor = incrementalDutyItem(t, h, issue.IssueID, impl.TaskID, cursor)
		if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue same actual authorized task"}}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		duty, _ := incrementalDutyItem(t, h, issue.IssueID, impl.TaskID, cursor)
		if duty.Status != "executing" || duty.ExecutionTaskID != impl.TaskID {
			t.Fatal(duty)
		}
	})
}
