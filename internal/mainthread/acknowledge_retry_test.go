package mainthread

import (
	"context"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

type receiptWriter struct {
	attempts int
	sequence int64
	next     error
}

func (w *receiptWriter) AcknowledgeEvent(_ context.Context, event harness.Event) error {
	w.attempts++
	if event.CoordinatorSequence != w.sequence {
		return errors.New("receipt identity changed")
	}
	if w.attempts == 1 {
		return context.DeadlineExceeded
	}
	return w.next
}

func TestAppliedEventRetriesOnlyOriginalReceiptAfterWriterTimeout(t *testing.T) {
	permanent := errors.New("unknown original")
	for _, outcome := range []error{nil, permanent} {
		w := &receiptWriter{sequence: 42, next: outcome}
		err := acknowledgeAppliedEvent(t.Context(), w, harness.Event{CoordinatorSequence: 42})
		if !errors.Is(err, outcome) || w.attempts != 2 {
			t.Fatal(err, w.attempts)
		}
	}
}
