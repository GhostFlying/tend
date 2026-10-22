package traex

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// Models implements harness.ModelCatalog using the harness's visible catalog.
func (s *Session) Models(ctx context.Context) ([]harness.Model, error) {
	models := []harness.Model{}
	var cursor *string
	seen := map[string]bool{}
	for {
		var page ModelListResponse
		if err := s.conn.call(ctx, methodModelList, ModelListParams{Cursor: cursor, Limit: 100}, &page); err != nil {
			return nil, fmt.Errorf("traex: list models: %w", err)
		}
		for _, m := range page.Data {
			model := harness.Model{ID: m.ID, Name: m.Model, Label: m.DisplayName, Default: m.IsDefault,
				DefaultEffort: m.DefaultReasoningEffort, SupportedEfforts: []string{}}
			if m.ModelProviderID != nil {
				model.Provider = *m.ModelProviderID
			}
			for _, option := range m.SupportedReasoningEfforts {
				model.SupportedEfforts = append(model.SupportedEfforts, option.ReasoningEffort)
			}
			models = append(models, model)
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return models, nil
		}
		if seen[*page.NextCursor] {
			return nil, fmt.Errorf("traex: repeated model catalog cursor")
		}
		seen[*page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func (t *thread) setModelDefaults(res ThreadStartResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.defaults = harness.ModelSelection{Model: res.Model, Provider: res.ModelProvider}
	if res.ReasoningEffort != nil {
		t.defaults.Effort = *res.ReasoningEffort
	}
}

// ModelDefaults implements harness.ConfiguredThread.
func (t *thread) ModelDefaults() harness.ModelSelection {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.defaults
}
