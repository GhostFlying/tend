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
	if selected, named, err := m.app.NamedHostedSelection(ctx, m.cfg.ConversationID); err != nil {
		return protocol.ConversationSettingsView{}, err
	} else if named {
		models, catalogErr := m.modelCatalog(ctx)
		return protocol.ConversationSettingsView{Enabled: true, Available: catalogErr == nil && len(models) > 0, Revision: selected.ConfigurationRevision, Model: selected.Definition.Model, Effort: selected.Definition.Effort, Models: modelViews(models), Source: "named_definition", ReadOnly: true}, nil
	}
	settings, err := m.app.ConversationSettings(ctx, m.cfg.ConversationID)
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
	var selected *harness.Model
	for i := range models {
		model := &models[i]
		if (model.ID != selection.Model && model.Name != selection.Model) ||
			(selection.Provider != "" && model.Provider != "" && selection.Provider != model.Provider) {
			continue
		}
		// An omitted catalog provider inherits the configured provider. Prefer
		// an explicit matching namespace when both forms are present.
		if selected == nil || model.Provider == selection.Provider {
			selected = model
		}
		if model.Provider == selection.Provider {
			break
		}
	}
	if selected != nil {
		view.Model = selected.ID
		if view.Effort == "" {
			view.Effort = selected.DefaultEffort
		}
	}
	return view, nil
}

// turnModel snapshots settings once a queued message starts processing. Later
// owner writes affect subsequent turns, including queued offline messages.
func (m *Manager) turnModel(ctx context.Context, th harness.Thread, request string) (harness.ModelSelection, int64, error) {
	if selected, named, err := m.app.HostedSelectionForAdmission(ctx, request, m.cfg.ConversationID); err != nil {
		return harness.ModelSelection{}, 0, err
	} else if named {
		if current := m.namedSelection(); current != nil && !sameNamedBinding(*current, selected) {
			m.mu.Lock()
			session, active := m.session, m.active
			m.mu.Unlock()
			if active == nil && session != nil {
				_ = session.Close()
			}
			return harness.ModelSelection{}, 0, protocol.Errorf(protocol.CodeInvalidTransition, "named definition changed; resume its frozen generation before starting this execution")
		}
		var models []harness.Model
		if selected.AppliedModel == "" {
			models, err = m.modelCatalog(ctx)
			if err != nil {
				return harness.ModelSelection{}, 0, err
			}
		}
		actual, err := resolveNamedModel(models, selected)
		return actual, selected.ConfigurationRevision, err
	}
	settings, err := m.app.ConversationSettings(ctx, m.cfg.ConversationID)
	if err != nil {
		return harness.ModelSelection{}, 0, err
	}
	if settings.Revision == 0 && settings.Model == "" {
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
