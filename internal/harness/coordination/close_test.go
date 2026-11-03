package coordination

import (
	"context"
	"errors"
	"testing"
)

type uncertainCloseTransport struct{ err error }

func (t uncertainCloseTransport) Call(context.Context, string, any, any) error { return t.err }
func (uncertainCloseTransport) Events(ctx context.Context, _ int64) ([]Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestClosePreservesUnknownOutcome(t *testing.T) {
	want := errors.New("native close outcome unknown")
	s := New(t.Context(), uncertainCloseTransport{err: want})
	for i := range 2 {
		if err := s.Close(); !errors.Is(err, want) {
			t.Fatalf("close %d lost original outcome: %v", i, err)
		}
	}
}
