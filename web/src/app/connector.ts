// Keeps the link to tend up: checks the session on start, retries with a
// growing pause while tend cannot be reached, and checks again at once when
// the network comes back or the page is shown. Each time the link comes up
// it calls onOnline, which resends what the outbox holds. The event stream
// (F3) will take over noticing a lost link.
import type { ApiClient } from "../api/client.ts";

const firstPause = 1_000;
const maxPause = 30_000;

export function startConnector(client: ApiClient, onOnline: () => void): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pause = firstPause;
  let running = false;
  let stopped = false;

  const status = () => client.connection.get().status;

  const schedule = () => {
    if (stopped || running || timer !== undefined || status() === "not-owner") {
      return;
    }
    timer = setTimeout(() => {
      timer = undefined;
      void attempt();
    }, pause);
    pause = Math.min(pause * 2, maxPause);
  };

  const attempt = async () => {
    if (stopped || running) {
      return;
    }
    running = true;
    try {
      await client.connect();
    } catch {
      // The client recorded why; a refusal (not an owner) is not retried.
    } finally {
      running = false;
    }
    if (status() !== "online") {
      schedule();
    }
  };

  const wake = () => {
    if (status() !== "online" && document.visibilityState === "visible") {
      clearTimeout(timer);
      timer = undefined;
      pause = firstPause;
      void attempt();
    }
  };

  // Any request can change the link: one without an answer starts the
  // retries, and the first answer after that sends the outbox.
  let last = status();
  const unsubscribe = client.connection.subscribe(() => {
    const now = status();
    if (now === "offline") {
      schedule();
    } else if (now === "online" && last !== "online") {
      clearTimeout(timer);
      timer = undefined;
      pause = firstPause;
      onOnline();
    }
    last = now;
  });
  window.addEventListener("online", wake);
  document.addEventListener("visibilitychange", wake);
  void attempt();

  return () => {
    stopped = true;
    clearTimeout(timer);
    unsubscribe();
    window.removeEventListener("online", wake);
    document.removeEventListener("visibilitychange", wake);
  };
}
