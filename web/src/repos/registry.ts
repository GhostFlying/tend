import type { RepoRegistry } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { Store } from "../state/store.ts";

export interface RegistryState {
  view: RepoRegistry | null;
  loading: boolean;
  stale: boolean;
  error: boolean;
}

/** Complete ledger registrations, never scoped to the selected project. */
export class RepositoryRegistry {
  readonly state = new Store<RegistryState>({
    view: null,
    loading: false,
    stale: true,
    error: false,
  });
  private generation = 0;
  private controller: AbortController | null = null;
  private connected = false;
  private stopped = false;

  constructor(private readonly api: Reader) {}

  setConnected(connected: boolean): void {
    if (this.connected === connected || this.stopped) return;
    this.connected = connected;
    if (connected) void this.refresh();
    else {
      this.generation++;
      this.controller?.abort();
      this.state.set({ ...this.state.get(), loading: false, stale: true });
    }
  }

  async refresh(): Promise<void> {
    if (!this.connected || this.stopped) return;
    const generation = ++this.generation;
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    this.state.set({ ...this.state.get(), loading: true, stale: true, error: false });
    try {
      const view = await this.api.get<RepoRegistry>("/api/v1/repos", controller.signal);
      if (generation !== this.generation) return;
      this.state.set({ view, loading: false, stale: false, error: false });
    } catch {
      if (generation !== this.generation) return;
      this.state.set({ ...this.state.get(), loading: false, stale: true, error: true });
    } finally {
      if (this.controller === controller) this.controller = null;
    }
  }

  stop(): void {
    this.stopped = true;
    this.generation++;
    this.controller?.abort();
  }
}
