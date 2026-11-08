package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Service) conversationContextIndex(ctx context.Context, c Call) (any, error) {
	var args protocol.ConversationContextQuery
	if err := decode(c.Arguments, &args); err != nil {
		return nil, err
	}
	id := store.ConversationID(args.ConversationID)
	var v protocol.ConversationContextIndex
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		conv, err := store.AgentConversation(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if err = conversationHistoryScope(c, conv.ID); err != nil {
			return err
		}
		v, err = views.ConversationContextIndex(ctx, tx.Q(), id, args)
		return err
	})
	return v, err
}

// Consultation has no work-item ID. History isolation uses the registered
// conversation identity rather than treating an empty Issue as global scope.
func conversationHistoryScope(c Call, id string) error {
	if c.Principal.Kind == protocol.PrincipalBackgroundAgent && id != store.ConversationID(c.Principal.ConversationID) {
		return protocol.Errorf(protocol.CodeForbidden, "history is outside this conversation")
	}
	return nil
}
