package contextbuilder

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/store"
)

func TestSourcedFactsSurviveContextInvalidationAndBudget(t *testing.T) {
	e := newEnv(t)
	source, seq := e.message("owner", "Do not deploy without a new owner decision", "")
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,scope,kind,body,source_message_id,attribution,created_at) VALUES('fact-owner','global','constraint',? ,?,'owner',0)`, "Do not deploy without a new owner decision", source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		packet, err := e.b.Prepare(t.Context(), thread, seq+1)
		if err != nil {
			t.Fatal(err)
		}
		if packet == nil || !strings.Contains(packet.Text, "fact-owner") || !strings.Contains(packet.Text, "attribution=owner source="+source) {
			t.Fatalf("source or attribution lost: %+v", packet)
		}
		if err = e.b.Confirm(t.Context(), packet.ID); err != nil {
			t.Fatal(err)
		}
		if err = e.b.Invalidate(t.Context(), thread); err != nil {
			t.Fatal(err)
		}
	}
	rendered := Render(Snapshot{Full: true, Facts: []string{"fact-owner: original owner constraint"}}, 1)
	if !strings.Contains(rendered, "original owner constraint") {
		t.Fatal("budget dropped critical source fact")
	}
}
