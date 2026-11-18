package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueShortNumberMigrationAndRollback(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: db, read: db, now: clock.Now}
	t.Cleanup(func() { old.Close() })
	if err := old.migrate(ctx, 146); err != nil {
		t.Fatal(err)
	}
	legacy := protocol.WorkItem{
		WorkItemID: "wi_00000000000000000000000003", Status: protocol.WorkItemOpen,
		IssuePlan: protocol.IssuePlan{Title: "Legacy work item", Project: "default", Intent: "Retain its original definition"},
		Revision:  1, CreatedAt: 1, UpdatedAt: 1,
	}
	closed := canonicalIssueFixture("wi_00000000000000000000000002")
	closed.State = protocol.IssueClosed
	closed.CreatedAt, closed.UpdatedAt = 3, 3
	if err := old.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, closed, 0, issueProvenance()); err != nil {
			return err
		}
		return tx.SaveIssue(ctx, legacy, 0, "historical creation")
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
	if err := s.Read(ctx, func(tx *ReadTx) error {
		view, err := GetIssue(ctx, tx.Q(), legacy.WorkItemID)
		if err != nil {
			return err
		}
		if view.ShortCode != "T1" || view.Title != legacy.Title {
			t.Errorf("legacy view = %+v", view)
		}
		if _, err := IssueIdentity(ctx, tx.Q(), legacy.WorkItemID); !errors.Is(err, ErrNotFound) {
			t.Errorf("legacy-only work item has a canonical link: %v", err)
		}
		history, err := GetCanonicalIssueRevision(ctx, tx.Q(), closed.IssueID, 1)
		if err != nil {
			return err
		}
		if history.ShortCode != "T2" || history.State != protocol.IssueClosed {
			t.Errorf("historical Issue = %+v", history)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newIssue := canonicalIssueFixture("wi_00000000000000000000000001")
	rollback := errors.New("rollback after allocation")
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, newIssue, 0, issueProvenance()); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		_, err := IssueShortCode(ctx, tx.Q(), newIssue.IssueID)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("rolled back identity visible: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saveCanonicalFixture(t, s, newIssue, 0)
	if got := readCanonicalFixture(t, s, newIssue.IssueID, 0); got.ShortCode != "T3" {
		t.Fatalf("new Issue = %+v", got)
	}
}
