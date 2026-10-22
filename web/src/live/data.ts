// The views the Web shows, kept current from the event stream (D69): the
// snapshot, the conversation and the open task's details. Conversation
// events carry their item, which replaces the one shown; any other event
// only says something changed, and the affected views are read again. The
// client never rebuilds ledger state from events itself.
import { ApiError } from "../api/client.ts";
import type {
  ConversationItem,
  ConversationPage,
  LedgerEvent,
  RunLogPage,
  RunLogView,
  Snapshot,
  TaskDetail,
  TaskSummary,
} from "../api/generated/api.ts";
import { Store, type Readable } from "../state/store.ts";

/** The part of the API client the views read with. */
export interface Reader {
  get<T>(path: string): Promise<T>;
}

export interface ConversationState {
  /** In conversation order, by seq. */
  items: readonly ConversationItem[];
  /** Whether earlier items exist than the first one held. */
  more: boolean;
  /** Whether the latest page has been read at least once. */
  loaded: boolean;
  loadingEarlier: boolean;
  error?: string;
  /**
   * When each owner message was seen to start processing, in ms. Read from
   * the event that said so; a message already processing when the page
   * was read counts from when it was sent.
   */
  processingSince: ReadonlyMap<string, number>;
}

export interface DetailState {
  taskId: string;
  /** null while the first read is under way or failed. */
  task: TaskDetail | null;
  error?: string;
  /** The latest run's most recent logs, oldest first. */
  logs: { runId: string; entries: readonly RunLogView[] } | null;
}

/** A task looked up for a reference outside the snapshot; null if tend has none. */
export type TaskLookup = ReadonlyMap<string, TaskSummary | null>;

const pageSize = 50;
/** How many log entries of the latest run the details keep. */
const logTail = 40;
/** How many pages of logs a details read goes through, at most. */
const maxLogPages = 20;

const noProcessing: ReadonlyMap<string, number> = new Map();

export class LiveData {
  private readonly api: Reader;
  private readonly snapshotStore = new Store<Snapshot | null>(null);
  private readonly conversationStore = new Store<ConversationState>({
    items: [],
    more: false,
    loaded: false,
    loadingEarlier: false,
    processingSince: noProcessing,
  });
  private readonly detailStore = new Store<DetailState | null>(null);
  private readonly lookupStore = new Store<TaskLookup>(new Map());
  private readonly lookingUp = new Set<string>();
  /** Conversation events that arrived while the latest page was being read. */
  private held: LedgerEvent[] | null = null;
  private readonly snapshotReads: Coalesced;
  private readonly detailReads: Coalesced;

  constructor(api: Reader) {
    this.api = api;
    this.snapshotReads = new Coalesced(() => this.readSnapshot());
    this.detailReads = new Coalesced(() => this.readDetail());
  }

  get snapshot(): Readable<Snapshot | null> {
    return this.snapshotStore;
  }

  get conversation(): Readable<ConversationState> {
    return this.conversationStore;
  }

  get detail(): Readable<DetailState | null> {
    return this.detailStore;
  }

  get lookups(): Readable<TaskLookup> {
    return this.lookupStore;
  }

  /** Reads every view again, as after a fresh start of the event stream. */
  async reload(): Promise<void> {
    this.lookupStore.set(new Map());
    await Promise.all([
      this.refreshSnapshot(),
      this.readLatest(),
      this.detailStore.get() ? this.refreshDetail() : Promise.resolve(),
    ]);
  }

  /** Takes in one ledger event. */
  apply(event: LedgerEvent): void {
    switch (event.object_type) {
      case "message":
        if (this.held) {
          this.held.push(event);
        } else {
          this.applyItem(event);
        }
        return;
      case "outbox":
        // Deliveries; what they deliver has events of its own.
        return;
      case "task":
        if (this.lookupStore.get().has(event.object_id)) {
          void this.lookUp(event.object_id, true);
        }
        break;
    }
    void this.refreshSnapshot();
    if (this.detailStore.get()) {
      void this.refreshDetail();
    }
  }

  refreshSnapshot(): Promise<void> {
    return this.snapshotReads.run();
  }

  refreshDetail(): Promise<void> {
    return this.detailReads.run();
  }

  /** Reads the page before the first item held. */
  async loadEarlier(): Promise<void> {
    const state = this.conversationStore.get();
    const first = state.items[0];
    if (!state.more || state.loadingEarlier || !first) {
      return;
    }
    this.conversationStore.set({ ...state, loadingEarlier: true });
    try {
      const page = await this.api.get<ConversationPage>(
        `/api/v1/conversation?before=${first.seq}&limit=${pageSize}`,
      );
      const now = this.conversationStore.get();
      this.conversationStore.set({
        ...now,
        items: merge(page.items ?? [], now.items),
        more: page.more,
        loadingEarlier: false,
      });
    } catch (err) {
      this.conversationStore.set({
        ...this.conversationStore.get(),
        loadingEarlier: false,
        error: describe(err),
      });
    }
  }

  /** Shows a task's details, reading them. */
  open(taskId: string): Promise<void> {
    const current = this.detailStore.get();
    if (current?.taskId !== taskId) {
      this.detailStore.set({ taskId, task: null, logs: null });
    }
    return this.refreshDetail();
  }

  close(): void {
    this.detailStore.set(null);
  }

