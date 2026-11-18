import { useContext, useEffect, useSyncExternalStore } from "react";
import { ApiError, type ApiClient } from "../api/client.ts";
import type { IssueIdentity } from "../api/generated/api.ts";
import { ServicesContext } from "../app/services.tsx";
import { Store } from "../state/store.ts";
import { WorkItemRead, type ReadState } from "../workitems/live.ts";

export const shortIssueCode = (id: string) =>
  /^[Tt][1-9][0-9]*$/.test(id) ? id.toUpperCase() : "";
const internalID = /^wi_[0-7][0-9A-HJKMNP-TV-Z]{25}$/;

type Identity = IssueIdentity | "missing";
type Entry = { read: WorkItemRead<Identity>; users: number };
const clients = new WeakMap<ApiClient, Map<string, Entry>>();
const empty = new Store<ReadState<Identity>>({ value: null, loading: false });

function identityEntry(client: ApiClient, id: string): Entry {
  let entries = clients.get(client);
  if (!entries) {
    entries = new Map();
    clients.set(client, entries);
  }
  let entry = entries.get(id);
  if (!entry) {
    const read = new WorkItemRead<Identity>({
      async get<T>(path: string, signal?: AbortSignal): Promise<T> {
        try {
          const issue = await client.get<IssueIdentity>(path, signal);
          if (issue.short_code && !signal?.aborted) rememberIssueIdentity(client, issue);
          return (issue.short_code ? issue : "missing") as T;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return "missing" as T;
          throw error;
        }
      },
    });
    entry = { read, users: 0 };
    entries.set(id, entry);
  }
  return entry;
}

function rememberIssueIdentity(client: ApiClient, issue: IssueIdentity) {
  for (const id of [issue.issue_id, issue.short_code]) {
    identityEntry(client, id).read.state.set({ value: issue, loading: false });
  }
}

export function useIssueIdentity(input: string) {
  const client = useContext(ServicesContext)?.client;
  const code = shortIssueCode(input);
  const id = code || input;
  const entry = client && (code || internalID.test(id)) ? identityEntry(client, id) : null;
  const store = entry?.read.state ?? empty;
  const state = useSyncExternalStore(store.subscribe, store.get, store.get);
  useEffect(() => {
    if (!entry) return;
    const { read } = entry;
    const value = read.state.get().value;
    if (++entry.users === 1 && !value)
      void read.open(`/api/v1/issues/${encodeURIComponent(id)}?projection=identity`);
    return () => {
      if (--entry.users !== 0) return;
      const last = read.state.get().value;
      if (last && last !== "missing") read.pause();
      else read.close();
    };
  }, [entry, id]);
  return state;
}
