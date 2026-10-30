import { ChoiceResponse } from "../workitems/choice.ts";
import type { OpSender } from "../app/ops.ts";
import { issueConversationReader } from "../conversation/issueMetadata.ts";
import { eventContent } from "../conversation/eventContent.ts";
import { AgentCatalog } from "../agents/data.ts";
import { Issues } from "../issues/data.ts";
import { AttentionData } from "../attention/data.ts";
import { DirectConversations } from "../conversation/api.ts";
import { ConversationQueue } from "../conversation/queueView.ts";
// The views the Web shows, kept current from the event stream (D69): the
// snapshot, the conversation and the open task's details. Conversation
// events carry their item, which replaces the one shown; any other event
// only says something changed, and the affected views are read again. The
// client never rebuilds ledger state from events itself.
import { RepositoryRegistry } from "../repos/registry.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { TaskLists } from "../tasks/lists.ts";
import { ConversationHandoff } from "../conversation/handoff.ts";
import { ConversationSettings } from "../conversation/settings.ts";
import { ApiError } from "../api/client.ts";
import type {
  WorkItemChoice,
  IssueConversationItem as ConversationItem,
  IssueConversationPage as ConversationPage,
  LedgerEvent,
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
}

/** A task looked up for a reference outside the snapshot; null if tend has none. */
export type TaskLookup = ReadonlyMap<string, TaskSummary | null>;

const pageSize = 50;
const noProcessing: ReadonlyMap<string, number> = new Map();

export class LiveData {
  readonly queueView: ConversationQueue;
  readonly settings: ConversationSettings;
  readonly handoff: ConversationHandoff;
  readonly tasks: TaskLists;
  readonly repos: RepositoryRegistry;
  readonly issues: Issues;
  readonly attention: AttentionData;
  readonly directChats: DirectConversations;
  readonly agents: AgentCatalog;
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
  private readonly choiceVersions = new Map<string, WorkItemChoice>();
  private readonly choiceReads = new Map<string, WorkItemRead<WorkItemChoice>>();
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

