// The owner's event stream (D69): GET /api/v1/events, read with fetch so
// it can carry the bearer token. The stream starts with a "start" event
// naming its cursor, then sends a "ledger" event for each ledger event
// and a comment as a heartbeat while it is quiet.
//
// The client remembers the last event_seq it received and, after a lost
// connection, resumes after it with a growing pause. A stream that sends
// nothing for longer than a few heartbeats counts as lost. A cursor tend
// refuses (a 4xx other than 401, such as one past a restored ledger)
// means everything the client holds may be wrong: it drops the cursor and
// connects again without one, and the fresh start reloads every view.
import { ApiError } from "../api/client.ts";
import type { LedgerEvent, StreamStart } from "../api/generated/api.ts";
import { Store, type Readable } from "../state/store.ts";
import { SseParser } from "./sse.ts";

/**
 * connecting: opening the stream, the first time or again.
 * live: the stream started and is delivering.
 * waiting: lost; it connects again after a pause.
 * stopped: stop() was called.
 */
export type StreamState = "connecting" | "live" | "waiting" | "stopped";

export interface StreamOptions {
  /** Opens the stream after the cursor, or at the newest event without one. */
  open(after: number | undefined, signal: AbortSignal): Promise<Response>;
  /**
   * The stream started at eventSeq. fresh is true when it connected without
   * a cursor, so the views must be read again; tend promises that reading
   * them after the start misses nothing.
   */
  onStart(eventSeq: number, fresh: boolean): void;
  /** A ledger event after the cursor, in order, each once. */
  onEvent(event: LedgerEvent): void;
  /** Anything arrived, a heartbeat included. */
  onAlive?(): void;
  /** The connection was lost; it reconnects by itself. */
  onLost?(reason: string): void;
  /** How long a silent stream counts as alive. Default 45 seconds, three heartbeats. */
  silenceMs?: number;
  /** The pause before the given reconnection attempt, from 0. */
  backoffMs?(attempt: number): number;
}

/** How one connection ended. */
type Ending =
  { kind: "lost"; reason: string; started: boolean } | { kind: "reset" } | { kind: "stopped" };

const defaultBackoff = (attempt: number): number => Math.min(1_000 * 2 ** attempt, 30_000);

export class EventStream {
  private readonly options: StreamOptions;
  private readonly silenceMs: number;
  private readonly backoffMs: (attempt: number) => number;
  private readonly status = new Store<StreamState>("connecting");
  private cursorSeq: number | undefined;
  private running = false;
  private connection: AbortController | null = null;
  private pause: AbortController | null = null;
  private loop: Promise<void> | null = null;

  constructor(options: StreamOptions) {
    this.options = options;
    this.silenceMs = options.silenceMs ?? 45_000;
    this.backoffMs = options.backoffMs ?? defaultBackoff;
  }

  get state(): Readable<StreamState> {
    return this.status;
  }

  /** The last event_seq received, which a reconnection resumes after. */
  get cursor(): number | undefined {
    return this.cursorSeq;
  }

  start(): void {
    if (this.running) {
      return;
    }
    this.running = true;
    this.loop = this.run();
  }

  /** Closes the stream for good; resolves when it has. */
  async stop(): Promise<void> {
    this.running = false;
    this.connection?.abort();
    this.pause?.abort();
    await this.loop;
    this.status.set("stopped");
  }

  /** Ends a pause before reconnecting early, as when the network returns. */
  wake(): void {
    this.pause?.abort();
  }

  private async run(): Promise<void> {
    // Losses and refused cursors since the stream last started.
    let failures = 0;
    let resets = 0;
    while (this.running) {
      const ending = await this.connect();
      if (ending.kind === "stopped" || !this.running) {
        return;
      }
      let pause: number;
      if (ending.kind === "reset") {
        this.cursorSeq = undefined;
        // Straight back, unless tend keeps refusing.
        pause = resets === 0 ? 0 : this.backoffMs(resets - 1);
        resets++;
      } else {
        if (ending.started) {
          failures = 0;
          resets = 0;
        }
        this.call(() => this.options.onLost?.(ending.reason));
        pause = this.backoffMs(failures);
        failures++;
      }
      if (pause > 0) {
        this.status.set("waiting");
        await this.sleep(pause);
      }
    }
  }

  private async connect(): Promise<Ending> {
    const ctrl = new AbortController();
    this.connection = ctrl;
    this.status.set("connecting");
    let silent = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const arm = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        silent = true;
        ctrl.abort();
      }, this.silenceMs);
    };
    let started = false;
    arm();
    try {
      const res = await this.options.open(this.cursorSeq, ctrl.signal);
      if (!res.body) {
        return { kind: "lost", reason: "事件流没有内容", started };
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      const parser = new SseParser();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) {
          return { kind: "lost", reason: "tend 结束了事件流", started };
        }
        arm();
        this.call(() => this.options.onAlive?.());
        for (const m of parser.feed(decoder.decode(value, { stream: true }))) {
          if (m.event === "start") {
            const start = JSON.parse(m.data) as StreamStart;
            const fresh = this.cursorSeq === undefined;
            this.cursorSeq = start.event_seq;
            started = true;
            this.status.set("live");
            this.call(() => this.options.onStart(start.event_seq, fresh));
          } else if (m.event === "ledger") {
            const event = JSON.parse(m.data) as LedgerEvent;
            if (this.cursorSeq !== undefined && event.event_seq <= this.cursorSeq) {
              continue;
            }
            this.cursorSeq = event.event_seq;
            this.call(() => this.options.onEvent(event));
          }
        }
      }
    } catch (err) {
      if (!this.running) {
        return { kind: "stopped" };
      }
      if (err instanceof ApiError && refusesCursor(err.status)) {
        return { kind: "reset" };
      }
      if (silent) {
        return { kind: "lost", reason: "事件流太久没有消息", started };
      }
      if (err instanceof ApiError) {
        return { kind: "lost", reason: err.message, started };
      }
      return { kind: "lost", reason: "连不上 tend", started };
    } finally {
      clearTimeout(timer);
      ctrl.abort();
      this.connection = null;
    }
  }

  private sleep(ms: number): Promise<void> {
    const ctrl = new AbortController();
    this.pause = ctrl;
    return new Promise<void>((resolve) => {
      const timer = setTimeout(done, ms);
      ctrl.signal.addEventListener("abort", done);
      function done() {
        clearTimeout(timer);
        resolve();
      }
    }).finally(() => {
      if (this.pause === ctrl) {
        this.pause = null;
      }
    });
  }

  /** Runs a callback; a failing view must not take the stream down. */
  private call(fn: () => void): void {
    try {
      fn();
    } catch (err) {
      console.error("tend: an event handler failed", err);
    }
  }
}

/**
 * Whether tend refused the request itself, rather than the session or the
 * moment: 401 is a token to renew, 403 a device that is not the owner's,
 * 408 and 429 ask to come back later.
 */
function refusesCursor(status: number): boolean {
  return status >= 400 && status < 500 && ![401, 403, 408, 429].includes(status);
}
