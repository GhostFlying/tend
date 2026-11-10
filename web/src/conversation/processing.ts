import type { IssueConversationItem, MessageProcessingState } from "../api/generated/api.ts";

/** Show one current processing state for each logical turn's visible replies. */
export function groupedProcessing(items: readonly IssueConversationItem[]) {
  const turns = new Map<string, { last: string; state: MessageProcessingState }>();
  for (const item of items) {
    const state = item.associations?.processing;
    if (item.tool || !state) continue;
    const previous = turns.get(state.logical_turn_id);
    turns.set(state.logical_turn_id, {
      last: item.message_id,
      state: previous && previous.state.revision > state.revision ? previous.state : state,
    });
  }
  return new Map([...turns.values()].map(({ last, state }) => [last, state]));
}
