// The views the Web shows, kept current from the event stream (D69): the
// snapshot, the conversation and the open task's details. Conversation
// events carry their item, which replaces the one shown; any other event
// only says something changed, and the affected views are read again. The
// client never rebuilds ledger state from events itself.
import { RepositoryRegistry } from "../repos/registry.ts";
import { WorkItems } from "../workitems/live.ts";
import { TaskLists } from "../tasks/lists.ts";
import { ConversationHandoff } from "../conversation/handoff.ts";
import { ConversationSettings } from "../conversation/settings.ts";
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
  get<T>(path: string, signal?: AbortSignal): Promise<T>;
}

export interface ConversationState {
  /** In conversation order, by seq. */
  items: readonly ConversationItem[];
  /** Whether earlier items exist than the first one held. */
  more: boolean;
  /** Whether the latest page has been read at least once. */
  loaded: boolean;
  loadingEarlier: boolean;
  /** Oldest sequence in the contiguous history, independent of live item updates. */
  before?: number | undefined;
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
  logs: { runId: string; entries: readonly RunLogView[]; more: boolean } | null;
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
  readonly settings: ConversationSettings;
  readonly handoff: ConversationHandoff;
  readonly tasks: TaskLists;
  readonly repos: RepositoryRegistry;
  readonly workItems: WorkItems;
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
  private readonly dirtyLookups = new Set<string>();
  private historyGeneration = 0;
  private logCache: { runId: string; entries: RunLogView[]; next: number; more: boolean } | null =
    null;
  /** Conversation events that arrived while the latest page was being read. */
  private held: LedgerEvent[] | null = null;
  private readonly snapshotReads: Coalesced;
  private readonly detailReads: Coalesced;
  private readonly latestReads: Coalesced;
  private readonly failed = new Set<"snapshot" | "conversation" | "detail">();
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private retryDelay = 1_000;
  private stopped = false;
  private detailController: AbortController | null = null;
  private logWatch: {
    taskId: string;
    runId: string;
    controller: AbortController;
    done: Promise<void>;
  } | null = null;

  constructor(api: Reader) {
    this.api = api;
    this.settings = new ConversationSettings(api);
    this.handoff = new ConversationHandoff(api);
    this.tasks = new TaskLists(api);
    this.repos = new RepositoryRegistry(api);
    this.workItems = new WorkItems(api);
    this.snapshotReads = new Coalesced(() => this.readSnapshot());
    this.detailReads = new Coalesced(() => this.readDetail());
    this.latestReads = new Coalesced(() => this.readLatest());
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
    void this.repos.refresh();
    void this.settings.refresh();
    void this.handoff.refresh();
    this.tasks.refresh();
    this.workItems.refresh();
    this.detailController?.abort();
    void this.stopLogs();
    this.logCache = null;
    this.lookupStore.set(new Map());
    await Promise.all([
      this.refreshSnapshot(),
      this.latestReads.run(),
      this.detailStore.get() ? this.refreshDetail() : Promise.resolve(),
    ]);
  }

  /** Takes in one ledger event. */
  apply(event: LedgerEvent): void {
    if (
      [
        "conversation.handoff_settings",
        "conversation.handoff_requested",
        "conversation.handoff_completed",
        "conversation.summary",
        "conversation.summary_started",
        "conversation.summary_failed",
      ].includes(event.kind)
    ) {
      void this.handoff.refresh();
      return;
    }
    if (event.kind === "conversation.settings") {
      void this.settings.refresh();
      return;
    }
    switch (event.object_type) {
      case "message":
        this.handoff.invalidate(event.event_seq);
        if (this.held) {
          this.held.push(event);
        } else {
          this.applyItem(event);
        }
        return;
      case "outbox":
        // Deliveries; what they deliver has events of its own.
        return;
    }
    // A host, workspace or another task can change a reference's queue reason.
    // Refresh cached active references from the server, including an initial read
    // still in flight; never derive a scheduling result from the event itself.
    const references = new Set([...this.lookupStore.get().keys(), ...this.lookingUp]);
    for (const id of references) {
      const task = this.lookupStore.get().get(id);
      if (
        (event.object_type === "task" && event.object_id === id) ||
        task === undefined ||
        (task !== null && task.lifecycle !== "completed" && task.lifecycle !== "cancelled")
      ) {
        void this.lookUp(id, true);
      }
    }
    this.tasks.changed();
    this.workItems.changed();
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
    const first = state.before ?? state.items[0]?.seq;
    if (!state.more || state.loadingEarlier || !first) {
      return;
    }
    const generation = this.historyGeneration;
    this.conversationStore.set({ ...state, loadingEarlier: true });
    try {
      const page = await this.api.get<ConversationPage>(
        `/api/v1/conversation?before=${first}&limit=${pageSize}`,
      );
      if (generation !== this.historyGeneration) return;
      const now = this.conversationStore.get();
      this.conversationStore.set({
        ...now,
        items: merge(page.items ?? [], now.items),
        more: page.more,
        before: page.items?.[0]?.seq ?? now.before,
        loadingEarlier: false,
      });
    } catch (err) {
      if (generation !== this.historyGeneration) return;
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
      this.detailController?.abort();
      void this.stopLogs();
      this.detailStore.set({ taskId, task: null, logs: null });
    }
    return this.refreshDetail();
  }

