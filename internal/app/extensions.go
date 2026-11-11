package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// StageExtension authenticates and verifies immutable bytes before opening the
// ledger transaction. Only the receipt and content reference enter the ledger.
func (a *App) StageExtension(ctx context.Context, p protocol.Principal, in protocol.ExtensionStageRequest) (protocol.Operation, error) {
	if err := allowKinds(p, protocol.PrincipalOwner); err != nil {
		return protocol.Operation{}, err
	}
	r := in.Package.Resource
	if err := extensions.ValidateResource(r); err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid extension resource: %s", err)
	}
	if err := extensions.VerifyContent(r, in.Package.Content); err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid package content")
	}
	if err := extensions.ValidatePackage(ctx, r.Kind, in.Package.Content); err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid package: %s", err)
	}
	object, err := a.store.FileStore().Put(ctx, bytes.NewReader(in.Package.Content))
	if err != nil {
		return protocol.Operation{}, err
	}
	if object.SHA256 != r.ContentSHA256 || object.Size != r.Size {
		return protocol.Operation{}, fmt.Errorf("extension staging integrity mismatch")
	}
	raw, _ := json.Marshal(protocol.ExtensionEditPayload{Resource: &r})
	env := protocol.Envelope{RequestID: in.RequestID, Operation: "extensions.stage", Target: protocol.Target{Type: "extensions", ID: "catalog"}, ExpectedControlRevision: &in.ExpectedControlRevision, Payload: raw}
	return a.Execute(ctx, p, &env, stageExtension{verified: r})
}

type stageExtension struct{ verified protocol.ExtensionResource }

func (stageExtension) Operation() string { return "extensions.stage" }
func (stageExtension) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c stageExtension) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ExtensionEditPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Resource == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "missing staged resource")
	}
	want, _ := json.Marshal(c.verified)
	got, _ := json.Marshal(p.Resource)
	if !bytes.Equal(want, got) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unverified extension content")
	}
	s, err := store.GetExtensionState(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if err = extensionCAS(x, s); err != nil {
		return Outcome{}, err
	}
	if err = tx.InsertExtensionContent(ctx, *p.Resource, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: p.Resource}, nil
}

// EditExtensions modifies only the owner-controlled draft, never a running view.
type EditExtensions struct{ Action string }

func (c EditExtensions) Operation() string { return "extensions." + c.Action }
func (EditExtensions) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func extensionCAS(x *Exec, s protocol.ExtensionState) error {
	if x.Env.Target != (protocol.Target{Type: "extensions", ID: "catalog"}) || x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != s.Revision {
		return protocol.Errorf(protocol.CodeStaleRevision, "extension catalog revision has changed")
	}
	return nil
}

