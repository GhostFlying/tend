import type { AttentionPage, NextActionPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";

/** Share the same owner-action count between desktop and phone navigation. */
export function inboxCount(summary?: AttentionPage, next?: NextActionPage) {
  const items = summary?.items ?? [];
  return (
    items.filter((item) => item.status === "open" && item.kind !== "information").length +
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
  return { count: inboxCount(summary, next.value ?? undefined), more: !!summary?.more };
}
