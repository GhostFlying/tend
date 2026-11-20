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
	return s, current, closed, ref
}

func currentFact(t *testing.T, s *Store, ref protocol.IssueDelivery, o protocol.DeliveryObservation) DeliveryObservationFactRow {
	t.Helper()
	var fact DeliveryObservationFactRow
	if err := s.Write(t.Context(), func(tx *Tx) (err error) { fact, err = tx.PutDeliveryObservationFact(t.Context(), ref, o); return err }); err != nil {
		t.Fatal(err)
	}
	return fact
}

func readCurrentObservation(t *testing.T, s *Store, issue, id string) DeliveryObservationRow {
	t.Helper()
	var row DeliveryObservationRow
	if err := s.Read(t.Context(), func(tx *ReadTx) (err error) {
		row, err = GetDeliveryObservation(t.Context(), tx.Q(), issue, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestDeliveryObservationReferencesShareFactsWithoutQueryCopies(t *testing.T) {
	s, current, closed, ref := currentDeliveryFixture(t)
	observation := protocol.DeliveryObservation{Observer: "server", CheckedAt: 20, AttemptedAt: 20, BaselineUpgradeID: "original-query", BaselineUpgradeRevision: 2, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("d", 40)}}
	fact := currentFact(t, s, ref, observation)
	foreign := ref
	foreign.Number++
	currentFact(t, s, foreign, protocol.DeliveryObservation{CheckedAt: 30, AttemptedAt: 30, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: "foreign-newer-head"}})
	for _, id := range []string{current.IssueID, closed.IssueID} {
		row := readCurrentObservation(t, s, id, ref.DeliveryID)
		if row.Observation.HeadSHA != observation.HeadSHA || row.Observation.Generation != 0 || row.FactRevision != fact.FactRevision {
			t.Fatal("reference without query did not see current PR", row)
		}
	}
	query := DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: closed.IssueID, DeliveryID: ref.DeliveryID, Generation: 7, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: "not-provider-evidence"}}, HostID: "server", Nonce: "historical-server-query", QueryBaselineUpgradeID: "later-query", QueryBaselineUpgradeRevision: 3, ObservedRevision: fact.FactRevision}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), query) }); err != nil {
		t.Fatal(err)
	}
	got := readCurrentObservation(t, s, closed.IssueID, ref.DeliveryID)
	if got.Observation.HeadSHA != observation.HeadSHA || got.Observation.BaselineUpgradeID != "original-query" || got.QueryBaselineUpgradeID != "later-query" || got.Observation.Generation != 7 {
		t.Fatal("query metadata replaced provider facts or baseline", got)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, closed.IssueID, ref.DeliveryID).Scan(&raw); err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
		for key := range fields {
			if key != "baseline_upgrade_id" && key != "baseline_upgrade_revision" && key != "observed_revision" {
				t.Fatalf("query persisted provider field %q: %s", key, raw)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryObservationFactRevisionsCaptureSameClockChanges(t *testing.T) {
	s, current, _, ref := currentDeliveryFixture(t)
	o := protocol.DeliveryObservation{WorkItemID: current.IssueID, DeliveryID: ref.DeliveryID, Generation: 99, RefreshPending: true, Observer: "server", CheckedAt: 20, AttemptedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "one", BaseSHA: "base"}}
	first := currentFact(t, s, ref, o)
	if first.FactRevision != 1 || first.ChangedRevision != 1 {
		t.Fatal(first)
	}
	same := currentFact(t, s, ref, o)
	if same.FactRevision != 2 || same.ChangedRevision != 1 {
		t.Fatal("unchanged fact caused another change notice", same)
	}
	o.HeadSHA = "two"
	moved := currentFact(t, s, ref, o)
	if moved.FactRevision != 3 || moved.ChangedRevision != 3 {
		t.Fatal("same-clock candidate move disappeared", moved)
	}
	o.ErrorKind = "timeout"
	failed := currentFact(t, s, ref, o)
	if failed.FactRevision != 4 || failed.ChangedRevision != 4 {
		t.Fatal(failed)
	}
	o.RetryAt = 50
	retry := currentFact(t, s, ref, o)
	if retry.FactRevision != 5 || retry.ChangedRevision != 4 {
		t.Fatal("retry interval invented a provider change", retry)
	}
	o.RetryAt = 0
	recovered := currentFact(t, s, ref, o)
	if recovered.FactRevision != 6 || recovered.ChangedRevision != 6 {
		t.Fatal("retry recovery did not invalidate observers", recovered)
	}
	o.CheckedAt, o.AttemptedAt, o.Observer = 21, 21, "runner"
	checked := currentFact(t, s, ref, o)
	if checked.FactRevision != 7 || checked.ChangedRevision != 6 {
		t.Fatal("clock or observer change invented a provider change", checked)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT observation_json FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number).Scan(&raw); err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
		for _, key := range []string{"work_item_id", "delivery_id", "generation", "refresh_pending"} {
			if _, ok := fields[key]; ok {
				t.Fatalf("fact belongs to one Issue via %q", key)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedReviewUsesOnlyNormalizedProviderCandidate(t *testing.T) {
	s, current, closed, ref := currentDeliveryFixture(t)
	o := protocol.DeliveryObservation{Observer: "runner", CheckedAt: 20, AttemptedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("d", 40), CI: &protocol.DeliveryCI{HeadSHA: strings.Repeat("c", 40), State: "success", Checks: []protocol.DeliveryCheck{{Name: "check", Conclusion: "success"}}}}}
	currentFact(t, s, ref, o)
	review := protocol.SharedDeliveryReview{Provider: ref.Provider, Repository: ref.Repository, Number: ref.Number, Status: "passed", HeadSHA: o.HeadSHA, BaseSHA: o.BaseSHA, Links: []protocol.SharedDeliveryReviewLink{{IssueID: current.IssueID, DeliveryID: ref.DeliveryID}, {IssueID: closed.IssueID, DeliveryID: ref.DeliveryID}}}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var err error
		review.RequirementsDigest, err = SharedReviewRequirements(t.Context(), tx.Q(), review)
		if err != nil {
			return err
		}
		changed, err := SharedDeliveryReviewChanged(t.Context(), tx.Q(), &review)
		if err == nil && (changed || !review.Valid) {
			t.Fatal("registered closed responsibility overrode unique PR fact", review)
		}
		if err != nil {
			return err
		}
		return RequireDeliveryChecks(t.Context(), tx.Q(), closed, protocol.IssueDeliveryCandidate{DeliveryID: ref.DeliveryID, HeadSHA: o.HeadSHA})
	}); err != nil {
		t.Fatal(err)
	}
	o.HeadSHA = strings.Repeat("e", 40)
	currentFact(t, s, ref, o)
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		changed, err := SharedDeliveryReviewChanged(t.Context(), tx.Q(), &review)
		if err == nil && (!changed || review.Valid || review.InvalidReason != "provider candidate changed") {
			t.Fatal("actual provider change did not invalidate review", review)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryObservationUnknownAndReadErrors(t *testing.T) {
	s, current, _, ref := currentDeliveryFixture(t)
	mustExec(t, s, `INSERT INTO retained_delivery_observations(work_item_id,delivery_id,generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,server_refresh_revision,observation_json) VALUES(?,?,3,'original','original-host','original-instance',90,100,0,0,'{"state":"merged","head_sha":"historical-only","checked_at":10}')`, current.IssueID, ref.DeliveryID)
	unknown := readCurrentObservation(t, s, current.IssueID, ref.DeliveryID)
	if unknown.Observation.State != "unknown" || unknown.FactRevision != 0 {
		t.Fatal(unknown)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		if _, err := GetDeliveryObservation(t.Context(), tx.Q(), current.IssueID, "removed"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing reference: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := GetDeliveryObservationFact(ctx, tx.Q(), ref); !errors.Is(err, context.Canceled) {
			t.Fatalf("read error hidden: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE server_delivery_jobs SET observation_json='{bad' WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		if _, err := GetDeliveryObservation(t.Context(), tx.Q(), current.IssueID, ref.DeliveryID); err == nil {
			t.Fatal("corrupt target became old successful snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryObservationPendingFollowsCurrentTargetQueries(t *testing.T) {
	for _, name := range []string{"old server member", "current runner", "retired runner", "handed-off runner", "undated runner", "handoff before deadline", "server query", "uncovered refresh", "covered refresh", "canceled refresh counter", "legacy refresh"} {
		t.Run(name, func(t *testing.T) {
			s, current, closed, ref := currentDeliveryFixture(t)
			currentFact(t, s, ref, protocol.DeliveryObservation{Observer: "runner", CheckedAt: 20, AttemptedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "current", BaseSHA: "base"}})
			query := DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: current.IssueID, DeliveryID: ref.DeliveryID, Generation: 7}, Nonce: "original-query", HostID: "server", InstanceID: "original-instance", QueryUntil: 90}
			pending := false
			switch name {
			case "current runner", "retired runner", "handed-off runner", "undated runner", "handoff before deadline":
				query.HostID = "original-runner"
				pending = name != "retired runner" && name != "handed-off runner"
				if name == "undated runner" {
					query.QueryUntil = 0
				}
			case "server query":
				mustExec(t, s, `UPDATE server_delivery_jobs SET nonce='actual-server-query',query_until=90 WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
				pending = true
			case "uncovered refresh", "covered refresh":
				query.RefreshRequested, query.ServerRefreshRevision = true, 2
				observed := 2
				if name == "uncovered refresh" {
					observed, pending = 1, true
				}
				mustExec(t, s, `UPDATE server_delivery_jobs SET refresh_revision=2,observed_refresh_revision=? WHERE provider=? AND repository=? AND number=?`, observed, ref.Provider, ref.Repository, ref.Number)
			case "old server member":
				query.RefreshRequested, query.ServerRefreshRevision = true, 1
				mustExec(t, s, `UPDATE server_delivery_jobs SET refresh_revision=1,observed_refresh_revision=1 WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
			case "canceled refresh counter":
				mustExec(t, s, `UPDATE server_delivery_jobs SET refresh_revision=2,observed_refresh_revision=1 WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
			case "legacy refresh":
				query.RefreshRequested, pending = true, true
			}
			if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), query) }); err != nil {
				t.Fatal(err)
			}
			if name == "retired runner" {
				mustExec(t, s, `INSERT INTO retired_runner_delivery_queries(nonce,issue_id,delivery_id,generation,host_id,instance_id,query_until,retired_at) VALUES(?,?,?,7,?,?,90,100)`, query.Nonce, current.IssueID, ref.DeliveryID, query.HostID, query.InstanceID)
			}
			if name == "handed-off runner" || name == "undated runner" || name == "handoff before deadline" {
				started := 100
				if name == "handoff before deadline" {
					started = 89
				}
				mustExec(t, s, `INSERT INTO server_delivery_queries(query_id,provider,repository,number,status,started_at) VALUES('handoff-query',?,?,?,'completed',?)`, ref.Provider, ref.Repository, ref.Number, started)
			}
			for _, issue := range []protocol.Issue{current, closed} {
				row := readCurrentObservation(t, s, issue.IssueID, ref.DeliveryID)
				if row.Observation.RefreshPending != pending || row.Observation.HeadSHA != "current" {
					t.Fatal("Issue metadata masked the unique current fact or query responsibility", row)
				}
				if issue.IssueID == current.IssueID && (row.Nonce != query.Nonce || row.Observation.Generation != 7) {
					t.Fatal("unknown or retired identity was erased instead of retained", row)
				}
			}
		})
	}
}

func TestDeliveryObservationRefreshPendingUsesRemainingRequests(t *testing.T) {
	s, current, other, ref := currentDeliveryFixture(t)
	currentFact(t, s, ref, protocol.DeliveryObservation{Observer: "server", CheckedAt: 20, AttemptedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "current", BaseSHA: "base"}})
	requests := []DeliveryObservationRow{
		{Observation: protocol.DeliveryObservation{WorkItemID: current.IssueID, DeliveryID: ref.DeliveryID, Generation: 3}, RefreshRequested: true, ServerRefreshRevision: 1},
		{Observation: protocol.DeliveryObservation{WorkItemID: other.IssueID, DeliveryID: ref.DeliveryID, Generation: 4}, RefreshRequested: true, ServerRefreshRevision: 2},
	}
	for _, request := range requests {
		if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), request) }); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s, `UPDATE server_delivery_jobs SET refresh_revision=2,observed_refresh_revision=0 WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number)
	assertPending := func(want bool) {
		t.Helper()
		for _, issue := range []protocol.Issue{current, other} {
			row := readCurrentObservation(t, s, issue.IssueID, ref.DeliveryID)
			if row.Observation.RefreshPending != want || row.Observation.HeadSHA != "current" {
				t.Fatal("remaining refresh ownership disagreed across references", row)
			}
		}
	}
	assertPending(true)
	for index := range requests {
		requests[index].RefreshRequested = false
		requests[index].Observation.Generation++
		if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), requests[index]) }); err != nil {
			t.Fatal(err)
		}
		assertPending(index == 0)
	}
	// Cancellation does not fabricate a completed read or erase request ordering.
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var refresh, observed int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT refresh_revision,observed_refresh_revision FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number).Scan(&refresh, &observed); err != nil {
			return err
		}
		if refresh != 2 || observed != 0 {
			t.Fatal("cancellation rewrote provider completion history", refresh, observed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryObservationTransitionSurvivesCheckedResultsAndReopen(t *testing.T) {
	s, _, _, ref := currentDeliveryFixture(t)
	o := protocol.DeliveryObservation{Observer: "runner", CheckedAt: 20, AttemptedAt: 20, ErrorKind: "timeout", DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "head", BaseSHA: "base"}}
	currentFact(t, s, ref, o)
	o.State, o.ErrorKind, o.MergeSHA = "merged", "", "merge"
	merged := currentFact(t, s, ref, o)
	if merged.ChangedRevision != 2 || merged.PreviousState != "open" || merged.PreviousErrorKind != "timeout" {
		t.Fatal("merge lost its real transition", merged)
	}
	o.Observer, o.CheckedAt, o.AttemptedAt = "server", 21, 21
	checked := currentFact(t, s, ref, o)
	if checked.FactRevision != 3 || checked.ChangedRevision != 2 || checked.PreviousState != "open" || checked.PreviousErrorKind != "timeout" {
		t.Fatal("checked-only result erased the unconsumed merge transition", checked)
	}
	path, now := s.path, s.now
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if err := reopened.Read(t.Context(), func(tx *ReadTx) error {
		stored, err := GetDeliveryObservationFact(t.Context(), tx.Q(), ref)
		if err == nil && (stored.FactRevision != 3 || stored.ChangedRevision != 2 || stored.PreviousState != "open" || stored.PreviousErrorKind != "timeout") {
			t.Fatal("cached continuation reconstructed its own state as its predecessor", stored)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o.HeadSHA = "next-head"
	next := currentFact(t, reopened, ref, o)
	if next.ChangedRevision != 4 || next.PreviousState != "merged" || next.PreviousErrorKind != "" {
		t.Fatal("new semantic change kept an unrelated transition", next)
	}
}

func TestDeliveryObservationNotificationPendingPersistsWithoutRefresh(t *testing.T) {
	s, _, closed, ref := currentDeliveryFixture(t)
	fact := currentFact(t, s, ref, protocol.DeliveryObservation{Observer: "server", CheckedAt: 20, AttemptedAt: 20, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: "head", BaseSHA: "base", MergeSHA: "merge"}})
	row := readCurrentObservation(t, s, closed.IssueID, ref.DeliveryID)
	if row.NotificationPending {
		t.Fatal("old query metadata invented an unconsumed notification", row)
	}
	row.Observation.Generation = 7
	row.QueryBaselineUpgradeID, row.QueryBaselineUpgradeRevision = "original-query", 3
	row.NotificationPending = true
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), row) }); err != nil {
		t.Fatal(err)
	}
	path, now := s.path, s.now
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	owed := readCurrentObservation(t, reopened, closed.IssueID, ref.DeliveryID)
	if !owed.NotificationPending || owed.RefreshRequested || owed.Observation.RefreshPending || owed.Observation.Generation != 7 || owed.QueryBaselineUpgradeID != "original-query" || owed.QueryBaselineUpgradeRevision != 3 || owed.FactRevision != fact.FactRevision || owed.ObservedRevision != 0 {
		t.Fatal("deferred notification became a refresh or lost its original metadata", owed)
	}
	owed.NotificationPending, owed.ObservedRevision = false, fact.FactRevision
	if err := reopened.Write(t.Context(), func(tx *Tx) error { return tx.PutDeliveryObservationQuery(t.Context(), owed) }); err != nil {
		t.Fatal(err)
	}
	consumed := readCurrentObservation(t, reopened, closed.IssueID, ref.DeliveryID)
	if consumed.NotificationPending || consumed.RefreshRequested || consumed.Observation.RefreshPending || consumed.ObservedRevision != fact.FactRevision || consumed.Observation.HeadSHA != "head" {
		t.Fatal("notification consumption changed the PR or restarted a query", consumed)
	}
	if err := reopened.Read(t.Context(), func(tx *ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, closed.IssueID, ref.DeliveryID).Scan(&raw); err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
		for _, field := range []string{"notification_pending", "head_sha", "state", "ci"} {
			if _, ok := fields[field]; ok {
				t.Fatal("consumed metadata retained a notification or provider snapshot", field, raw)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
