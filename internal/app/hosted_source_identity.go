package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

// mainExecutionSourceAdmitted checks native input identity, not mutable review
// membership. Repairs and empty-input continuations retain their original
// logical execution's actual delivery and consumed steers.
func mainExecutionSourceAdmitted(ctx context.Context, q store.Querier, execution, source string) (bool, error) {
	var admitted bool
	err := q.QueryRowContext(ctx, `WITH native_turns AS (
 SELECT t.request_id,t.message_id,t.thread_id,t.turn_id
 FROM logical_turn_executions current JOIN logical_turn_executions original USING(logical_turn_id)
 JOIN main_turns t ON t.request_id=original.request_id WHERE current.request_id=?
 UNION SELECT request_id,message_id,thread_id,turn_id FROM main_turns WHERE request_id=?
 ) SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_message_controls c ON c.message_id=m.message_id
 WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0 AND (
 EXISTS(SELECT 1 FROM native_turns t WHERE t.message_id=m.message_id
 OR EXISTS(SELECT 1 FROM conversation_return_links r WHERE r.message_id=t.message_id AND r.source_message_id=m.message_id))
 OR EXISTS(SELECT 1 FROM native_turns t JOIN main_steers s ON s.thread_id=t.thread_id AND s.turn_id=t.turn_id
 WHERE s.message_id=m.message_id AND s.native_item_id<>'')))`, execution, execution, source).Scan(&admitted)
	return admitted, err
}