  constructor(
    api: Reader,
    readonly conversationID = "main",
  ) {
    api = issueConversationReader(api, conversationID);
    this.api = api;
    this.queueView = new ConversationQueue(api, (item) => this.currentChoices(item));
    this.settings = new ConversationSettings(api, conversationID);
    this.handoff = new ConversationHandoff(api);
    this.tasks = new TaskLists(api);
    this.repos = new RepositoryRegistry(api);
    this.issues = new Issues(api);
    this.attention = new AttentionData(api);
    this.directChats = new DirectConversations(api);
    this.agents = new AgentCatalog(api);
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

  private readonly reloadListeners = new Set<() => void>();
  subscribeReload(listener: () => void): () => void {
    this.reloadListeners.add(listener);
    return () => this.reloadListeners.delete(listener);
  }

  /** Resume a retained conversation reader when its surface becomes visible. */
  resume(): void {
    this.stopped = false;
    this.queueView.start();
    this.settings.start();
    this.handoff.start();
    void this.reload();
  }

  /** Reads every view again, as after a fresh start of the event stream. */
  async reload(): Promise<void> {
    for (const listener of this.reloadListeners) listener();
    void this.queueView.refresh().then(() => this.refreshTranscriptChoices());
    void this.repos.refresh();
    void this.settings.refresh();
    void this.handoff.refresh();
    this.tasks.refresh();
    this.issues.refresh();
    this.attention.refresh();
    this.directChats.refresh();
    this.agents.refresh();
    for (const read of this.choiceReads.values()) void read.refresh();
    this.detailController?.abort();
    this.lookupStore.set(new Map());
    await Promise.all([
      this.refreshSnapshot(),
      this.latestReads.run(),
      this.detailStore.get() ? this.refreshDetail() : Promise.resolve(),
    ]);
  }

  private refreshTranscriptChoices(): void {
    const ids = new Set(
      this.queueView.state
        .get()
        .blocks.flatMap((block) =>
          [block.trigger, ...(block.items ?? [])].flatMap(
            (item) => item.choices?.map((choice) => choice.choice_id) ?? [],
          ),
        ),
    );
    for (const id of ids) void this.choiceChanged(id);
  }

  /** One coalesced, cancellable read/backoff per choice, shared by both surfaces. */
  choiceRead(id: string): WorkItemRead<WorkItemChoice> {
    let read = this.choiceReads.get(id);
    if (!read) {
      read = new WorkItemRead(this.api, (choice) => {
        this.rememberChoice(choice);
        this.queueView.refreshChoices();
        const state = this.conversationStore.get();
        this.conversationStore.set({
          ...state,
          items: state.items.map((item) => this.currentChoices(item)),
        });
      });
      this.choiceReads.set(id, read);
    }
    return read;
  }

  private readonly choiceActions = new Map<string, ChoiceResponse>();
  choiceResponse(id: string, sender: OpSender): ChoiceResponse {
    let response = this.choiceActions.get(id);
    if (!response) {
      response = new ChoiceResponse(sender, () => void this.choiceChanged(id));
      this.choiceActions.set(id, response);
    }
    return response;
  }

  /** Refresh a card in place, including older loaded chat history. */
  async choiceChanged(id: string): Promise<void> {
    if (this.stopped) return;
    await this.choiceRead(id).open(`/api/v1/work-item-choices/${encodeURIComponent(id)}`);
  }

  private rememberChoice(choice: WorkItemChoice): WorkItemChoice {
    const current = this.choiceVersions.get(choice.choice_id);
    if (current && current.version > choice.version) return current;
    this.choiceVersions.set(choice.choice_id, choice);
    return choice;
  }

  /** Every read path shares card versions, even when its HTTP page is older. */
  private currentChoices(item: ConversationItem): ConversationItem {
    if (!item.choices?.length) return item;
    return { ...item, choices: item.choices.map((choice) => this.rememberChoice(choice)) };
  }

  private readonly eventListeners = new Set<(event: LedgerEvent) => void>();
  subscribeEvents(listener: (event: LedgerEvent) => void): () => void {
    this.eventListeners.add(listener);
    return () => this.eventListeners.delete(listener);
  }

  /** Takes in one ledger event. */
  apply(event: LedgerEvent): void {
    for (const listener of this.eventListeners) listener(event);
    if (["category", "host", "hosted_configuration"].includes(event.object_type))
      this.agents.refresh();
    // Global responsibility views must not inherit the selected chat filter.
    if (
      [
        "issue",
        "work_item",
        "task",
        "context_fact",
        "attention",
        "deadline",
        "planner_entry",
        "issue_settings",
        "followup",
        "decision",
        "work_item_choice",
      ].includes(event.object_type)
    ) {
      this.issues.refresh();
      this.attention.refresh();
    }
    // Execution and resource facts can change primary-task summaries without a
    // task event (for example lease expiry). Hidden reads stay lazy.
    if (["run", "host", "workspace"].includes(event.object_type)) this.issues.refresh();
    if (event.object_type === "message" && event.item?.author !== "main_agent") {
      this.issues.refresh();
      this.attention.refresh();
    }
    if (
      [
        "conversation",
        "message",
        "run",
        "run_message",
        "task",
        "issue",
        "work_item",
        "outbox",
      ].includes(event.object_type)
    )
      this.directChats.refresh();
    if (event.item && (event.item.conversation_id ?? "main") !== this.conversationID) return;
    if (event.kind === "conversation.associations_changed") {
      void this.latestReads.run();
      void this.queueView.refresh();
      return;
    }

    if (event.object_type === "work_item") {
      void this.latestReads.run();
      void this.queueView.refresh();
    }
    this.queueView.changed(event);
    if (event.object_type === "work_item_choice") {
      void this.choiceChanged(event.object_id);
      return;
    }
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
        (["issue", "work_item"].includes(event.object_type) &&
          task?.primary_issue_id === event.object_id) ||
        task === undefined ||
        (task !== null && task.lifecycle !== "completed" && task.lifecycle !== "cancelled")
      ) {
        void this.lookUp(id, true);
      }
    }
    this.tasks.changed();
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
        items: merge(page.items ?? [], now.items).map((item) => this.currentChoices(item)),
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
      this.detailStore.set({ taskId, task: null });
    }
    return this.refreshDetail();
  }

  close(): void {
    this.detailController?.abort();
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
      items = items.map((item) => this.currentChoices(item));
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
    const previous = this.conversationStore
      .get()
      .items.find((it) => it.message_id === event.item?.message_id);
    const item = event.item
      ? this.currentChoices({
          ...eventContent(event.item),
          ...(previous?.associations ? { associations: previous.associations } : {}),
          ...(previous?.background ? { background: previous.background } : {}),
        })
      : undefined;
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
    this.detailStore.set({ taskId, task });
  }

  /** Stops owned timers and pending view reads. */
  async stop(): Promise<void> {
    this.stopped = true;
    this.queueView.stop();
    this.dirtyLookups.clear();
    this.repos.stop();
    this.issues.stop();
    this.attention.stop();
    this.directChats.stop();
    this.agents.stop();
    for (const read of this.choiceReads.values()) read.close();
    this.detailController?.abort();
    clearTimeout(this.retryTimer);
    this.retryTimer = undefined;
    await Promise.all([this.settings.stop(), this.handoff.stop(), this.tasks.stop()]);
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
    byId.set(
      it.message_id,
      current?.choices?.length
        ? {
            ...it,
            choices: (it.choices ?? current.choices).map((choice) => {
              const old = current.choices?.find((entry) => entry.choice_id === choice.choice_id);
              return old && old.version > choice.version ? old : choice;
            }),
          }
        : it,
    );
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
    ...(t.primary_issue_id === undefined ? {} : { primary_issue_id: t.primary_issue_id }),
    ...(t.primary_issue_title === undefined ? {} : { primary_issue_title: t.primary_issue_title }),
    ...(t.ended_at === undefined ? {} : { ended_at: t.ended_at }),
    ...(t.completed_at === undefined ? {} : { completed_at: t.completed_at }),
  };
}

function describe(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
