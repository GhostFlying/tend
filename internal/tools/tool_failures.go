package tools

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type toolFailuresArgs struct {
	BeforeSeq int64  `json:"before_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Tool      string `json:"tool,omitempty"`
}

type toolFailure struct {
	Seq             int64           `json:"seq"`
	MessageID       string          `json:"message_id"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	ThreadID        string          `json:"thread_id"`
	TurnID          string          `json:"turn_id"`
	ItemID          string          `json:"item_id"`
	Tool            string          `json:"tool"`
	UpdatedAt       int64           `json:"updated_at"`
	Error           *protocol.Error `json:"error,omitempty"`
}

// toolFailuresList reads the existing call journal. Inputs and full outputs
// are deliberately excluded, including nested results of read tools.
// Like conversation_read, this is owner-wide hosted conversation history:
// rejected calls may precede task creation and have no historical project.
// Current session scope cannot retroactively attribute those calls.
func (s *Service) toolFailuresList(ctx context.Context, c Call) (any, error) {
	if c.Principal.Kind != protocol.PrincipalMainAgent {
		return nil, protocol.Errorf(protocol.CodeForbidden, "tool failure history is only available to planning agents")
	}
	var args toolFailuresArgs
	if err := decode(c.Arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.BeforeSeq < 0 || args.Limit < 1 || args.Limit > 50 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "before_seq must be nonnegative and limit between 1 and 50")
	}
	result := struct {
		Scope         string        `json:"scope"`
		Items         []toolFailure `json:"items"`
		NextBeforeSeq int64         `json:"next_before_seq,omitempty"`
	}{Scope: "hosted_conversation", Items: []toolFailure{}}
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT m.seq,u.message_id,COALESCE(m.reply_to,''),u.thread_id,u.turn_id,u.item_id,u.tool,u.updated_at,u.output FROM conversation_tool_uses u JOIN conversation_messages m ON m.message_id=u.message_id WHERE u.status='failed' AND (?=0 OR m.seq<?) AND (?='' OR u.tool=?) ORDER BY m.seq DESC LIMIT ?`, args.BeforeSeq, args.BeforeSeq, args.Tool, args.Tool, args.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item toolFailure
			var output string
			if err := rows.Scan(&item.Seq, &item.MessageID, &item.SourceMessageID, &item.ThreadID, &item.TurnID, &item.ItemID, &item.Tool, &item.UpdatedAt, &output); err != nil {
				return err
			}
			if len(result.Items) == args.Limit {
				result.NextBeforeSeq = result.Items[len(result.Items)-1].Seq
				break
			}
			var decoded struct {
				Error *protocol.Error `json:"error"`
			}
			if json.Unmarshal([]byte(output), &decoded) == nil {
				item.Error = decoded.Error
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	return result, err
}
