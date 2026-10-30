import type { IssueActivityPage, IssueActivityEntry } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** Retain already-read pages while replacing streamed entries by stable ID. */
export class IssueActivityRead {
  private id = "";
  private older = new Map<string, IssueActivityEntry>();
  private generation = 0;
  private fetching = false;
  private controller: AbortController | undefined;
  readonly read: WorkItemRead<IssueActivityPage>;
  readonly state: WorkItemRead<IssueActivityPage>["state"];
  constructor(private api: Reader) {
    this.read = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal) => {
        const generation = this.generation;
        let page = await this.api.get<IssueActivityPage>(path, signal);
        const head = page;
        const items = [...(page.items ?? [])];
        // Admission can reveal an older owner block. Revalidate the retained
        // range rather than stopping at the first overlapping head entry.
        const retainedFrom = this.older.size
          ? Math.min(...[...this.older.values()].map((item) => item.position))
          : 0;
        while (retainedFrom && page.more && (page.next_before ?? 0) > retainedFrom) {
          page = await this.api.get<IssueActivityPage>(
            `${path}?before=${page.next_before}`,
            signal,
          );
          items.push(...(page.items ?? []));
        }
        const fetched = new Set(items.map((item) => item.id));
        const oldest =
          page.more && items.length ? Math.min(...items.map((item) => item.position)) : 0;
        const removed = [...this.older.values()].filter(
          (item) =>
            !fetched.has(item.id) && item.position >= oldest && item.position <= head.event_seq,
        );
        if (signal?.aborted || generation !== this.generation) return head as T;
        for (const item of removed) this.older.delete(item.id);
        for (const item of items) {
          if (!item.block?.trigger.control?.withdrawn_at) this.older.set(item.id, item);
        }
        return {
          ...head,
          items: [...this.older.values()].sort((a, b) => a.position - b.position),
          more: page.more,
          next_before: page.next_before,
        } as T;
      },
    });
    this.state = this.read.state;
  }
  open(id: string, retained?: IssueActivityPage | null): Promise<void> {
    if (this.id !== id) {
      this.close();
      this.id = id;
    }
    const pending = this.read.open(`/api/v1/issues/${encodeURIComponent(id)}/activity`);
    if (retained && !this.state.get().value) {
      for (const item of retained.items ?? []) this.older.set(item.id, item);
      this.state.set({ ...this.state.get(), value: retained });
    }
    return pending;
  }
  async earlier(): Promise<void> {
    const current = this.state.get().value;
    if (!current?.more || this.fetching) return;
    const generation = this.generation;
    this.fetching = true;
    const controller = new AbortController();
    this.controller = controller;
    try {
      const page = await this.api.get<IssueActivityPage>(
        `/api/v1/issues/${encodeURIComponent(this.id)}/activity?before=${current.next_before}`,
        controller.signal,
      );
      if (generation !== this.generation) return;
      for (const item of page.items ?? []) this.older.set(item.id, item);
      this.state.set({
        ...this.state.get(),
        value: {
          ...(this.state.get().value ?? current),
          more: page.more,
          next_before: page.next_before ?? 0,
        },
      });
      await this.read.refresh();
    } finally {
      if (generation === this.generation) this.fetching = false;
    }
  }
  refresh(): Promise<void> {
    return this.read.refresh();
  }
  close(): void {
    this.generation++;
    this.controller?.abort();
    this.fetching = false;
    this.id = "";
    this.older.clear();
    this.read.close();
  }
}
