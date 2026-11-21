package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Replay the accepted snapshots referenced by the original owner wake
// groups. The fixture retains group membership and anonymized equality of
// commits. Historical reports lack check origin and attempt evidence.
func TestUnsubscribedPROwnerWakeReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/pr_observation_wakes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key                string
		providerFacts      int
		providerOnlyGroups int
	}{{"case351", 52, 17}, {"case393", 30, 25}} {
		t.Run(tc.key, func(t *testing.T) {
			f := newPRWaitTest(t, true)
			stopOwnerClaim(t, f.h, f.run)
			f.h.clock.Advance(6 * time.Minute)
			f.sweep(t)
			baseline := issueChangeSources(t, f.h, f.issue.IssueID)
			runCount := f.h.count("runs")
			observations := 0
			groups := make(map[int]bool)
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.HasPrefix(line, tc.key+" ") {
					continue
				}
				var group, providerOnly, head, base, target int
				var key, delivery, mergeable, state, draft, rollup, checkState, conclusion string
				if _, err := fmt.Sscan(line, &key, &group, &providerOnly, &delivery, &head, &base, &target, &mergeable, &state, &draft, &rollup, &checkState, &conclusion); err != nil {
					t.Fatal(err, line)
				}
				o := protocol.DeliverySnapshot{HeadSHA: replayPRCommit(head), BaseSHA: replayPRCommit(base), TargetSHA: replayPRCommit(target),
					TargetRef: "integration", Mergeability: mergeable, State: state, URL: prWaitURL}
				if draft != "-" {
					value, err := strconv.ParseBool(draft)
					if err != nil {
						t.Fatal(err)
					}
					o.Draft = &value
				}
				if rollup != "-" {
					o.CI = &protocol.DeliveryCI{HeadSHA: o.HeadSHA, State: rollup}
					if checkState != "-" {
						o.CI.Checks = []protocol.DeliveryCheck{{Name: "check", State: checkState, Conclusion: conclusion}}
					}
				}
				f.record(t, &o, "")
				observations++
				if providerOnly == 1 {
					groups[group] = true
				}
				view := readObservations(t, f.h, f.issue.IssueID)[0]
				if view.HeadSHA != o.HeadSHA || view.TargetSHA != o.TargetSHA || view.Mergeability != o.Mergeability ||
					view.RefreshPending || o.CI != nil && (view.CI == nil || view.CI.State != o.CI.State) {
					t.Fatal("provider facts stopped updating", line, view)
				}
				f.h.clock.Advance(6 * time.Minute)
				f.sweep(t)
			}
			if observations != tc.providerFacts || len(groups) != tc.providerOnlyGroups {
				t.Fatal("historical observation or group count changed", observations, len(groups))
			}
			if changed := issueChangeSources(t, f.h, f.issue.IssueID); changed != baseline ||
				f.h.count("runs") != runCount || f.h.count("events WHERE kind='task.external_wait_ready'") != 0 {
				t.Fatal("ordinary PR observations created an owner execution source", changed, baseline)
			}
		})
	}
}

func replayPRCommit(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%040x", n)
}
