// Keeps the link to tend up through the event stream (D69): the stream
// reconnects by itself, resuming after the last event it received, and a
// fresh start reads every view again. Each time the link comes up the
// outbox is sent; the network coming back or the page being shown again
// cuts a reconnection pause short.
import type { ApiClient, Connection } from "../api/client.ts";
import type { LiveData } from "../live/data.ts";
import { EventStream } from "../live/eventStream.ts";
import { Store, type Readable } from "../state/store.ts";

export interface Link {
  /** What the page shows: connected only while the event stream is live. */
  connection: Readable<Connection>;
  stop(): Promise<void>;
}

export function startConnector(client: ApiClient, live: LiveData, onOnline: () => void): Link {
  const stream = new EventStream({
    open: (after, signal) =>
      client.openStream(
        after === undefined ? "/api/v1/events" : `/api/v1/events?after=${after}`,
        signal,
      ),
    onStart: () => {
      void live.reload();
    },
    onEvent: (event) => live.apply(event),
    onAlive: () => client.noteAlive(),
    onLost: (reason) => client.markOffline(reason),
  });

  const shown = new Store<Connection>({ status: "connecting" });
  let everLive = false;
  const update = () => {
    const c = client.connection.get();
    const s = stream.state.get();
    everLive ||= s === "live";
    if (c.status === "not-owner" || (s === "live" && c.status === "online")) {
      shown.set(same(shown.get(), c) ? shown.get() : c);
    } else if (!everLive) {
      shown.set(shown.get().status === "connecting" ? shown.get() : { status: "connecting" });
    } else {
      const reason = c.status === "offline" ? c.reason : "事件流断开";
      const now = shown.get();
      if (now.status !== "offline" || now.reason !== reason) {
        shown.set({ status: "offline", reason });
      }
    }
  };

  // Any answer can bring the link up, and the first one after it was down
  // sends the outbox.
  let last = client.connection.get().status;
  const unsubscribeClient = client.connection.subscribe(() => {
    const now = client.connection.get().status;
    if (now === "online" && last !== "online") {
      onOnline();
      stream.wake();
    }
    last = now;
    update();
  });
  const unsubscribeStream = stream.state.subscribe(update);

  const wake = () => {
    if (document.visibilityState === "visible") {
      stream.wake();
    }
  };
  window.addEventListener("online", wake);
  document.addEventListener("visibilitychange", wake);

  // The session check names the owner; the stream signs in if needed.
  void client.connect().catch(() => {});
  stream.start();

  return {
    connection: shown,
    async stop() {
      unsubscribeClient();
      unsubscribeStream();
      window.removeEventListener("online", wake);
      document.removeEventListener("visibilitychange", wake);
      await Promise.all([stream.stop(), live.stop()]);
    },
  };
}

function same(a: Connection, b: Connection): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
