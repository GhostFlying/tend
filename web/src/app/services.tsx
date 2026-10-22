// What the views reach for: the API client, the live views, the send
// queue, and opening a task's details.
import { createContext, useContext } from "react";
import type { ApiClient } from "../api/client.ts";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";

export interface Services {
  client: ApiClient;
  live: LiveData;
  /** null while the outbox opens, or where the browser has none. */
  queue: SendQueue | null;
  openTask(taskId: string): void;
}

export const ServicesContext = createContext<Services | null>(null);

export function useServices(): Services {
  const s = useContext(ServicesContext);
  if (!s) {
    throw new Error("useServices outside the shell");
  }
  return s;
}
