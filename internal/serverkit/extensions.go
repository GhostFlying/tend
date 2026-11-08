package serverkit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

// Hosted views follow server publication through the same content verifier as
// runners. Updating a view leaves adoption to mainthread's existing idle gate.
func runHostedExtensions(ctx context.Context, a *app.App, cfg MainAgentConfig, log *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := prepareHostedExtensions(ctx, a, cfg); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "hosted extensions remain pending", "reason", "content or category preparation is incomplete")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func prepareHostedExtensions(ctx context.Context, a *app.App, cfg MainAgentConfig) error {
	r, err := a.PublishedExtensions(ctx)
	if err != nil {
		return err
	}
	if err = cfg.extensionConsumer.Receive(ctx, r, a.ExtensionSource()); err != nil {
		return err
	}
	conversations, err := a.HostedExtensionConversations(ctx)
	if err != nil {
		return err
	}
	var pending error
	for _, id := range conversations {
		selected, named, err := a.NamedHostedSelection(ctx, id)
		if err != nil || !named || selected.RunnerManaged || !cfg.extensionConsumer.Enabled(selected.Runtime.ID) {
			continue
		}
		instance := cfg.Instance
		if id != "main" {
			instance += "/" + id
		}
		scope := selected.BindingInstance(instance)
		binding, _, err := cfg.extensionConsumer.PrepareScoped(ctx, selected.Extensions, selected.Runtime.ID, scope)
		if err == nil {
			err = binding.QueueAdoption(ctx, "coordinator:"+scope)
		}
		if err != nil {
			pending = errors.Join(pending, err)
		}
	}
	return pending
}
