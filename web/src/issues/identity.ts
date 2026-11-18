import { useContext, useEffect, useSyncExternalStore } from "react";
import type { ApiClient } from "../api/client.ts";
import type { IssueIdentity } from "../api/generated/api.ts";
import { ServicesContext } from "../app/services.tsx";
import { Store } from "../state/store.ts";

export const shortIssueCode = (id: string) =>
  /^[Tt][1-9][0-9]*$/.test(id) ? id.toUpperCase() : "";
const internalID = /^wi_[0-7][0-9A-HJKMNP-TV-Z]{25}$/;

type IdentityState = IssueIdentity | "missing" | null;
type Entry = Store<IdentityState>;
const clients = new WeakMap<ApiClient, Map<string, Entry>>();
const empty = new Store<IdentityState>(null);
const loading = new WeakSet<Entry>();

function identities(client: ApiClient) {
  let entries = clients.get(client);
  if (!entries) {
    entries = new Map();
    clients.set(client, entries);
  }
  return entries;
}

export function rememberIssueIdentity(client: ApiClient, issue: IssueIdentity) {
  const entries = identities(client);
  for (const id of [issue.issue_id, issue.short_code]) {
    identityEntry(entries, id).set(issue);
  }
}

export function useIssueIdentity(input: string): IdentityState {
  const client = useContext(ServicesContext)?.client;
  const id = shortIssueCode(input) || input;
  const entries = client && (internalID.test(id) || shortIssueCode(id)) ? identities(client) : null;
  const entry = entries ? identityEntry(entries, id) : empty;
  const state = useSyncExternalStore(entry.subscribe, entry.get, entry.get);
  // A single request serves all mounted references to the same identity.
  useEffect(() => {
    const value = entry.get();
    if (!client || !entries || (value && value !== "missing") || loading.has(entry)) return;
    loading.add(entry);
    void client
      .get<IssueIdentity>(`/api/v1/issues/${encodeURIComponent(id)}?projection=identity`)
      .then((issue) => {
        if (issue.short_code) rememberIssueIdentity(client, issue);
        else entry.set("missing");
      })
      .catch(() => entry.set("missing"))
      .finally(() => loading.delete(entry));
  }, [client, entries, entry, id]);
  return state;
}

function identityEntry(entries: Map<string, Entry>, id: string): Entry {
  let entry = entries.get(id);
  if (!entry) {
    entry = new Store<IdentityState>(null);
    entries.set(id, entry);
  }
  return entry;
}
