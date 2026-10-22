// A short note above the composer about what a button did, such as an
// answer tend carried out or a write that went stale. It fades after a
// few seconds; a newer note replaces it.
import { Store, type Readable } from "../state/store.ts";

export interface Notice {
  text: string;
  tone: "info" | "warn";
  id: number;
}

const store = new Store<Notice | null>(null);
let next = 1;
let timer: ReturnType<typeof setTimeout> | undefined;

export const notice: Readable<Notice | null> = store;

export function notify(text: string, tone: Notice["tone"] = "info", ms = 6_000): void {
  clearTimeout(timer);
  store.set({ text, tone, id: next++ });
  timer = setTimeout(() => store.set(null), ms);
}

export function dismissNotice(): void {
  clearTimeout(timer);
  store.set(null);
}