func (c EditExtensions) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ExtensionEditPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	s, err := store.GetExtensionState(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if err = extensionCAS(x, s); err != nil {
		return Outcome{}, err
	}
	switch c.Action {
	case "resource_put":
		if p.Resource == nil || p.Binding != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "one resource is required")
		}
		r := *p.Resource
		if err = extensions.ValidateResource(r); err != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid resource: %s", err)
		}
		prior, err := store.ExtensionResourceVersion(ctx, tx.Q(), r.Kind, r.ID, r.Version)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Outcome{}, err
		}
		if err == nil {
			old, _ := json.Marshal(prior)
			next, _ := json.Marshal(r)
			if !bytes.Equal(old, next) {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "resource versions are immutable, including removed versions")
			}
		}
		if err = tx.InsertExtensionResourceVersion(ctx, r); err != nil {
			return Outcome{}, err
		}
		if r.Kind != "mcp" {
			ok, err := store.ExtensionContentExists(ctx, tx.Q(), r)
			if err != nil {
				return Outcome{}, err
			}
			if !ok {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "package must be staged before editing the draft")
			}
		}
		index := slices.IndexFunc(s.Catalog.Resources, func(old protocol.ExtensionResource) bool {
			return old.Kind == r.Kind && old.ID == r.ID && old.Version == r.Version
		})
		if index >= 0 {
			old, _ := json.Marshal(s.Catalog.Resources[index])
			next, _ := json.Marshal(r)
			if !bytes.Equal(old, next) {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "resource versions are immutable; choose a new version")
			}
		} else {
			s.Catalog.Resources = append(s.Catalog.Resources, r)
		}
	case "resource_remove":
		index := slices.IndexFunc(s.Catalog.Resources, func(r protocol.ExtensionResource) bool {
			return r.Kind == p.Kind && r.ID == p.ID && r.Version == p.Version
		})
		if index < 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "draft resource version does not exist")
		}
		s.Catalog.Resources = slices.Delete(s.Catalog.Resources, index, index+1)
	case "binding_set":
		if p.Binding == nil || p.Resource != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "one category or Agent binding is required")
		}
		b := *p.Binding
		if err = extensions.ValidateBinding(b); err != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid binding: %s", err)
		}
		bindings := &s.Catalog.Agents
		if b.CategoryID != "" {
			category, err := store.GetAgentCategory(ctx, tx.Q(), b.CategoryID)
			if err != nil {
				return Outcome{}, errNotFound(err, "category does not exist")
			}
			if category.State != "active" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "category is retired")
			}
			bindings = &s.Catalog.Categories
		} else {
			// Validate against accepted declarations, including a previously known
			// Agent temporarily absent from the current inventory.
			declarations, err := store.ListAgentCapabilityDeclarations(ctx, tx.Q(), b.Agent.ExecutionEnvironmentID)
			if err != nil {
				return Outcome{}, err
			}
			known := slices.ContainsFunc(declarations, func(a protocol.AgentInventoryDefinition) bool { return a.Identity == b.Agent })
			if x.hostedConfiguration != nil {
				known = known || slices.ContainsFunc(x.hostedConfiguration.Public().Agents, func(a protocol.AgentInventoryDefinition) bool { return a.Identity == b.Agent })
			}
			if !known {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown full Agent identity")
			}
		}
		index := slices.IndexFunc(*bindings, func(old protocol.ExtensionBinding) bool {
			return old.CategoryID == b.CategoryID && old.Agent == b.Agent
		})
		var previous int64
		if index >= 0 {
			previous = (*bindings)[index].Revision
		}
		if b.Revision != previous {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "extension binding revision has changed")
		}
		bindingID := "category:" + b.CategoryID
		if b.CategoryID == "" {
			bindingID = "agent:" + b.Agent.ExecutionEnvironmentID + "/" + b.Agent.LocalID
		}
		latest, err := store.ExtensionBindingRevision(ctx, tx.Q(), bindingID)
		if err != nil {
			return Outcome{}, err
		}
		b.Revision = latest + 1
		if err = tx.InsertExtensionBindingVersion(ctx, bindingID, b); err != nil {
			return Outcome{}, err
		}
		if index < 0 {
			*bindings = append(*bindings, b)
		} else {
			(*bindings)[index] = b
		}
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown extension draft operation")
	}
	s.Revision++
	s.Catalog.Revision++
	s.Catalog, err = extensions.CanonicalCatalog(s.Catalog)
	if err != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid extension catalog: %s", err)
	}
	if err = tx.InsertExtensionState(ctx, s, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return extensionOutcome(s, "extensions.draft_updated"), nil
}

func extensionOutcome(s protocol.ExtensionState, kind string) Outcome {
	return Outcome{Result: s, Notify: true, Events: []Event{{ObjectType: "extensions", ObjectID: "catalog", ObjectVersion: s.Revision, Kind: kind, Body: map[string]any{"published": s.Published}}}}
}

type PublishExtensions struct {
	Rollback        bool
	validatedID     string
	validationError error
}

func WithExtensionValidator(validate func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error) Option {
	return func(a *App) { a.extensionValidator = validate }
}