  close(): void {
    this.detailController?.abort();
    void this.stopLogs();
    this.failed.delete("detail");
    this.detailStore.set(null);
  }

  /**
   * Looks up a task a reply names that the snapshot does not list, such as
   * a finished one. Each is read once unless again is set.
   */
  async lookUp(taskId: string, again = false): Promise<void> {
    if (this.stopped) return;
    if (this.lookingUp.has(taskId)) {
      if (again) this.dirtyLookups.add(taskId);
      return;
    }
    if (!again && this.lookupStore.get().has(taskId)) return;
    this.lookingUp.add(taskId);
    try {
      const t = await this.api.get<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(taskId)}`);
      if (!this.stopped) this.remember(taskId, summary(t));
    } catch (err) {
      if (err instanceof ApiError && err.code === "not_found") {
        this.remember(taskId, null);
      }
      // Otherwise it stays unknown and is tried again when shown again.
    } finally {
      this.lookingUp.delete(taskId);
      if (this.dirtyLookups.delete(taskId) && !this.stopped) void this.lookUp(taskId, true);
    }
  }

  private remember(taskId: string, task: TaskSummary | null): void {
    const next = new Map(this.lookupStore.get());
    next.set(taskId, task);
    this.lookupStore.set(next);
  }

  private async readSnapshot(): Promise<void> {
    if (this.stopped) return;
    try {
      this.snapshotStore.set(await this.api.get<Snapshot>("/api/v1/snapshot"));
      this.recovered("snapshot");
    } catch {
      this.retry("snapshot");
    }
  }

  private async readLatest(): Promise<void> {
    if (this.stopped) return;
    this.historyGeneration++;
    this.held = [];
    try {
      const page = await this.api.get<ConversationPage>(`/api/v1/conversation?limit=${pageSize}`);
      let items = page.items ?? [];
      let more = page.more;
      // A long active turn or a queue of owner messages can push the owner
      // whose turn is running out of the latest page. Find the closest
      // non-queued owner before rendering the stop control.
      while (
        more &&
        items.length &&
        !items.some((it) => it.author === "owner" && it.state !== "queued")
      ) {
        const earlier = await this.api.get<ConversationPage>(
          `/api/v1/conversation?before=${items[0]!.seq}&limit=${pageSize}`,
        );
        const previous = earlier.items ?? [];
        if (!previous.length) {
          more = false;
          break;
        }
        items = merge(previous, items);
        more = earlier.more;
      }
      this.recovered("conversation");
      // Items before the page's first one are dropped: the page may have
      // moved past them while the stream was down, so they could be stale.
      this.conversationStore.set({
        items,
        more,
        before: items[0]?.seq,
        loaded: true,
        loadingEarlier: false,
        processingSince: keepFor(this.conversationStore.get().processingSince, items),
      });
    } catch (err) {
      this.retry("conversation");
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
    if (this.stopped) return;
    const open = this.detailStore.get();
    if (!open) {
      this.recovered("detail");
      return;
    }
    const { taskId } = open;
    const controller = new AbortController();
    this.detailController = controller;
    let task: TaskDetail;
    try {
      task = await this.api.get<TaskDetail>(
        `/api/v1/tasks/${encodeURIComponent(taskId)}`,
        controller.signal,
      );
    } catch (err) {
      if (controller.signal.aborted) return;
      const now = this.detailStore.get();
      if (now?.taskId === taskId) {
        this.retry("detail");
        this.detailStore.set({ ...now, error: describe(err) });
      }
      return;
    }
    this.recovered("detail");
    if (this.lookupStore.get().has(taskId)) {
      this.remember(taskId, summary(task));
    }
    const now = this.detailStore.get();
    if (now?.taskId !== taskId) {
      return;
    }
    const run = task.runs?.at(-1);
    if (this.logWatch && this.logWatch.runId !== run?.run_id) void this.stopLogs();
    const withTask: DetailState = {
      taskId,
      task,
      logs: now.logs?.runId === run?.run_id ? now.logs : null,
    };
    this.detailStore.set(withTask);
    if (!run) {
      this.detailStore.set({ ...withTask, logs: null });
      return;
    }
    if (this.logWatch?.runId === run.run_id) return;
    const logs = await this.readLogTail(run.run_id, controller.signal);
    if (controller.signal.aborted) return;
    if (!logs) this.retry("detail");
    const latest = this.detailStore.get();
    if (latest?.taskId === taskId && logs) {
      this.detailStore.set({ ...latest, logs: { runId: run.run_id, ...logs } });
    }
    if (
      this.detailStore.get()?.taskId === taskId &&
      run.observed &&
      run.observed !== "finished" &&
      run.observed !== "failed"
    ) {
      this.watchLogs(taskId, run.run_id);
    }
  }

  /** Stops all owned timers and the pending log request. */
  async stop(): Promise<void> {
    this.stopped = true;
    this.dirtyLookups.clear();
    this.repos.stop();
    this.workItems.stop();
    this.detailController?.abort();
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    await Promise.all([
      this.stopLogs(),
      this.settings.stop(),
      this.handoff.stop(),
      this.tasks.stop(),
    ]);
  }

  private recovered(view: "snapshot" | "conversation" | "detail"): void {
    this.failed.delete(view);
    if (!this.failed.size) {
      clearTimeout(this.retryTimer);
      this.retryTimer = undefined;
      this.retryDelay = 1_000;
    }
  }

  private retry(view: "snapshot" | "conversation" | "detail"): void {
    this.failed.add(view);
    if (this.stopped || this.retryTimer !== undefined) return;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = undefined;
      if (this.failed.has("snapshot")) void this.refreshSnapshot();
      if (this.failed.has("conversation")) void this.latestReads.run();
      if (this.failed.has("detail")) void this.refreshDetail();
    }, this.retryDelay);
    this.retryDelay = Math.min(this.retryDelay * 2, 30_000);
  }

  private stopLogs(): Promise<void> {
    const watch = this.logWatch;
    this.logWatch = null;
    watch?.controller.abort();
    return watch?.done ?? Promise.resolve();
  }

  private watchLogs(taskId: string, runId: string): void {
    if (this.stopped) return;
    const controller = new AbortController();
    const watch = { taskId, runId, controller, done: Promise.resolve() };
    this.logWatch = watch;
    watch.done = (async () => {
      let delay = 1_000;
      while (!controller.signal.aborted) {
        const after = this.logCache?.runId === runId ? this.logCache.next : 0;
        try {
          const page = await this.api.get<RunLogPage>(
            `/api/v1/runs/${encodeURIComponent(runId)}/logs?after=${after}&wait=${this.logCache?.more ? 0 : 15}`,
            controller.signal,
          );
          if (controller.signal.aborted || this.logWatch !== watch) return;
          const entries = [
            ...(this.logCache?.runId === runId ? this.logCache.entries : []),
            ...(page.logs ?? []),
          ].slice(-logTail);
          const more = (page.logs?.length ?? 0) > 0;
          this.logCache = { runId, entries, next: page.next, more };
          const current = this.detailStore.get();
          if (current?.taskId === taskId)
            this.detailStore.set({ ...current, logs: { runId, entries, more } });
          if (!more && (page.observed === "finished" || page.observed === "failed")) return;
          delay = 1_000;
          // A server may answer an empty poll immediately when run state changes.
          if (!page.logs?.length) await abortablePause(250, controller.signal);
        } catch {
          if (controller.signal.aborted) return;
          await abortablePause(delay, controller.signal);
          delay = Math.min(delay * 2, 30_000);
        }
      }
    })().finally(() => {
      if (this.logWatch === watch) this.logWatch = null;
    });
  }

  /** Reads a run's logs to the end, keeping the tail; null if it failed. */
  private async readLogTail(
    runId: string,
    signal?: AbortSignal,
  ): Promise<{ entries: RunLogView[]; more: boolean } | null> {
    const previous = this.logCache?.runId === runId ? this.logCache : null;
    const tail: RunLogView[] = [...(previous?.entries ?? [])];
    let after = previous?.next ?? 0;
    let more = true;
    try {
      for (let i = 0; i < maxLogPages; i++) {
        const page = await this.api.get<RunLogPage>(
          `/api/v1/runs/${encodeURIComponent(runId)}/logs?after=${after}`,
          signal,
        );
        const logs = page.logs ?? [];
        tail.push(...logs);
        tail.splice(0, Math.max(0, tail.length - logTail));
        if (logs.length === 0 || page.next <= after) {
          more = false;
          break;
        }
        after = page.next;
      }
    } catch {
      return null;
    }
    this.logCache = { runId, entries: tail, next: after, more };
    return { entries: tail, more };
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

/** Merges items in conversation order, preserving the newest assistant revision. */
export function merge(
  items: readonly ConversationItem[],
  incoming: readonly ConversationItem[],
): ConversationItem[] {
  const byId = new Map<string, ConversationItem>();
  for (const it of items) {
    byId.set(it.message_id, it);
  }
  for (const it of incoming) {
    const current = byId.get(it.message_id);
    // Reads and delayed stream events may arrive in either order. A final
    // body can be shorter than a draft, so only its revision orders them.
    if ((current?.assistant_revision ?? 0) > (it.assistant_revision ?? 0)) continue;
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
    ...(t.execution === undefined ? {} : { execution: t.execution }),
    ...(t.primary_work_item_id === undefined
      ? {}
      : { primary_work_item_id: t.primary_work_item_id }),
    ...(t.primary_work_item_title === undefined
      ? {}
      : { primary_work_item_title: t.primary_work_item_title }),
    ...(t.completed_at === undefined ? {} : { completed_at: t.completed_at }),
  };
}

function describe(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function abortablePause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    const timer = setTimeout(finish, ms);
    signal.addEventListener("abort", finish, { once: true });
  });
}
