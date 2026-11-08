package app

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagedHostedCategoryIsExplicitAndHistoricalPinsRemainFrozen(t *testing.T) {
	h := newHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	inventoryCategory(t, h, "reviewer", 0, "active")
	c := hostedConfigurationFixture()
	c.Definitions.Agents[0].Categories = []string{"planner", "reviewer"}
	op, prepared := acceptHostedFixture(t, h, c, 0)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared), WithHostedExtensions([]string{"hosted"}), WithExtensionValidator(func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error {
		return nil
	}))
	for i, category := range []string{"planner", "reviewer"} {
		b := protocol.ExtensionBinding{CategoryID: category, AgentsMD: category + " instructions"}
		if op := extensionEdit(t, h, "binding_set", protocol.ExtensionEditPayload{Binding: &b}, int64(i)); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if op := extensionPublish(t, h); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, _, err := h.app.NamedHostedSelection(t.Context(), "main"); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("ambiguous category was guessed", err)
	}
	view, err := h.app.HostedCategorySettings(t.Context(), "main")
	if err != nil || !view.CategoryEditable || view.CategoryID != "" || len(view.Categories) != 2 {
		t.Fatal(view, err)
	}
	set := func(category string, categoryRevision int64) protocol.Operation {
		return h.exec(owner, h.env("conversation.set_settings", protocol.Target{Type: "conversation"}, protocol.SetConversationSettingsPayload{CategoryID: category, ExpectedCategoryRevision: rev(categoryRevision)}, rev(1)), SetConversationSettings{})
	}
	if op := set("planner", 0); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	first, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named || first.Extensions.CategoryID != "planner" || first.Extensions.ReleaseID == "" {
		t.Fatal(first, named, err)
	}
	request := h.ids.New(protocol.PrefixRequest)
	if err = h.app.write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: request, ConversationID: "main", Selection: first, CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	if op := set("reviewer", 1); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second, _, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || second.Extensions.CategoryID != "reviewer" || first.BindingIdentity() != second.BindingIdentity() {
		t.Fatal("category changed native identity", first, second, err)
	}
	if second.Definition.Revision != first.Definition.Revision || second.ConfigurationRevision != first.ConfigurationRevision {
		t.Fatal("extensions edited Agent identity or model configuration")
	}
	frozen, _, err := h.app.HostedSelectionForAdmission(t.Context(), request, "main")
	if err != nil || frozen.Extensions != first.Extensions || frozen.CategoryID != first.CategoryID {
		t.Fatal("historical execution reselected current category", frozen, err)
	}
	if op := set("planner", 0); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("category CAS was ignored", op)
	}
	p := protocol.CreateConsultationPayload{AgentRef: "server-hosted/planner", Title: "Inspect a change"}
	if op := h.exec(owner, h.env("conversation.consult", protocol.Target{Type: "conversation"}, p, nil), CreateConsultation{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("consultation guessed an ambiguous category", op)
	}
	p.CategoryID = "reviewer"
	if op := h.exec(owner, h.env("conversation.consult", protocol.Target{Type: "conversation"}, p, nil), CreateConsultation{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
