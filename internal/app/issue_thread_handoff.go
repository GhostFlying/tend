package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// IssueThreadHandoffFacts uses the registered conversation's scope without
// admitting a turn, advancing source review or reading the global Main packet.
func (a *App) IssueThreadHandoffFacts(ctx context.Context, conversation, issue string) (json.RawMessage, error) {
	var out json.RawMessage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		c, err := store.AgentConversation(ctx, q, conversation)
		if err != nil {
			return err
		}
		if c.Kind != "issue" || c.WorkItemID == "" || issue != "" && c.WorkItemID != issue {
			return protocol.Errorf(protocol.CodeForbidden, "handoff conversation does not belong to this Issue")
		}
		current, err := store.GetCanonicalIssue(ctx, q, c.WorkItemID)
		facts := map[string]any{"conversation_id": c.ID, "conversation_revision": c.Revision, "conversation_through": c.LastMessageSeq}
		if errors.Is(err, store.ErrNotFound) {
			item, err := views.Issue(ctx, q, c.WorkItemID)
			if err != nil {
				return err
			}
			context, err := views.IssueContext(ctx, q, c.WorkItemID, 0)
			if err != nil {
				return err
			}
			facts["work_item"], facts["context"] = item, context
		} else {
			if err != nil {
				return err
			}
			item, err := views.CanonicalIssue(ctx, q, current.IssueID, 0)
			if err != nil {
				return err
			}
			context, err := views.CanonicalIssueContextIncremental(ctx, q, current.IssueID, protocol.IssueContextQuery{View: "incremental"})
			if err != nil {
				return err
			}
			inherited, err := freezeCanonicalTaskContext(ctx, q, current)
			if err != nil {
				return err
			}
			facts["issue"], facts["context"], facts["inherited_context"] = item, context, inherited[1:]
		}
		out, err = json.Marshal(facts)
		return err
	})
	return out, err
}
