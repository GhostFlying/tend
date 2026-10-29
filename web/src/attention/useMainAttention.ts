import { useEffect, useMemo } from "react";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ApiClient } from "../api/client.ts";
import type { LiveData } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { useStore } from "../state/store.ts";

/** A main-summary read stays independent of the Inbox's current filter/page. */
export function useMainAttention(client: ApiClient, live: LiveData) {
  const read = useMemo(() => new WorkItemRead<AttentionPage>(client), [client]);
  useEffect(() => {
    void read.open("/api/v1/inbox?status=open");
    // The existing Next read already receives Attention/planner/deadline SSE
    // invalidation and reload. Reuse its completion signal, not another stream.
    const stop = live.attention.next.state.subscribe(() => {
      if (!live.attention.next.state.get().loading) void read.refresh();
    });
    void live.attention.openNext();
    return () => {
      stop();
      read.close();
    };
  }, [read, live]);
  return useStore(read.state);
}
