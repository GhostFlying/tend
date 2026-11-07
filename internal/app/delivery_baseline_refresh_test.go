package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestServerDeliveryBaselineRefreshWakesMergedSharedJob(t *testing.T) {
	for _, terminal := range []string{"completed", "cancelled"} {
		for _, inFlight := range []bool{false, true} {
			name := terminal
			if inFlight {
				name += "/old-read-in-flight"
			}
			t.Run(name, func(t *testing.T) {
				h, upgrade := upgradeHarness(t)
				h.app.serverDeliveryObservations = true
				var issues []protocol.Issue
				for n := 0; n < 2; n++ {
					_, issue, source, candidate := deliveryFixtureOnHarness(t, h)
					candidate.Deploy = true
					candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
					candidate.DeploymentScope = "Verify controlled deployment of the original delivery"
					prepareBaselineRefreshDuty(t, h, issue.IssueID, source, candidate)
					if n == 0 {
						// Two source tasks in one Issue must not invalidate its one
						// provider observation twice at the same baseline transition.
						second := createCanonicalTaskForTest(t, h, canonicalTaskIssue(t, h, issue.IssueID))
						acceptDeliveryTaskForTest(t, h, second)
						prepareBaselineRefreshDuty(t, h, issue.IssueID, second, candidate)
					}
					issues = append(issues, issue)
				}
				query := claimBaselineRefresh(t, h)
				snapshot := &protocol.DeliverySnapshot{State: "merged", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), MergeSHA: strings.Repeat("c", 40), TargetSHA: strings.Repeat("c", 40), TargetContainsMerge: true}
				if err := h.app.RecordServerDelivery(t.Context(), query.ID, snapshot, ""); err != nil {
					t.Fatal(err)
				}
				if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
					t.Fatal("successful merged delivery must stop periodic reads", q, err)
				}
				var old *ServerDeliveryQuery
				if inFlight {
					issue := canonicalTaskIssue(t, h, issues[0].IssueID)
					op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(issue.IssueID), protocol.RefreshDeliveryPayload{DeliveryID: "pr"}, rev(issue.Revision)), RefreshDelivery{})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					old = claimBaselineRefresh(t, h)
				}
				before := make(map[string]protocol.DeliveryObservation)
				for _, issue := range issues {
					before[issue.IssueID] = readObservations(t, h, issue.IssueID)[0]
				}
				// Keep existing runner bytes so this test exercises the real
				// baseline transition without introducing a second install model.
				upgrade.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
				upgrade.Manifest.Storage.RunnerJournal.Max = 9
				if op := startTestUpgrade(h, "new-baseline", upgrade); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				var settled protocol.UpgradeView
				if terminal == "cancelled" {
					batch := upgradeRead(h)
					op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(batch.View.Revision)), ControlUpgrade{})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					settled = upgradeRead(h).View
				} else {
					observation := ServerObservation{MainDrained: true, BinarySHA256: upgrade.ServerHash}
					upgradeTick(h, observation)
					settled = upgradeTick(h, observation)
				}
				if settled.Status != terminal {
					t.Fatal("upgrade did not reach its actual terminal transition", settled)
				}
				for _, issue := range issues {
					pending := readObservations(t, h, issue.IssueID)[0]
					if pending.Generation != before[issue.IssueID].Generation+1 {
						t.Errorf("baseline invalidated one member more than once: generation %d -> %d", before[issue.IssueID].Generation, pending.Generation)
					}
					if !pending.RefreshPending || pending.CheckedAt != before[issue.IssueID].CheckedAt {
						t.Fatal("baseline must retain original facts while awaiting its new read", pending, before[issue.IssueID])
					}
				}
				if old != nil {
					if err := h.app.RecordServerDelivery(t.Context(), old.ID, snapshot, ""); err != nil {
						t.Fatal(err)
					}
					for _, issue := range issues {
						if !readObservations(t, h, issue.IssueID)[0].RefreshPending {
							t.Fatal("old provider result cleared the new baseline refresh")
						}
					}
				}
				// No explicit refresh is issued after the baseline transition.
				fresh := claimBaselineRefresh(t, h)
				if old != nil && fresh.ID == old.ID {
					t.Fatal("reused the old provider request")
				}
				if err := h.app.RecordServerDelivery(t.Context(), fresh.ID, snapshot, ""); err != nil {
					t.Fatal(err)
				}
				for _, issue := range issues {
					o := readObservations(t, h, issue.IssueID)[0]
					if o.RefreshPending || o.ErrorKind != "" || o.BaselineUpgradeID != settled.UpgradeID || o.BaselineUpgradeRevision != settled.Revision || o.State != "merged" || !o.TargetContainsMerge {
						t.Fatal("fresh shared result did not settle this member's current baseline", o, settled)
					}
				}
				if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
					t.Fatal("shared terminal result caused a duplicate provider read", q, err)
				}
			})
		}
	}
}

func prepareBaselineRefreshDuty(t *testing.T, h *harness, issue, source string, candidate protocol.IssueDeliveryCandidate) {
	t.Helper()
	op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "prepare", Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Prepare a real delivery while its Issue policy is unresolved"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func claimBaselineRefresh(t *testing.T, h *harness) *ServerDeliveryQuery {
	t.Helper()
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal("pending delivery baseline has no claimable shared provider read", q, err)
	}
	return q
}
