// A minimal observable value for React's useSyncExternalStore.
import { useSyncExternalStore } from "react";

export interface Readable<T> {
  get(): T;
  subscribe(listener: () => void): () => void;
}

export class Store<T> implements Readable<T> {
  private value: T;
  private readonly listeners = new Set<() => void>();

  constructor(initial: T) {
    this.value = initial;
  }

  get = (): T => this.value;

  set(value: T): void {
    if (Object.is(value, this.value)) {
      return;
    }
    this.value = value;
    for (const listener of [...this.listeners]) {
      listener();
    }
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
}

export function useStore<T>(store: Readable<T>): T {
  return useSyncExternalStore(store.subscribe, store.get);
}
