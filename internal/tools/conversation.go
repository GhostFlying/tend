package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type conversationArgs struct {
	ConversationID string `json:"conversation_id,omitempty"`
	BeforeSeq      int64  `json:"before_seq,omitempty"`
	AfterSeq       int64  `json:"after_seq,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
}

type conversationPage struct {
	protocol.ConversationPage
	NextBeforeSeq int64 `json:"next_before_seq,omitempty"`
	NextAfterSeq  int64 `json:"next_after_seq,omitempty"`
}

// conversationRead recovers owner goals and coordination history without giving
// the dispatcher filesystem access. Attachment originals remain separate inputs.
func (s *Service) conversationRead(ctx context.Context, c Call) (any, error) {
	var args conversationArgs
	if err := decode(c.Arguments, &args); err != nil {
		return nil, err
	}
	limit := 20
	if args.Limit != nil {
		limit = *args.Limit
	}
	if args.BeforeSeq < 0 || args.AfterSeq < 0 || (args.BeforeSeq > 0 && args.AfterSeq > 0) || limit < 1 || limit > 50 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "use at most one nonnegative before_seq or after_seq and limit between 1 and 50")
	}
	var result conversationPage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		result.ConversationPage, err = views.Conversation(ctx, tx.Q(), args.BeforeSeq, args.AfterSeq, limit, args.ConversationID)
		if err != nil {
			return err
		}
		for i := range result.Items {
			if err := store.HideUnadmittedForCoordinator(ctx, tx.Q(), &result.Items[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range result.Items {
		if tool := result.Items[i].Tool; tool != nil {
			// Nested conversation_read outputs can contain earlier tool results. Keep
			// navigation and status without recursively copying historical payloads.
			tool.Input = ""
			tool.Output = ""
		}
	}
	if result.More && len(result.Items) > 0 {
		if args.AfterSeq > 0 {
			result.NextAfterSeq = result.Items[len(result.Items)-1].Seq
		} else {
			result.NextBeforeSeq = result.Items[0].Seq
		}
	}
	return result, nil
}
