package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkItemUpgradeEvidenceVerifiesAndFreezesBatch(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	hash := strings.Repeat("a", 64)
	r := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "batch", ReleaseID: "release", Revision: 5, Status: "completed", Phase: "completed", UpdatedAt: time.Unix(100, 0), Hosts: []protocol.UpgradeHostView{{HostID: "host", Status: "skipped", CurrentVersion: "previous-release", TargetVersion: "release"}}}, Manifest: release.Manifest{ReleaseID: "release"}, ManifestSHA256: hash, ServerHash: hash, Hosts: []store.UpgradeHost{{HostID: "host", PreviousHash: hash, TargetHash: hash}}}
	put := func() {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), r) }); err != nil {
			t.Fatal(err)
		}
	}
	put()
	w.Stages[0].Status = "done"
	w.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: "upgrade", Reference: "batch", Summary: "Fleet update completed", Upgrade: &protocol.UpgradeEvidence{ReleaseID: "forged"}}}
	op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var updated protocol.WorkItem
	_ = json.Unmarshal(op.Result, &updated)
	proof := updated.Stages[0].Evidence[0].Upgrade
	if proof == nil || proof.ReleaseID != "release" || proof.Revision != 5 || len(proof.Hosts) != 1 {
		t.Fatal(proof)
	}
	r.View.Revision = 6
	r.View.Status = "cancelled"
	put()
	updated.Title = "Retain historical proof"
	op = h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: updated.IssuePlan}, rev(updated.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	other := h.createWork(workPlan())
	other.Stages[0].Status = "done"
	other.Stages[0].Evidence = w.Stages[0].Evidence
	expectWorkCode(t, h.exec(mainAgent, h.env("work_items.update", workTarget(other.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: other.IssuePlan}, rev(other.Revision)), UpdateIssue{}), protocol.CodeInvalidRequest)
}

func TestWorkItemUpgradeEvidenceRejectsIncompleteOrMismatchedInstallations(t *testing.T) {
	for _, scenario := range []string{"missing", "running", "partial", "scope", "receipt", "target", "skipped", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			hash := strings.Repeat("a", 64)
			receipt := &protocol.UpdaterReceipt{Binding: protocol.UpgradeBinding{UpgradeID: "batch", ReleaseID: "release", ManifestSHA256: hash, ArtifactSHA256: hash}, Phase: "committed", CommittedConfirmed: true, CurrentSHA256: hash, CurrentProcessInstanceID: "process"}
			r := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "batch", ReleaseID: "release", Revision: 5, Status: "completed", Phase: "completed", UpdatedAt: time.Unix(100, 0), Hosts: []protocol.UpgradeHostView{{HostID: "host", Status: "completed"}}}, Manifest: release.Manifest{ReleaseID: "release"}, ManifestSHA256: hash, ServerHash: hash, Hosts: []store.UpgradeHost{{HostID: "host", TargetHash: hash, Committed: receipt}}}
			switch scenario {
			case "running":
				r.View.Status = "running"
			case "partial":
				r.View.Hosts[0].Status = "failed"
			case "scope":
				r.View.Hosts[0].HostID = "other"
			case "receipt":
				r.Hosts[0].Committed = nil
			case "target":
				receipt.Binding.ArtifactSHA256 = strings.Repeat("b", 64)
			case "skipped":
				r.View.Hosts[0].Status = "skipped"
			}
			if scenario != "missing" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), r) }); err != nil {
					t.Fatal(err)
				}
			}
			w := h.createWork(workPlan())
			w.Stages[0].Status = "done"
			w.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: "upgrade", Reference: "batch", Summary: "Batch proof"}}
			op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan}, rev(w.Revision)), UpdateIssue{})
			if scenario == "complete" {
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				return
			}
			if op.Status == protocol.OpSucceeded {
				t.Fatal("invalid batch accepted", scenario)
			}
		})
	}
}
