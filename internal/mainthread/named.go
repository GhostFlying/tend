package mainthread

import (
	"slices"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (m *Manager) namedSelection() *hostedconfig.Selection {
	m.namedMu.RLock()
	defer m.namedMu.RUnlock()
	if m.named == nil {
		return nil
	}
	copy := *m.named
	return &copy
}

// Different configuration bindings never resume a native generation carrying
// another definition's instructions. Recovery selects the old binding first.
func (m *Manager) bindingInstance() string {
	selected := m.namedSelection()
	if selected == nil {
		return m.cfg.Instance
	}
	return selected.BindingInstance(m.cfg.Instance)
}

func resolveNamedModel(models []harness.Model, selected hostedconfig.Selection) (harness.ModelSelection, error) {
	if selected.AppliedModel != "" {
		return harness.ModelSelection{Model: selected.AppliedModel, Provider: selected.AppliedProvider, Effort: selected.AppliedEffort}, nil
	}
	for _, model := range models {
		if model.ID != selected.Definition.Model && model.Name != selected.Definition.Model {
			continue
		}
		effort := selected.Definition.Effort
		if effort == "" {
			effort = model.DefaultEffort
		}
		if len(model.SupportedEfforts) > 0 && !slices.Contains(model.SupportedEfforts, effort) {
			return harness.ModelSelection{}, protocol.Errorf(protocol.CodeInvalidRequest, "named effort is unsupported by the selected model")
		}
		if len(model.SupportedEfforts) == 0 && effort != model.DefaultEffort {
			return harness.ModelSelection{}, protocol.Errorf(protocol.CodeInvalidRequest, "named effort is unsupported by the selected model")
		}
		return harness.ModelSelection{Model: model.Name, Provider: model.Provider, Effort: effort}, nil
	}
	return harness.ModelSelection{}, protocol.Errorf(protocol.CodeInvalidRequest, "the named model is unavailable; update its definition")
}

func sameNamedBinding(a, b hostedconfig.Selection) bool {
	return a.BindingIdentity() == b.BindingIdentity()
}
