import type { IssueActivityPage, IssueActivityEntry } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** Retain already-read pages while replacing streamed entries by stable ID. */
export class IssueActivityRead {
  private id = "";
  private older = new Map<string, IssueActivityEntry>();
  private generation = 0;
  private earlierRead: Promise<void> | null = null;
  private controller: AbortController | undefined;
  readonly read: WorkItemRead<IssueActivityPage>;
  readonly state: WorkItemRead<IssueActivityPage>["state"];
  constructor(private api: Reader) {
    this.read = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal) => {
        const generation = this.generation;
        if (signal?.aborted) throw signal.reason;
        const current = this.state.get().value;
        if (current?.readable && current.event_seq > 0) {
          return (await this.changes(path, current, signal)) as T;
        }
        let page = await this.api.get<IssueActivityPage>(path, signal);
        const head = page;
        if (page.readable) {
          if (!signal?.aborted && generation === this.generation) this.merge(page);
          return { ...page, items: this.items() } as T;
        }
        const items = [...(page.items ?? [])];
        // Admission can reveal an older owner block. Revalidate the retained
        // range rather than stopping at the first overlapping head entry.
        const retainedFrom = this.older.size
          ? Math.min(...[...this.older.values()].map((item) => item.position))
          : 0;
        while (retainedFrom && page.more && (page.next_before ?? 0) > retainedFrom) {
          page = await this.api.get<IssueActivityPage>(
            `${path}&before=${page.next_before}`,
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
          items: this.items(),
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
    const pending = this.read.open(`${this.path()}?view=readable`);
    if (retained && !this.state.get().value) {
      for (const item of retained.items ?? []) this.older.set(item.id, item);
      this.state.set({ ...this.state.get(), value: retained });
    }
    return pending;
  }
  earlier(): Promise<void> {
    const current = this.state.get().value;
    if (!current?.more || this.earlierRead) return Promise.resolve();
    const generation = this.generation;
    const controller = new AbortController();
    this.controller = controller;
    const pending = (async () => {
      const page = await this.api.get<IssueActivityPage>(
        `${this.path()}?view=readable&before=${current.next_before}`,
        controller.signal,
      );
      if (generation !== this.generation) return;
      this.merge(page);
      this.state.set({
        ...this.state.get(),
        value: {
          ...(this.state.get().value ?? current),
          items: this.items(),
          more: page.more,
          next_before: page.next_before ?? 0,
          event_seq: Math.min(current.event_seq, page.event_seq),
        },
      });
    })().finally(() => {
      if (this.earlierRead === pending) this.earlierRead = null;
    });
    this.earlierRead = pending;
    return pending.then(() => {
      if (generation === this.generation) return this.read.refresh();
    });
  }

  private path() {
    return `/api/v1/issues/${encodeURIComponent(this.id)}/activity`;
  }

  private items() {
    return [...this.older.values()].sort((a, b) => a.position - b.position);
  }

  private merge(page: IssueActivityPage) {
    for (const id of page.removed ?? []) this.older.delete(id);
    for (const item of page.items ?? []) this.older.set(item.id, item);
  }

  private async changes(path: string, current: IssueActivityPage, signal?: AbortSignal) {
    const from = current.more ? (current.next_before ?? 0) : 0;
    const query = `${path}&since=${current.event_seq}&from=${from}`;
    const head = await this.api.get<IssueActivityPage>(query, signal);
    const pages = [head];
    let page = head;
    while (page.more && page.next_before && !signal?.aborted) {
      page = await this.api.get<IssueActivityPage>(
        `${query}&through=${head.event_seq}&before=${page.next_before}`,
        signal,
      );
      pages.push(page);
    }
    if (signal?.aborted) return current;
    for (const changed of pages) this.merge(changed);
    // Reading history can advance the range while the delta is in flight.
    // earlier() then refreshes using its own page's coverage.
    const latest = this.state.get().value ?? current;
    return {
      ...latest,
      items: this.items(),
      event_seq:
        latest.next_before === current.next_before && latest.more === current.more
          ? head.event_seq
          : latest.event_seq,
    };
  }

  refresh(): Promise<void> {
    return this.read.refresh();
  }
  close(): void {
    this.generation++;
    this.controller?.abort();
    this.earlierRead = null;
    this.id = "";
    this.older.clear();
    this.read.close();
  }
}

/** Merge only adjacent background events from the same actor and Issue within two minutes. */
export function groupActivityEvents(rows: readonly IssueActivityEntry[]): IssueActivityEntry[][] {
  const groups: IssueActivityEntry[][] = [];
  for (const row of rows) {
    const group = groups.at(-1);
    const previous = group?.at(-1);
    const background = (entry: IssueActivityEntry) =>
      !entry.block &&
      !entry.artifact_id &&
      ["issue.created", "issue.updated", "issue.progress_updated", "issue.status_changed"].includes(
        entry.kind,
      ) &&
      !entry.progress;
    if (
      previous &&
      background(previous) &&
      background(row) &&
      previous.author === row.author &&
      previous.issue_id === row.issue_id &&
      row.created_at >= previous.created_at &&
      row.created_at - previous.created_at <= 120_000
    )
      group!.push(row);
    else groups.push([row]);
  }
  return groups;
}

/** Each retained identity belongs to one group, with its original member taking priority. */
export function identifyActivityGroups(
  groups: readonly IssueActivityEntry[][],
  identities: Map<string, string>,
) {
  const owners = new Map<string, number>();
  const retained = new Set(identities.values());
  groups.forEach((entries, index) => {
    for (const entry of entries) if (retained.has(entry.id)) owners.set(entry.id, index);
  });
  const claimed = new Set<string>();
  const result = groups.map((entries, index) => {
    const candidates = [
      ...new Set(
        entries.map((entry) => identities.get(entry.id)).filter((key): key is string => !!key),
      ),
    ];
    const key =
      candidates.find((key) => owners.get(key) === index && !claimed.has(key)) ??
      candidates.find((key) => !owners.has(key) && !claimed.has(key)) ??
      entries
        .map((entry) => entry.id)
        .find((key) => !claimed.has(key) && (!owners.has(key) || owners.get(key) === index))!;
    claimed.add(key);
    return { entries, key };
  });
  for (const { entries, key } of result) for (const entry of entries) identities.set(entry.id, key);
  return result;
}
