package traex

import (
	"errors"
	"testing"
)

func TestClosePreservesUnknownOutcome(t *testing.T) {
	_, s := newFake(t)
	want := errors.New("native stop outcome unknown")
	stop := s.stop
	s.stop = func() error {
		_ = stop()
		return want
	}
	for i := range 2 {
		if err := s.Close(); !errors.Is(err, want) {
			t.Fatalf("close %d lost original outcome: %v", i, err)
		}
	}
}
