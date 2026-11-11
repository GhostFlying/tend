package app

import (
	"context"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func extensionState(t *testing.T, h *harness) protocol.ExtensionState {
	t.Helper()
	var s protocol.ExtensionState
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		s, err = store.GetExtensionState(t.Context(), tx.Q())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s
}
func extensionEdit(t *testing.T, h *harness, action string, p protocol.ExtensionEditPayload, revision int64) protocol.Operation {
	t.Helper()
	return h.exec(owner, h.env("extensions."+action, protocol.Target{Type: "extensions", ID: "catalog"}, p, rev(revision)), EditExtensions{Action: action})
}
func extensionPublish(t *testing.T, h *harness) protocol.Operation {
	t.Helper()
	s := extensionState(t, h)
	r, _, err := extensions.NewRelease(s.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	env := h.env("extensions.publish", protocol.Target{Type: "extensions", ID: "catalog"}, protocol.ExtensionPublishPayload{Release: r}, rev(s.Revision))
	op, err := h.app.PublishExtensionDraft(t.Context(), owner, env, false)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestExtensionDraftPublishFailureRollbackAndReplay(t *testing.T) {
	h := newHarness(t)
	inventoryCategory(t, h, "implementer", 0, "active")
	validations := 0
	fail := false
	h.app.extensionValidator = func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error {
		validations++
		if fail {
			return errors.New("native package rejected")
		}
		return nil
	}
	mcp := protocol.ExtensionResource{Kind: "mcp", ID: "tools", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "stdio", Command: "fixture", EnvVars: []string{"LOCAL_TOKEN"}}}
	if op := extensionEdit(t, h, "resource_put", protocol.ExtensionEditPayload{Resource: &mcp}, 0); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	b := protocol.ExtensionBinding{CategoryID: "implementer", Choices: []protocol.ExtensionChoice{{Kind: "mcp", ID: "tools", Version: "1"}}, AgentsMD: "category first"}
	if op := extensionEdit(t, h, "binding_set", protocol.ExtensionEditPayload{Binding: &b}, 1); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if extensionState(t, h).Published != nil {
		t.Fatal("draft changed publication")
	}
	if op := extensionPublish(t, h); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	first := extensionState(t, h)
	if first.Revision != 3 || first.Published == nil || first.Catalog.Categories[0].Revision != 1 {
		t.Fatal(first)
	}
	b.Revision = 1
	b.AgentsMD = "changed"
	if op := extensionEdit(t, h, "binding_set", protocol.ExtensionEditPayload{Binding: &b}, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	s := extensionState(t, h)
	r, _, _ := extensions.NewRelease(s.Catalog)
	env := h.env("extensions.publish", protocol.Target{Type: "extensions", ID: "catalog"}, protocol.ExtensionPublishPayload{Release: r}, rev(s.Revision))
	fail = true
	if op, err := h.app.PublishExtensionDraft(t.Context(), owner, env, false); err != nil || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("native validation did not gate publication", op, err)
	}
	if extensionState(t, h).Published.ID != first.Published.ID {
		t.Fatal("failed validation replaced publication")
	}
	fail = false
	beforeFailedReplay := validations
	op, err := h.app.PublishExtensionDraft(t.Context(), owner, env, false)
	if err != nil || op.Status != protocol.OpSucceeded || validations != beforeFailedReplay+1 {
		t.Fatal("explicit unchanged publication did not revalidate after a definite rejection", op, err)
	}
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	before := validations
	replayed, err := h.app.PublishExtensionDraft(t.Context(), owner, env, false)
	if err != nil || replayed.Error == nil || replayed.Error.Code != protocol.CodeStaleRevision || validations != before {
		t.Fatal("old publication CAS reran validation", replayed, err)
	}
	s = extensionState(t, h)
	rollback := h.env("extensions.rollback", protocol.Target{Type: "extensions", ID: "catalog"}, protocol.ExtensionPublishPayload{Release: protocol.ExtensionRelease{ID: first.Published.ID}}, rev(s.Revision))
	if op, err = h.app.PublishExtensionDraft(t.Context(), owner, rollback, true); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	s = extensionState(t, h)
	if s.Catalog.Categories[0].AgentsMD != "category first" || s.Catalog.Resources[0].MCP.EnvVars[0] != "LOCAL_TOKEN" {
		t.Fatal("rollback lost original content", s)
	}
	if stale := extensionEdit(t, h, "resource_remove", protocol.ExtensionEditPayload{Kind: "mcp", ID: "tools", Version: "1"}, 0); stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
}

func TestExtensionPublishCannotBypassNativeValidationOrCAS(t *testing.T) {
	h := newHarness(t)
	s := extensionState(t, h)
	r, _, _ := extensions.NewRelease(s.Catalog)
	env := h.env("extensions.publish", protocol.Target{Type: "extensions", ID: "catalog"}, protocol.ExtensionPublishPayload{Release: r}, rev(0))
	if op := h.exec(owner, env, PublishExtensions{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("unvalidated direct command published", op)
	}
	if _, err := h.app.PublishExtensionDraft(t.Context(), runner("linux-1"), env, false); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("runner published", err)
	}
	h.app.extensionValidator = func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error {
		res := protocol.ExtensionResource{Kind: "mcp", ID: "concurrent", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "stdio", Command: "fixture"}}
		if op := extensionEdit(t, h, "resource_put", protocol.ExtensionEditPayload{Resource: &res}, 0); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		return nil
	}
	env = h.env("extensions.publish", protocol.Target{Type: "extensions", ID: "catalog"}, protocol.ExtensionPublishPayload{Release: r}, rev(0))
	op, err := h.app.PublishExtensionDraft(t.Context(), owner, env, false)
	if err != nil || op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("publication ignored concurrent edit", op, err)
	}
	if extensionState(t, h).Published != nil {
		t.Fatal("stale publication committed")
	}
}

func TestUnstagedResourceCannotEnterDraft(t *testing.T) {
	h := newHarness(t)
	r := protocol.ExtensionResource{Kind: "skill", ID: "fixture", Version: "1", Source: protocol.ExtensionSource{Type: "local"}, ContentSHA256: extensions.Hash([]byte("unverified")), Size: 10}
	op := extensionEdit(t, h, "resource_put", protocol.ExtensionEditPayload{Resource: &r}, 0)
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("unstaged package entered draft", op)
	}
	if s := extensionState(t, h); s.Revision != 0 || len(s.Catalog.Resources) != 0 {
		t.Fatal(s)
	}
}