  /**
   * Looks up a task a reply names that the snapshot does not list, such as
   * a finished one. Each is read once unless again is set.
   */
  async lookUp(taskId: string, again = false): Promise<void> {
    if (this.lookingUp.has(taskId) || (!again && this.lookupStore.get().has(taskId))) {
      return;
    }
    this.lookingUp.add(taskId);
    try {
      const t = await this.api.get<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(taskId)}`);
      this.remember(taskId, summary(t));
    } catch (err) {
      if (err instanceof ApiError && err.code === "not_found") {
        this.remember(taskId, null);
      }
      // Otherwise it stays unknown and is tried again when shown again.
    } finally {
      this.lookingUp.delete(taskId);
    }
  }

  private remember(taskId: string, task: TaskSummary | null): void {
    const next = new Map(this.lookupStore.get());
    next.set(taskId, task);
    this.lookupStore.set(next);
  }

  private async readSnapshot(): Promise<void> {
    try {
      this.snapshotStore.set(await this.api.get<Snapshot>("/api/v1/snapshot"));
    } catch {
      // The next event or reconnection reads it again.
    }
  }

  private async readLatest(): Promise<void> {
    this.held = [];
    try {
      const page = await this.api.get<ConversationPage>(`/api/v1/conversation?limit=${pageSize}`);
      const items = page.items ?? [];
      // Items before the page's first one are dropped: the page may have
      // moved past them while the stream was down, so they could be stale.
      this.conversationStore.set({
        items,
        more: page.more,
        loaded: true,
        loadingEarlier: false,
        processingSince: keepFor(this.conversationStore.get().processingSince, items),
      });
    } catch (err) {
      this.conversationStore.set({ ...this.conversationStore.get(), error: describe(err) });
    } finally {
      // Applied in order, the held events end at each item's newest state,
      // whichever of them the page already reflects.
      const held = this.held ?? [];
      this.held = null;
      for (const ev of held) {
        this.applyItem(ev);
      }
    }
  }

  private applyItem(event: LedgerEvent): void {
    const item = event.item;
    if (!item) {
      return;
    }
    const state = this.conversationStore.get();
    let processingSince = state.processingSince;
    if (item.state === "processing" && event.kind === "conversation.message_state") {
      const since = Date.parse(event.created_at);
      if (!Number.isNaN(since)) {
        const next = new Map(processingSince);
        next.set(item.message_id, since);
        processingSince = next;
      }
    }
    this.conversationStore.set({ ...state, items: merge(state.items, [item]), processingSince });
  }

  private async readDetail(): Promise<void> {
    const open = this.detailStore.get();
    if (!open) {
      return;
    }
    const { taskId } = open;
    let task: TaskDetail;
    try {
      task = await this.api.get<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(taskId)}`);
    } catch (err) {
      const now = this.detailStore.get();
      if (now?.taskId === taskId) {
        this.detailStore.set({ ...now, error: describe(err) });
      }
      return;
    }
    if (this.lookupStore.get().has(taskId)) {
      this.remember(taskId, summary(task));
    }
    const now = this.detailStore.get();
    if (now?.taskId !== taskId) {
      return;
    }
    const withTask: DetailState = { taskId, task, logs: now.logs };
    this.detailStore.set(withTask);
    const run = task.runs?.at(-1);
    if (!run) {
      this.detailStore.set({ ...withTask, logs: null });
      return;
    }
    const entries = await this.readLogTail(run.run_id);
    const latest = this.detailStore.get();
    if (latest?.taskId === taskId && entries) {
      this.detailStore.set({ ...latest, logs: { runId: run.run_id, entries } });
    }
  }

  /** Reads a run's logs to the end, keeping the tail; null if it failed. */
  private async readLogTail(runId: string): Promise<RunLogView[] | null> {
    const tail: RunLogView[] = [];
    let after = 0;
    try {
      for (let i = 0; i < maxLogPages; i++) {
        const page = await this.api.get<RunLogPage>(
          `/api/v1/runs/${encodeURIComponent(runId)}/logs?after=${after}`,
        );
        const logs = page.logs ?? [];
        tail.push(...logs);
        tail.splice(0, Math.max(0, tail.length - logTail));
        if (logs.length === 0 || page.next <= after) {
          break;
        }
        after = page.next;
      }
    } catch {
      return null;
    }
    return tail;
  }
}

/** Runs a read, and once more after it if asked again meanwhile. */
class Coalesced {
  private readonly fn: () => Promise<void>;
  private running: Promise<void> | null = null;
  private again = false;

  constructor(fn: () => Promise<void>) {
    this.fn = fn;
  }

  run(): Promise<void> {
    if (this.running) {
      this.again = true;
      return this.running;
    }
    this.running = (async () => {
      try {
        do {
          this.again = false;
          await this.fn();
        } while (this.again);
      } finally {
        this.running = null;
      }
    })();
    return this.running;
  }
}

/** Merges items into a conversation; the later version of an item wins. */
export function merge(
  items: readonly ConversationItem[],
  incoming: readonly ConversationItem[],
): ConversationItem[] {
  const byId = new Map<string, ConversationItem>();
  for (const it of items) {
    byId.set(it.message_id, it);
  }
  for (const it of incoming) {
    byId.set(it.message_id, it);
  }
  return [...byId.values()].sort((a, b) => a.seq - b.seq);
}

function keepFor(
  since: ReadonlyMap<string, number>,
  items: readonly ConversationItem[],
): ReadonlyMap<string, number> {
  const next = new Map<string, number>();
  for (const it of items) {
    const t = since.get(it.message_id);
    if (it.state === "processing" && t !== undefined) {
      next.set(it.message_id, t);
    }
  }
  return next.size === 0 ? noProcessing : next;
}

export function summary(t: TaskDetail): TaskSummary {
  return {
    task_id: t.task_id,
    title: t.title,
    project: t.project,
    lifecycle: t.lifecycle,
    control_mode: t.control_mode,
    control_revision: t.control_revision,
    spec_version: t.spec_version,
    rating: t.rating,
    block_reason: t.block_reason,
    workspace: t.workspace,
    next_implement_generation: t.next_implement_generation,
  };
}

function describe(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
