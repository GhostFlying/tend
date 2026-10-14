package testkit

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestClockAdvanceAndSet(t *testing.T) {
	c := NewClock()
	if got := c.Now(); !got.Equal(Epoch) {
		t.Fatalf("Now() = %v, want %v", got, Epoch)
	}
	c.Advance(90 * time.Second)
	if got, want := c.Now(), Epoch.Add(90*time.Second); !got.Equal(want) {
		t.Errorf("after Advance, Now() = %v, want %v", got, want)
	}
	later := Epoch.Add(24 * time.Hour)
	c.Set(later)
	if got := c.Now(); !got.Equal(later) {
		t.Errorf("after Set, Now() = %v, want %v", got, later)
	}
}

func TestIDGenIsReproducible(t *testing.T) {
	gen := func() []string {
		g := NewIDGen(NewClock(), 7)
		return []string{g.New(protocol.PrefixTask), g.New(protocol.PrefixRequest), g.New(protocol.PrefixRun)}
	}
	a, b := gen(), gen()
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("identifier %d differs between runs: %q vs %q", i, a[i], b[i])
		}
	}
	if !protocol.ValidID(protocol.PrefixTask, a[0]) {
		t.Errorf("generated %q is not a valid task ID", a[0])
	}
}

func TestIDGenSeedsDiffer(t *testing.T) {
	a := NewIDGen(NewClock(), 1).New(protocol.PrefixTask)
	b := NewIDGen(NewClock(), 2).New(protocol.PrefixTask)
	if a == b {
		t.Errorf("seeds 1 and 2 produced the same identifier %q", a)
	}
}
