package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueBoardStageMigrationRecoversContinuousHistory(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "before-board-stage.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: db, read: db, now: clock.Now}
	t.Cleanup(func() { old.Close() })
	if err := old.migrate(ctx, 147); err != nil {
		t.Fatal(err)
	}
	create := func(id string, at int64, status protocol.IssueWorkStatus) protocol.Issue {
		t.Helper()
		issue := canonicalIssueFixture(id)
		issue.WorkStatus = status
		issue.CreatedAt, issue.UpdatedAt = at, at
		saveCanonicalFixture(t, old, issue, 0)
		return issue
	}
	revise := func(issue *protocol.Issue, status protocol.IssueWorkStatus, at int64) {
		t.Helper()
		issue.Revision++
		issue.UpdatedAt = at
		issue.WorkStatus = status
		saveCanonicalFixture(t, old, *issue, issue.Revision-1)
	}
	reentered := create("wi_00000000000000000000000001", 11, protocol.IssueBacklog)
	revise(&reentered, protocol.IssueInProgress, 20)
	revise(&reentered, protocol.IssueBacklog, 30)
	revise(&reentered, protocol.IssueInProgress, 40)
	revise(&reentered, protocol.IssueInReview, 50)
	revise(&reentered, protocol.IssueInReview, 70)

	gap := create("wi_00000000000000000000000002", 12, protocol.IssueBacklog)
	revise(&gap, protocol.IssueInReview, 21)
	revise(&gap, protocol.IssueBacklog, 32)
	revise(&gap, protocol.IssueBacklog, 72)

	initial := create("wi_00000000000000000000000003", 13, protocol.IssueBacklog)
	revise(&initial, protocol.IssueBacklog, 73)
	reviewAtCreation := create("wi_00000000000000000000000004", 14, protocol.IssueInReview)
	revise(&reviewAtCreation, protocol.IssueInProgress, 34)
	activeGap := create("wi_00000000000000000000000005", 15, protocol.IssueInProgress)
	revise(&activeGap, protocol.IssueBacklog, 25)
	revise(&activeGap, protocol.IssueInReview, 35)

	// The old archive fixture is missing an intermediate control revision.
	// The matching stages before and after the gap do not prove continuity.
	if err := old.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `DROP TRIGGER issue_control_revision_immutable_delete`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM issue_control_revisions WHERE issue_id IN (?,?) AND revision=2`, gap.IssueID, activeGap.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	assertEntry := func(issue protocol.Issue, at int64, basis protocol.IssueStageEntryBasis) {
		t.Helper()
		err := s.Read(ctx, func(tx *ReadTx) error {
			var status protocol.IssueWorkStatus
			var enteredAt int64
			var source protocol.IssueStageEntryBasis
			err := tx.Q().QueryRowContext(ctx, `SELECT status,entered_at,basis FROM issue_board_stage_entries WHERE issue_id=?`, issue.IssueID).Scan(&status, &enteredAt, &source)
			if err == nil && (status != issue.BoardStatus() || enteredAt != at || source != basis) {
				t.Fatalf("%s entry = %s at %d from %s, want %s at %d from %s", issue.IssueID, status, enteredAt, source, issue.BoardStatus(), at, basis)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	assertEntry(reentered, 50, protocol.IssueStageEntryTransition)
	assertEntry(initial, 13, protocol.IssueStageEntryCreation)
	assertEntry(gap, 12, protocol.IssueStageEntryCreationFallback)
	assertGrouped := func(issue protocol.Issue, at int64, source protocol.IssueStageEntryBasis) {
		t.Helper()
		err := s.Read(ctx, func(tx *ReadTx) error {
			var status protocol.IssueWorkStatus
			var entered int64
			var basis protocol.IssueStageEntryBasis
			err := tx.Q().QueryRowContext(ctx, `SELECT status,entered_at,basis FROM issue_board_grouped_stage_entries WHERE issue_id=?`, issue.IssueID).Scan(&status, &entered, &basis)
			if err == nil && (status != protocol.IssueInProgress || entered != at || basis != source) {
				t.Fatalf("grouped %s entry = %s at %d from %s, want in_progress at %d from %s", issue.IssueID, status, entered, basis, at, source)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertGrouped(reentered, 40, protocol.IssueStageEntryTransition)
	assertGrouped(reviewAtCreation, 14, protocol.IssueStageEntryCreation)
	assertGrouped(activeGap, 15, protocol.IssueStageEntryCreationFallback)
	moveGrouped := func(status protocol.IssueWorkStatus, at int64) {
		t.Helper()
		reentered.Revision++
		reentered.UpdatedAt, reentered.WorkStatus = at, status
		saveCanonicalFixture(t, s, reentered, reentered.Revision-1)
	}
	moveGrouped(protocol.IssueInProgress, 77)
	assertEntry(reentered, 77, protocol.IssueStageEntryTransition)
	assertGrouped(reentered, 40, protocol.IssueStageEntryTransition)
	moveGrouped(protocol.IssueBacklog, 78)
	moveGrouped(protocol.IssueInReview, 79)
	assertGrouped(reentered, 79, protocol.IssueStageEntryTransition)
	gap.Revision++
	gap.UpdatedAt = 80
	gap.Plan.Title = "Updated title"
	saveCanonicalFixture(t, s, gap, gap.Revision-1)
	assertEntry(gap, 12, protocol.IssueStageEntryCreationFallback)

	next := gap
	next.Revision++
	next.UpdatedAt = 90
	next.State = protocol.IssueCancelled
	rollback := errors.New("rollback after writing stage entry")
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, next, gap.Revision, issueProvenance()); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertEntry(gap, 12, protocol.IssueStageEntryCreationFallback)
	saveCanonicalFixture(t, s, next, gap.Revision)
	assertEntry(next, 90, protocol.IssueStageEntryTransition)
}
