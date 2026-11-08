package app

import (
	"context"
	"io"
	"slices"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func WithHostedExtensions(runtimes []string) Option {
	return func(a *App) {
		a.managedHostedRuntimes = map[string]bool{}
		for _, id := range runtimes {
			a.managedHostedRuntimes[id] = true
		}
	}
}

func selectedCategory(categories []string, explicit string, required bool) (string, error) {
	if explicit != "" {
		if !slices.Contains(categories, explicit) {
			return "", protocol.Errorf(protocol.CodeInvalidRequest, "selected category is not declared by this Agent")
		}
		return explicit, nil
	}
	if len(categories) == 1 {
		return categories[0], nil
	}
	if required {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "choose one category explicitly for this session")
	}
	return "", nil
}

func (a *App) currentHostedSelection(ctx context.Context, q store.Querier, conversation string) (hostedconfig.Selection, bool, error) {
	selected, named, err := namedHostedSelection(ctx, q, a.hostedConfiguration, conversation)
	if err != nil || !named || selected.RunnerManaged {
		return selected, named, err
	}
	category, _, err := store.ExtensionConversationCategory(ctx, q, conversation)
	if err != nil {
		return selected, named, err
	}
	if category == "" {
		category = selected.CategoryID
	}
	if category == "" && !a.managedHostedRuntimes[selected.Runtime.ID] {
		return selected, named, nil
	}
	category, err = selectedCategory(selected.Definition.Categories, category, a.managedHostedRuntimes[selected.Runtime.ID])
	if err != nil {
		return selected, named, err
	}
	selected.CategoryID = category
	if !a.managedHostedRuntimes[selected.Runtime.ID] {
		return selected, named, nil
	}
	state, err := store.GetExtensionState(ctx, q)
	if err != nil {
		return selected, named, err
	}
	if state.Published == nil {
		return selected, named, protocol.Errorf(protocol.CodeInvalidTransition, "managed extension baseline has not been published")
	}
	release, _, err := store.GetExtensionRelease(ctx, q, state.Published.ID)
	if err != nil {
		return selected, named, err
	}
	combo, err := extensions.Resolve(release, category, selected.Identity)
	if err != nil {
		return selected, named, protocol.Errorf(protocol.CodeInvalidTransition, "managed extension composition is unavailable")
	}
	selected.Extensions = combo.Pin
	return selected, named, nil
}

// HostedCategorySettings remains readable before a session has selected a
// category or started its native process, including an ambiguous main Agent.
func (a *App) HostedCategorySettings(ctx context.Context, conversation string) (protocol.ConversationSettingsView, error) {
	var view protocol.ConversationSettingsView
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		selected, named, err := namedHostedSelection(ctx, tx.Q(), a.hostedConfiguration, conversation)
		if err != nil {
			return err
		}
		if !named {
			return nil
		}
		category, revision, err := store.ExtensionConversationCategory(ctx, tx.Q(), conversation)
		if err != nil {
			return err
		}
		if category == "" {
			category = selected.CategoryID
		}
		category, err = selectedCategory(selected.Definition.Categories, category, false)
		if err != nil {
			return err
		}
		view = protocol.ConversationSettingsView{Enabled: true, Available: true, Revision: selected.ConfigurationRevision, ReadOnly: true, Source: "named_definition", Model: selected.Definition.Model, Effort: selected.Definition.Effort, Categories: slices.Clone(selected.Definition.Categories), CategoryID: category, CategoryRevision: revision, CategoryEditable: !selected.RunnerManaged}
		return nil
	})
	return view, err
}

func (a *App) PublishedExtensions(ctx context.Context) (protocol.ExtensionRelease, error) {
	var release protocol.ExtensionRelease
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		s, err := store.GetExtensionState(ctx, tx.Q())
		if err != nil {
			return err
		}
		if s.Published == nil {
			return store.ErrNotFound
		}
		release, _, err = store.GetExtensionRelease(ctx, tx.Q(), s.Published.ID)
		return err
	})
	return release, err
}

func (a *App) ExtensionSource() extensions.ContentSource {
	return func(_ context.Context, r protocol.ExtensionResource) (io.ReadCloser, error) {
		return a.store.FileStore().Open(r.ContentSHA256)
	}
}

func (a *App) RetainedExtensionRelease(ctx context.Context, id string) (protocol.ExtensionRelease, error) {
	var release protocol.ExtensionRelease
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		release, _, err = store.GetExtensionRelease(ctx, tx.Q(), id)
		return err
	})
	return release, err
}

func (a *App) HostedExtensionConversations(ctx context.Context) ([]string, error) {
	ids := []string{"main"}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		more, err := store.FollowupIDs(ctx, tx.Q(), `SELECT conversation_id FROM consultation_conversations ORDER BY conversation_id`)
		ids = append(ids, more...)
		return err
	})
	return ids, err
}
