package mainthread

import (
	"context"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func modelViews(models []harness.Model) []protocol.ConversationModel {
	views := make([]protocol.ConversationModel, 0, len(models))
	for _, model := range models {
		efforts := append([]string{}, model.SupportedEfforts...)
		views = append(views, protocol.ConversationModel{ID: model.ID, Label: model.Label,
			SupportedEfforts: efforts, DefaultEffort: model.DefaultEffort})
	}
	return views
}

func (m *Manager) modelCatalog(ctx context.Context) ([]harness.Model, error) {
	m.mu.Lock()
	session := m.session
	m.mu.Unlock()
	provider, ok := session.(harness.ModelCatalog)
	if !ok {
		return nil, fmt.Errorf("mainthread: the hosted model catalog is not ready")
	}
	return provider.Models(ctx)
}

func (m *Manager) defaults(th harness.Thread) harness.ModelSelection {
	if configured, ok := th.(harness.ConfiguredThread); ok {
		return configured.ModelDefaults()
	}
	selection := harness.ModelSelection{Model: m.cfg.Thread.Model}
	if effort, ok := m.cfg.Thread.Config["model_reasoning_effort"].(string); ok {
		selection.Effort = effort
	}
	return selection
}

// Settings returns the shared next-turn settings and the live harness catalog.
// A disconnected harness leaves persisted selections readable but unavailable.
func (m *Manager) Settings(ctx context.Context) (protocol.ConversationSettingsView, error) {
	settings, err := m.app.ConversationSettings(ctx)
	if err != nil {
		return protocol.ConversationSettingsView{}, err
	}
	view := protocol.ConversationSettingsView{Enabled: true, Revision: settings.Revision,
		Model: settings.Model, Effort: settings.Effort, Models: []protocol.ConversationModel{}}
	models, err := m.modelCatalog(ctx)
	if err != nil {
		return view, nil
	}
	view.Models, view.Available = modelViews(models), len(models) > 0
	if settings.Revision != 0 {
		return view, nil
	}
	m.mu.Lock()
	th := m.thread
	m.mu.Unlock()
	if th == nil {
		view.Available = false
		return view, nil
	}
	selection := m.defaults(th)
	view.Model, view.Effort = selection.Model, selection.Effort
	for _, model := range models {
		if (model.ID == selection.Model || model.Name == selection.Model) &&
			(selection.Provider == "" || selection.Provider == model.Provider) {
			view.Model = model.ID
			if view.Effort == "" {
				view.Effort = model.DefaultEffort
			}
			break
		}
	}
	return view, nil
}

// turnModel snapshots settings once a queued message starts processing. Later
// owner writes affect subsequent turns, including queued offline messages.
func (m *Manager) turnModel(ctx context.Context, th harness.Thread) (harness.ModelSelection, int64, error) {
	settings, err := m.app.ConversationSettings(ctx)
	if err != nil {
		return harness.ModelSelection{}, 0, err
	}
	if settings.Revision == 0 {
		return m.defaults(th), 0, nil
	}
	models, err := m.modelCatalog(ctx)
	if err != nil {
		return harness.ModelSelection{}, 0, err
	}
	for _, model := range models {
		if model.ID != settings.Model {
			continue
		}
		supported := slices.Contains(model.SupportedEfforts, settings.Effort)
		if len(model.SupportedEfforts) == 0 {
			supported = settings.Effort == model.DefaultEffort
		}
		if !supported {
			return harness.ModelSelection{}, 0, protocol.Errorf(protocol.CodeInvalidRequest, "the selected effort is no longer supported; update conversation settings")
		}
		return harness.ModelSelection{Model: model.Name, Provider: model.Provider, Effort: settings.Effort}, settings.Revision, nil
	}
	return harness.ModelSelection{}, 0, protocol.Errorf(protocol.CodeInvalidRequest, "the selected model is no longer available; update conversation settings")
}
