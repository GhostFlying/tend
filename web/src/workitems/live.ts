// Shared cancellable reads serve current owner projections.
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export interface ReadState<T> {
  value: T | null;
  loading: boolean;
  error?: string | undefined;
}

/** A cancellable, coalesced read with bounded failure backoff. */
export class WorkItemRead<T> {
  readonly state = new Store<ReadState<T>>({ value: null, loading: false });
  private path: string | null = null;
  private generation = 0;
  private controller: AbortController | null = null;
  private running: Promise<void> | null = null;
  private dirty = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private delay = 1_000;

  constructor(
    private api: Reader,
    private received: (value: T) => T | void = () => {},
  ) {}

  open(path: string): Promise<void> {
    if (this.path === path) return this.refresh();
    this.close();
    this.path = path;
    this.state.set({ value: null, loading: false });
    return this.refresh();
  }

  refresh(): Promise<void> {
    if (!this.path) return Promise.resolve();
    clearTimeout(this.timer);
    this.timer = undefined;
    if (this.running) {
      this.dirty = true;
      return this.running;
    }
    const generation = this.generation;
    const run = (async () => {
      do {
        // A dirty read retries immediately; it supersedes the previous backoff.
        clearTimeout(this.timer);
        this.timer = undefined;
        this.dirty = false;
        const controller = new AbortController();
        this.controller = controller;
        this.state.set({ ...this.state.get(), loading: true });
        try {
          const received = await this.api.get<T>(this.path!, controller.signal);
          if (controller.signal.aborted || generation !== this.generation) return;
          const transformed = this.received(received);
          const value = transformed === undefined ? received : transformed;
          this.state.set({ value, loading: false });
          clearTimeout(this.timer);
          this.timer = undefined;
          this.delay = 1_000;
        } catch (error) {
          if (controller.signal.aborted || generation !== this.generation) return;
          this.state.set({
            ...this.state.get(),
            loading: false,
            error: error instanceof Error ? error.message : String(error),
          });
          clearTimeout(this.timer);
          this.timer = setTimeout(() => void this.refresh(), this.delay);
          this.delay = Math.min(30_000, this.delay * 2);
        }
      } while (this.dirty && generation === this.generation && this.path);
    })().finally(() => {
      if (this.running === run) this.running = null;
    });
    this.running = run;
    return run;
  }

  close(): void {
    this.generation++;
    this.path = null;
    this.controller?.abort();
    this.running = null;
    this.dirty = false;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.delay = 1_000;
    this.state.set({ value: null, loading: false });
  }
}
