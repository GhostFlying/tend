package journal

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpdaterDeniedAttemptRotatesOnlyAfterHigherRevisionAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(t.Context(), path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	firstRev, nextRev := int64(4), int64(7)
	first := protocol.Envelope{RequestID: "original", ExpectedControlRevision: &firstRev}
	next := protocol.Envelope{RequestID: "next", ExpectedControlRevision: &nextRev}
	if _, err := j.UpdaterAuthorization(t.Context(), "binding", first); err != nil {
		t.Fatal(err)
	}
	// No response received: even a newer control must replay the exact envelope.
	if got, err := j.UpdaterAuthorization(t.Context(), "binding", next); err != nil || got.RequestID != "original" || *got.ExpectedControlRevision != 4 {
		t.Fatal("uncertain request replaced", got, err)
	}
	if err := j.DenyUpdaterAuthorization(t.Context(), "binding", "original"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(t.Context(), path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if got, err := j.UpdaterAuthorization(t.Context(), "binding", first); err != nil || got.RequestID != "original" {
		t.Fatal("same-revision denial rotated", got, err)
	}
	if got, err := j.UpdaterAuthorization(t.Context(), "binding", next); err != nil || got.RequestID != "next" || *got.ExpectedControlRevision != 7 {
		t.Fatal("denied higher revision did not rotate", got, err)
	}
	// An old response must not mark the replacement as denied.
	if err := j.DenyUpdaterAuthorization(t.Context(), "binding", "original"); err != nil {
		t.Fatal(err)
	}
	newer := next
	newer.RequestID = "must-not-use"
	newerRev := int64(8)
	newer.ExpectedControlRevision = &newerRev
	if got, err := j.UpdaterAuthorization(t.Context(), "binding", newer); err != nil || got.RequestID != "next" {
		t.Fatal("late old denial poisoned replacement", got, err)
	}
}