// PublishExtensionDraft completes immutable I/O and native validation before
// the normal app transaction. That transaction still owns deduplication, CAS,
// the release reference, events and request receipt.
func (a *App) PublishExtensionDraft(ctx context.Context, p protocol.Principal, env *protocol.Envelope, rollback bool) (protocol.Operation, error) {
	if err := allowKinds(p, protocol.PrincipalOwner); err != nil {
		return protocol.Operation{}, err
	}
	if err := env.Validate(); err != nil {
		return protocol.Operation{}, err
	}
	cmd := PublishExtensions{Rollback: rollback}
	var candidate protocol.ExtensionRelease
	var uses []extensions.ValidationUse
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		s, err := store.GetExtensionState(ctx, tx.Q())
		if err != nil {
			return err
		}
		x := &Exec{Env: env}
		if err = extensionCAS(x, s); err != nil {
			return err
		}
		if rollback {
			var payload protocol.ExtensionPublishPayload
			if err = decodePayload(env, &payload); err != nil {
				return err
			}
			candidate, _, err = store.GetExtensionRelease(ctx, tx.Q(), payload.Release.ID)
			if err != nil {
				return err
			}
			s.Catalog = candidate.Catalog
			s.Catalog.Revision = *env.ExpectedControlRevision
		}
		candidate, _, err = extensions.NewRelease(s.Catalog)
		if err != nil {
			return err
		}
		uses, err = extensionValidationUses(ctx, tx.Q(), candidate)
		return err
	})
	if err != nil {
		if protocol.CodeOf(err) != protocol.CodeInternal && protocol.CodeOf(err) != "" {
			return a.Execute(ctx, p, env, cmd)
		}
		return protocol.Operation{}, err
	}
	{
		if a.extensionValidator == nil {
			cmd.validationError = protocol.Errorf(protocol.CodeInvalidTransition, "native extension validation is unavailable on this server")
			return a.Execute(ctx, p, env, cmd)
		}
		source := func(_ context.Context, r protocol.ExtensionResource) (io.ReadCloser, error) {
			return a.store.FileStore().Open(r.ContentSHA256)
		}
		if err = a.extensionValidator(ctx, candidate, source, uses); err != nil {
			if ctx.Err() != nil {
				return protocol.Operation{}, ctx.Err()
			}
			cmd.validationError = protocol.Errorf(protocol.CodeInvalidRequest, "extension release failed native validation: %s", err)
			return a.Execute(ctx, p, env, cmd)
		}
		cmd.validatedID = candidate.ID
	}
	return a.Execute(ctx, p, env, cmd)
}

func (c PublishExtensions) Operation() string {
	if c.Rollback {
		return "extensions.rollback"
	}
	return "extensions.publish"
}
func (PublishExtensions) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c PublishExtensions) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	s, err := store.GetExtensionState(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if err = extensionCAS(x, s); err != nil {
		return Outcome{}, err
	}
	var p protocol.ExtensionPublishPayload
	if err = decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	var r protocol.ExtensionRelease
	var raw []byte
	if c.Rollback {
		r, _, err = store.GetExtensionRelease(ctx, tx.Q(), p.Release.ID)
		if err != nil {
			return Outcome{}, errNotFound(err, "rollback release does not exist")
		}
		s.Catalog = r.Catalog
		s.Catalog.Revision = *x.Env.ExpectedControlRevision
	} else if p.Release.Catalog.Revision != s.Catalog.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "release was prepared from a different draft")
	}
	r, raw, err = extensions.NewRelease(s.Catalog)
	if err != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid release: %s", err)
	}
	if !c.Rollback && r.ID != p.Release.ID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release differs from current draft")
	}
	if c.validationError != nil {
		return Outcome{}, c.validationError
	}
	if c.validatedID != r.ID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "release requires completed native validation")
	}
	for _, resource := range r.Catalog.Resources {
		if resource.Kind != "mcp" {
			ok, err := store.ExtensionContentExists(ctx, tx.Q(), resource)
			if err != nil {
				return Outcome{}, err
			}
			if !ok {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release content was not staged")
			}
		}
	}
	// Resolve each declared category against all known Agent overrides. Native
	// package validation is recorded separately before this control commit.
	for _, b := range r.Catalog.Categories {
		for _, a := range r.Catalog.Agents {
			if _, err = extensions.Resolve(r, b.CategoryID, a.Agent); err != nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid release composition: %s", err)
			}
		}
	}
	ref := protocol.ExtensionReleaseRef{ID: r.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}
	if err = tx.InsertExtensionRelease(ctx, r, ref, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	s.Published = &ref
	s.Revision++
	if err = tx.InsertExtensionState(ctx, s, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return extensionOutcome(s, "extensions.published"), nil
}
