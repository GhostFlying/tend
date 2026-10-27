package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ConversationSettings is the owner's persisted next-turn selection.
// Revision zero means the owner has not overridden the harness defaults.
type ConversationSettings struct {
	Model, Effort string
	Revision      int64
}

// GetConversationSettings reads the shared selection, or its zero value.
func GetConversationSettings(ctx context.Context, q Querier, session ...string) (ConversationSettings, error) {
	var settings ConversationSettings
	if len(session) > 0 && ConversationID(session[0]) != "main" {
		c, err := AgentConversation(ctx, q, session[0])
		if err != nil {
			return settings, err
		}
		return ConversationSettings{Model: c.Model, Effort: c.Effort, Revision: c.Revision}, nil
	}
	err := q.QueryRowContext(ctx, `SELECT model, effort, revision FROM conversation_settings WHERE singleton = 1`).
		Scan(&settings.Model, &settings.Effort, &settings.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("store: get conversation settings: %w", err)
	}
	return settings, nil
}

// PutConversationSettings records a revision checked by the app transaction.
func (t *Tx) PutConversationSettings(ctx context.Context, settings ConversationSettings, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_settings (singleton, model, effort, revision, updated_at)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (singleton) DO UPDATE SET model = excluded.model,
		effort = excluded.effort, revision = excluded.revision, updated_at = excluded.updated_at`,
		settings.Model, settings.Effort, settings.Revision, now)
	if err != nil {
		return fmt.Errorf("store: put conversation settings: %w", err)
	}
	return nil
}
