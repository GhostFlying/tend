package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func extensionTarget(ctx context.Context, q store.Querier, host string) (*protocol.ExtensionTarget, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityManagedExtensions) || runtime.Runtime.Extensions == nil || !runtime.Runtime.Extensions.Enabled {
		return nil, nil
	}
	state, err := store.GetExtensionState(ctx, q)
	if err != nil || state.Published == nil {
		return nil, err
	}
	id := state.Published.ID
	return &protocol.ExtensionTarget{Release: *state.Published, ManifestPath: "/runner/v1/extensions/" + id + "/manifest", ContentBasePath: "/runner/v1/extensions/" + id + "/content"}, nil
}

// OpenExtensionContent only exposes complete, previously published objects to
// an authenticated active runner with the negotiated managed-content contract.
func (a *App) OpenExtensionContent(ctx context.Context, p protocol.Principal, release, hash string) (io.ReadCloser, error) {
	var manifest []byte
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		x := &Exec{Principal: p}
		if _, err := requireActiveHost(ctx, x, tx.Q()); err != nil {
			return err
		}
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityManagedExtensions) {
			return protocol.Errorf(protocol.CodeForbidden, "managed extensions were not negotiated")
		}
		r, _, err := store.GetExtensionRelease(ctx, tx.Q(), release)
		if err != nil {
			return errNotFound(err, "extension release is unavailable")
		}
		if hash == "" {
			manifest, err = json.Marshal(r)
			return err
		}
		ok, err := store.PublishedExtensionContent(ctx, tx.Q(), release, hash)
		if err != nil {
			return err
		}
		if !ok {
			return protocol.Errorf(protocol.CodeNotFound, "content is not in this published release")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return io.NopCloser(bytes.NewReader(manifest)), nil
	}
	return a.store.FileStore().Open(hash)
}
