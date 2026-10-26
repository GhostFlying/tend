import { DraftStorage, type SavedDraft, type Withdrawal } from "./draftStore.ts";
import type { ConversationAttachment, ConversationItem } from "../api/generated/api.ts";
// Shell-scoped composer text survives navigation without sending an intention.
import { intentionText, type WorkItemIntention } from "../workitems/model.ts";
import type { WorkItemView } from "../api/generated/api.ts";
import type { TaskSummary } from "../api/generated/api.ts";
import { Store } from "../state/store.ts";

interface DraftState {
  text: string;
  files: File[];
  focus: number;
  attachments: ConversationAttachment[];
  replyTo?: string | undefined;
  source?: ConversationItem | undefined;
  saved: SavedDraft[];
  loaded: boolean;
  error?: string | undefined;
}

export class ComposerDraft {
  readonly state = new Store<DraftState>({
    text: "",
    files: [],
    attachments: [],
    focus: 0,
    saved: [],
    loaded: false,
  });
  id: string;
  private owner: string = crypto.randomUUID();
  private timer: ReturnType<typeof setInterval> | undefined;
  private reporting = false;
  private storage: Promise<DraftStorage> | undefined;
  private saving: Promise<void> = Promise.resolve();
  constructor() {
    let id: string;
    try {
      id = sessionStorage.getItem("tend.draft-id") || crypto.randomUUID();
      sessionStorage.setItem("tend.draft-id", id);
    } catch {
      id = crypto.randomUUID();
    }
    this.id = id;
  }
  async open(): Promise<void> {
    if (this.storage) return;
    try {
      const navigation = performance.getEntriesByType("navigation")[0] as
        PerformanceNavigationTiming | undefined;
      if (navigation?.type === "reload")
        this.owner = sessionStorage.getItem("tend.draft-owner") || this.owner;
      sessionStorage.setItem("tend.draft-owner", this.owner);
    } catch {
      /* Storage denial leaves a page-scoped owner. */
    }
    this.storage = DraftStorage.open();
    try {
      const storage = await this.storage;
      if (!(await storage.claim(this.id, this.owner))) {
        this.id = crypto.randomUUID();
        try {
          sessionStorage.setItem("tend.draft-id", this.id);
        } catch {
          /* Isolated draft still works for this page. */
        }
        await storage.claim(this.id, this.owner);
      }
      const renew = async () => {
        if (!(await storage.claim(this.id, this.owner)))
          throw new Error("草稿正在另一标签页编辑；本页内容已保留");
      };
      const startTimer = () => {
        clearInterval(this.timer);
        this.timer = setInterval(() => {
          void renew().catch((error) => this.report(String(error)));
        }, 20000);
      };
      startTimer();
      if (typeof window !== "undefined") {
        let releasing = Promise.resolve();
        window.addEventListener("pagehide", () => {
          clearInterval(this.timer);
          releasing = storage.release().catch(() => {});
        });
        window.addEventListener("pageshow", () => {
          void releasing
            .then(renew)
            .then(() => {
              startTimer();
              return this.flush();
            })
            .catch((error) => this.report(String(error)));
        });
      }
      const saved = await storage.drafts();
      const current = saved.find((d) => d.id === this.id);
      this.state.set({
        ...this.state.get(),
        ...current,
        saved: saved.filter(
          (d) => d.id !== this.id && (d.text || d.files.length || d.attachments.length),
        ),
        loaded: true,
      });
      this.state.subscribe(() => {
        if (this.reporting) return;
        const value = this.snapshot();
        this.saving = this.saving
          .then(() => storage.save("drafts", value))
          .catch((error) =>
            this.report(error instanceof Error ? error.message : "草稿未能保存，请保持页面打开"),
          );
      });
    } catch {
      this.state.set({
        ...this.state.get(),
        error: "草稿存储不可用，请保持页面打开",
        loaded: true,
      });
    }
  }
  private report(error: string): void {
    this.reporting = true;
    this.state.set({ ...this.state.get(), error });
    this.reporting = false;
  }
  snapshot(): SavedDraft {
    const s = this.state.get();
    return {
      id: this.id,
      text: s.text,
      files: s.files,
      attachments: s.attachments,
      replyTo: s.replyTo,
      source: s.source,
      updated: Date.now(),
    };
  }
  async durableStorage(): Promise<DraftStorage> {
    if (!this.storage) throw new Error("草稿存储未就绪");
    return this.storage;
  }
  async adoptWithdrawal(id: string): Promise<void> {
    await this.flush();
    await (await this.durableStorage()).adoptWithdrawal(id, this.id);
  }
  async flush(): Promise<void> {
    await this.saving;
    if (!this.storage) throw new Error("草稿存储未就绪");
    await (await this.storage).save("drafts", this.snapshot());
  }
  async recover(w: Withdrawal, draft: SavedDraft): Promise<void> {
    await this.flush();
    const storage = await this.storage!;
    await storage.restore(w, draft);
    const saved = await storage.drafts();
    const current = saved.find((d) => d.id === this.id);
    this.state.set({
      ...this.state.get(),
      ...current,
      focus: this.state.get().focus + 1,
      saved: saved.filter(
        (d) => d.id !== this.id && (d.text || d.files.length || d.attachments.length),
      ),
    });
  }
  async select(saved: SavedDraft): Promise<void> {
    const storage = await this.storage!;
    const old = this.snapshot();
    if (old.text || old.files.length || old.attachments.length)
      await storage.save("drafts", { ...old, id: `stash:${crypto.randomUUID()}` });
    this.state.set({ ...this.state.get(), ...saved, focus: this.state.get().focus + 1 });
    await this.flush();
    this.state.set({
      ...this.state.get(),
      saved: (await storage.drafts()).filter(
        (d) => d.id !== this.id && (d.text || d.files.length || d.attachments.length),
      ),
    });
  }
  clearChoice(): void {
    const s = this.state.get();
    if (!s.source) return;
    const source = { ...s.source };
    delete source.decision;
    this.state.set({ ...s, source });
  }
  removeAttachment(index: number): void {
    const s = this.state.get();
    this.state.set({ ...s, attachments: s.attachments.filter((_, i) => i !== index) });
  }
  write(text: string): void {
    this.state.set({ ...this.state.get(), text });
  }
  addFiles(files: File[]): void {
    const current = this.state.get();
    this.state.set({ ...current, files: [...current.files, ...files] });
  }
  removeFile(index: number): void {
    const current = this.state.get();
    this.state.set({ ...current, files: current.files.filter((_, i) => i !== index) });
  }
  clear(): void {
    this.state.set({
      ...this.state.get(),
      text: "",
      files: [],
      attachments: [],
      replyTo: undefined,
      source: undefined,
    });
  }
  followUp(task: TaskSummary): void {
    const current = this.state.get();
    const heading = `跟进 / 修改任务 ${task.task_id}`;
    const intention = `${heading}（${task.title}）。\n\n需要补充或修改的内容：\n`;
    this.state.set({
      ...current,
      text: current.text.includes(heading)
        ? current.text
        : current.text + (current.text.trim() ? "\n\n" : "") + intention,
      focus: current.focus + 1,
    });
  }
  workItem(item: WorkItemView, intention: WorkItemIntention): void {
    const current = this.state.get();
    const text = intentionText(item, intention);
    this.state.set({
      ...current,
      text: current.text + (current.text.trim() ? "\n\n" : "") + text,
      focus: current.focus + 1,
    });
  }
  restore(text: string, files: File[] = []): void {
    const current = this.state.get();
    this.state.set({
      ...current,
      text: text && current.text ? `${text}\n\n${current.text}` : text || current.text,
      files: [...files, ...current.files],
    });
  }
}
