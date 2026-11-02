import type { AttentionPage, AttentionView, NextActionPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";

export function isHumanAction(item: AttentionView) {
  return (
    item.status === "open" &&
    item.kind !== "information" &&
    (!item.responsibility || item.responsibility.recipient_kind === "owner")
  );
}

/** Share the same owner-action count between desktop and phone navigation. */
export function inboxCount(summary?: AttentionPage, next?: NextActionPage) {
  const items = summary?.items ?? [];
  return (
    items.filter(isHumanAction).length +
    (next?.items ?? []).filter(
      (item) =>
        item.kind === "user_action" &&
        ["open", "blocked"].includes(item.status) &&
        (!item.attention_id ||
          !items.some((attention) => attention.attention_id === item.attention_id)),
    ).length
  );
}
export function useInboxCount(summary?: AttentionPage) {
  const { live } = useServices();
  const next = useStore(live.attention.next.state);
  const count = inboxCount(summary, next.value ?? undefined);
  const more = !!summary?.more;
  return { count, more, description: `${count}${more ? "+" : ""} 项待处理` };
}
