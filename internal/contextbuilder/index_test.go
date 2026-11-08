package contextbuilder

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedIndexRemainsBoundedAfterCompactAndDoesNotCopyOriginals(t *testing.T) {
	e := newEnv(t)
	_, seq := e.message("owner", strings.Repeat("ORIGINAL_OWNER_CONSTRAINT", 10000), "")
	for range 2 {
		p, err := e.b.PrepareIndex(t.Context(), thread, seq)
		if err != nil || p == nil {
			t.Fatal(p, err)
		}
		if len(p.Text) > protocol.ContextIndexBudget || strings.Contains(p.Text, "ORIGINAL_OWNER_CONSTRAINT") || !strings.Contains(p.Text, "conversation_context_index") {
			t.Fatal("unbounded automatic context", len(p.Text))
		}
		if err = e.b.Confirm(t.Context(), p.ID); err != nil {
			t.Fatal(err)
		}
		if err = e.b.Invalidate(t.Context(), thread); err != nil {
			t.Fatal(err)
		}
	}
}
