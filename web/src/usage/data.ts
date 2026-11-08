import type { TokenUsageView } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead, type ReadState } from "../workitems/live.ts";

type Entry = { read: WorkItemRead<TokenUsageView>; users: number; dynamic: boolean; path: string };

/** One cancellable read per query, driven by ledger changes and visible time windows. */
export class UsageData {
  private readonly entries = new Map<string, Entry>();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private visible = true;
  private listening = false;
  private stopped = false;
  constructor(private readonly api: Reader) {}

  private key(query: string): string {
    const params = new URLSearchParams(query);
    params.sort();
    return params.toString();
  }

  watch(query: string, listener: (state: ReadState<TokenUsageView>) => void): () => void {
    const key = this.key(query);
    let entry = this.entries.get(key);
    if (!entry) {
      const read: WorkItemRead<TokenUsageView> = new WorkItemRead(this.api, (value) => {
        const old = read.state.get().value;
        return old && old.revision > value.revision ? old : value;
      });
      const params = new URLSearchParams(key);
      const window = params.get("window") ?? "total";
      entry = {
        read,
        users: 0,
        path: `/api/v1/token-usage?${key}`,
        dynamic: window !== "total" && (!params.get("offset") || params.get("offset") === "0"),
      };
      this.entries.set(key, entry);
    }
    if (!this.listening && typeof document !== "undefined") {
      this.visible = document.visibilityState !== "hidden";
      document.addEventListener("visibilitychange", this.onVisibility);
      this.listening = true;
    }
    const selected = entry;
    const update = () => listener(selected.read.state.get());
    const unsubscribe = selected.read.state.subscribe(update);
    selected.users++;
    update();
    if (selected.users === 1 && this.visible && !this.stopped)
      void selected.read.open(selected.path);
    this.arm();
    return () => {
      unsubscribe();
      if (--selected.users === 0) {
        selected.read.close();
        this.entries.delete(key);
      }
      if (!this.entries.size && this.listening) {
        document.removeEventListener("visibilitychange", this.onVisibility);
        this.listening = false;
      }
      this.arm();
    };
  }

  private readonly onVisibility = () => this.setVisible(document.visibilityState !== "hidden");
  setVisible(visible: boolean): void {
    if (this.visible === visible) return;
    this.visible = visible;
    if (visible) this.refresh();
    else for (const entry of this.entries.values()) entry.read.pause();
    this.arm();
  }
  refresh(query?: string): void {
    if (!this.visible || this.stopped) return;
    const key = query === undefined ? undefined : this.key(query);
    for (const [id, entry] of this.entries)
      if (key === undefined || id === key) void entry.read.open(entry.path);
  }
  resume(): void {
    this.stopped = false;
    this.refresh();
    this.arm();
  }
  stop(): void {
    this.stopped = true;
    for (const entry of this.entries.values()) entry.read.pause();
    clearTimeout(this.timer);
    this.timer = undefined;
  }
  private arm(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (!this.visible || this.stopped || ![...this.entries.values()].some((e) => e.dynamic)) return;
    this.timer = setTimeout(() => {
      for (const entry of this.entries.values())
        if (entry.dynamic) void entry.read.open(entry.path);
      this.arm();
    }, 60_000);
  }
}
