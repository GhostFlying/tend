package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func currentDeliveryFixture(t *testing.T) (*Store, protocol.Issue, protocol.Issue, protocol.IssueDelivery) {
	t.Helper()
	s, _ := openTest(t)
	ref := protocol.IssueDelivery{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17}
	current := canonicalIssueFixture("wi_00000000000000000000000001")
	current.Plan.Deliveries = []protocol.IssueDelivery{ref}
	saveCanonicalFixture(t, s, current, 0)
	closed := canonicalIssueFixture("wi_00000000000000000000000002")
	closed.Plan.Deliveries = []protocol.IssueDelivery{ref}
	closed.State = protocol.IssueClosed
	saveCanonicalFixture(t, s, closed, 0)
	for _, issue := range []protocol.Issue{current, closed} {
		row := DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: issue.IssueID, DeliveryID: ref.DeliveryID, Generation: 3, Observer: "runner", DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}, AttemptedAt: 10, CheckedAt: 10}}
		if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservation(t.Context(), row) }); err != nil {
			t.Fatal(err)
		}
	}
	return s, current, closed, ref
}

func putCurrentServerObservation(t *testing.T, s *Store, ref protocol.IssueDelivery, observation protocol.DeliveryObservation, nonce string, refresh, observed int64) {
	t.Helper()
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE server_delivery_jobs SET observation_json=?,nonce=?,refresh_revision=?,observed_refresh_revision=? WHERE provider=? AND repository=? AND number=?`, string(raw), nonce, refresh, observed, ref.Provider, ref.Repository, ref.Number)
}

func TestSharedReviewUsesSharedProviderCandidate(t *testing.T) {
	s, current, closed, ref := currentDeliveryFixture(t)
	canonical := protocol.DeliveryObservation{Observer: "server", AttemptedAt: 20, CheckedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("d", 40), CI: &protocol.DeliveryCI{HeadSHA: strings.Repeat("c", 40), State: "success", Checks: []protocol.DeliveryCheck{{Name: "check", Conclusion: "success"}}}}}
	putCurrentServerObservation(t, s, ref, canonical, "", 0, 0)
	review := protocol.SharedDeliveryReview{Provider: ref.Provider, Repository: ref.Repository, Number: ref.Number, Status: "passed", HeadSHA: canonical.HeadSHA, BaseSHA: canonical.BaseSHA, Links: []protocol.SharedDeliveryReviewLink{{IssueID: current.IssueID, DeliveryID: ref.DeliveryID}, {IssueID: closed.IssueID, DeliveryID: ref.DeliveryID}}}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var err error
		review.RequirementsDigest, err = SharedReviewRequirements(t.Context(), tx.Q(), review)
		if err != nil {
			return err
		}
		changed, err := SharedDeliveryReviewChanged(t.Context(), tx.Q(), &review)
		if err != nil {
			return err
		}
		if changed || !review.Valid {
			t.Fatalf("old Issue copies overrode the exact shared provider fact: changed=%v review=%+v", changed, review)
		}
		if err := RequireDeliveryChecks(t.Context(), tx.Q(), closed, protocol.IssueDeliveryCandidate{DeliveryID: ref.DeliveryID, HeadSHA: canonical.HeadSHA}); err != nil {
			t.Fatalf("checks disagreed with the shared provider candidate: %v", err)
		}
		old, err := GetDeliveryObservation(t.Context(), tx.Q(), closed.IssueID, ref.DeliveryID)
		if err == nil && (old.Observation.HeadSHA != strings.Repeat("a", 40) || old.Observation.CheckedAt != 10 || old.Observation.Generation != 3) {
			t.Fatal("historical observation was rewritten", old)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentDeliveryObservationPreservesLatestProviderState(t *testing.T) {
	for _, name := range []string{"empty migration", "missing job", "current success", "failed read", "pending read", "uncovered refresh", "covered refresh", "first pending read", "first refresh", "foreign PR", "corrupt cache"} {
		t.Run(name, func(t *testing.T) {
			s, current, _, ref := currentDeliveryFixture(t)
			canonical := protocol.DeliveryObservation{Observer: "server", AttemptedAt: 20, CheckedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("d", 40)}}
			nonce := ""
			var refresh, observed int64
			wantHead, wantError, wantPending := canonical.HeadSHA, "", false
			legacy, corrupt := false, false
			switch name {
			case "empty migration":
				legacy = true
			case "missing job":
				mustExec(t, s, `DELETE FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
				legacy = true
			case "failed read":
				canonical.ErrorKind = "rate_limited"
				wantError = canonical.ErrorKind
			case "pending read":
				nonce, wantPending = "original-provider-read", true
			case "uncovered refresh":
				refresh, observed, wantPending = 2, 1, true
			case "covered refresh":
				refresh, observed = 2, 2
			case "first pending read":
				canonical = protocol.DeliveryObservation{}
				nonce, wantHead, wantPending = "first-provider-read", "", true
			case "first refresh":
				canonical = protocol.DeliveryObservation{}
				refresh, wantHead, wantPending = 1, "", true
			case "foreign PR":
				foreign := ref
				foreign.Number++
				mustExec(t, s, `INSERT INTO server_delivery_jobs(provider,repository,number) VALUES(?,?,?)`, foreign.Provider, foreign.Repository, foreign.Number)
				putCurrentServerObservation(t, s, foreign, canonical, "", 0, 0)
				legacy = true
			case "corrupt cache":
				mustExec(t, s, `UPDATE server_delivery_jobs SET observation_json='{bad' WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
				corrupt = true
			}
			if !legacy && !corrupt {
				putCurrentServerObservation(t, s, ref, canonical, nonce, refresh, observed)
			}
			if legacy {
				wantHead = strings.Repeat("a", 40)
			}
			if err := s.Read(t.Context(), func(tx *ReadTx) error {
				got, err := GetCurrentDeliveryObservation(t.Context(), tx.Q(), current.IssueID, ref)
				if corrupt {
					if err == nil || !strings.Contains(err.Error(), "decode shared delivery observation") {
						t.Fatalf("corrupt current cache fell back to old success: %+v, %v", got, err)
					}
					return nil
				}
				if err != nil {
					return err
				}
				if got.Observation.HeadSHA != wantHead || got.Observation.ErrorKind != wantError || got.Observation.RefreshPending != wantPending || got.Observation.WorkItemID != current.IssueID || got.Observation.DeliveryID != ref.DeliveryID {
					t.Fatalf("current provider selection lost its provenance or state: %+v", got)
				}
				if legacy && (got.Observation.Generation != 3 || got.Observation.Observer != "runner") {
					t.Fatal("legacy runner observation identity changed", got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCurrentDeliveryObservationReturnsReadErrors(t *testing.T) {
	s, current, _, ref := currentDeliveryFixture(t)
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := GetCurrentDeliveryObservation(ctx, tx.Q(), current.IssueID, ref); !errors.Is(err, context.Canceled) {
			t.Fatalf("current read failure was hidden by a legacy success: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedReviewPreservesLegacyLinkedCandidateChecks(t *testing.T) {
	s, current, closed, ref := currentDeliveryFixture(t)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		old, err := GetDeliveryObservation(t.Context(), tx.Q(), closed.IssueID, ref.DeliveryID)
		if err != nil {
			return err
		}
		old.Observation.HeadSHA = strings.Repeat("c", 40)
		return tx.PutDeliveryObservation(t.Context(), old)
	}); err != nil {
		t.Fatal(err)
	}
	review := protocol.SharedDeliveryReview{Provider: ref.Provider, Repository: ref.Repository, Number: ref.Number, Status: "passed", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), Links: []protocol.SharedDeliveryReviewLink{{IssueID: current.IssueID, DeliveryID: ref.DeliveryID}, {IssueID: closed.IssueID, DeliveryID: ref.DeliveryID}}}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var err error
		review.RequirementsDigest, err = SharedReviewRequirements(t.Context(), tx.Q(), review)
		if err != nil {
			return err
		}
		changed, err := SharedDeliveryReviewChanged(t.Context(), tx.Q(), &review)
		if err == nil && (!changed || review.Valid || review.InvalidReason != "provider candidate changed") {
			t.Fatal("one legacy Issue's snapshot replaced another original runner candidate", review)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
