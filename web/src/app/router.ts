import { navigationOpener } from "../ui/focus.ts";
import { createElement, type AnchorHTMLAttributes, type MouseEvent } from "react";
import { Store, useStore } from "../state/store.ts";

declare global {
  interface Window {
    tendNavigate: (target: Route | string) => void;
  }
}

export const paths = {
  im: "/",
  chat: "/diagnostics/main",
  more: "/more",
  conversation: "/chat/:id",
  inbox: "/inbox",
  attention: "/inbox/:id",
  issues: "/issues",
  issue: "/issues/:id",
  tasks: "/tasks",
  task: "/tasks/:id",
  repos: "/repos",
  usage: "/usage",
  hosts: "/hosts",
  settings: "/settings",
  agents: "/settings/agents",
  archive: "/archive",
  upgrade: "/upgrades/:id",
} as const;
export type IssueFilters = {
  view?: "board" | "tree";
  project?: string;
  assignee?: string;
  closed?: boolean;
};
export type Route =
  | {
      page:
        | "im"
        | "chat"
        | "more"
        | "inbox"
        | "tasks"
        | "repos"
        | "usage"
        | "hosts"
        | "settings"
        | "agents"
        | "archive";
    }
  | { page: "conversation" | "attention" | "issue" | "upgrade"; id: string }
  | { page: "task"; id: string; from?: string }
  | ({ page: "issues" } & IssueFilters)
  | { page: "missing" };

export function parse(input: string | URL): Route {
  const url = new URL(input, "https://tend.invalid");
  const query = url.searchParams;
  for (const [page, path] of Object.entries(paths)) {
    const pattern = path.split(":id");
    if (pattern.length === 2) {
      const prefix = pattern[0]!;
      if (!url.pathname.startsWith(prefix)) continue;
      const encoded = url.pathname.slice(prefix.length);
      if (!encoded || encoded.includes("/")) continue;
      let id: string;
      try {
        id = decodeURIComponent(encoded);
      } catch {
        return { page: "missing" };
      }
      if (page === "task")
        return { page, id, ...(query.get("from") ? { from: query.get("from")! } : {}) };
      return { page: page as "conversation" | "attention" | "issue" | "upgrade", id };
    }
    if (url.pathname !== path) continue;
    if (page === "issues")
      return {
        page,
        ...(query.get("view") === "tree" || query.get("view") === "board"
          ? { view: query.get("view") as "tree" | "board" }
          : {}),
        ...(query.get("project") ? { project: query.get("project")! } : {}),
        ...(query.get("assignee") ? { assignee: query.get("assignee")! } : {}),
        ...(query.has("closed") ? { closed: query.get("closed") !== "false" } : {}),
      };
    return { page } as Route;
  }
  return { page: "missing" };
}

export function href(route: Route): string {
  if (route.page === "missing") return "/404";
  const path = paths[route.page];
  const query = new URLSearchParams();
  if (route.page === "task" && route.from) query.set("from", route.from);
  if (route.page === "issues") {
    for (const key of ["view", "project", "assignee", "closed"] as const) {
      if (route[key] !== undefined && route[key] !== "") query.set(key, String(route[key]));
    }
  }
  return (
    path.replace(":id", "id" in route ? encodeURIComponent(route.id) : "") +
    (query.size ? `?${query}` : "")
  );
}

export function legacyURL(input: string | URL): string | null {
  const url = new URL(input, "https://tend.invalid");
  const issue = url.searchParams.get("issue");
  const task = url.searchParams.get("task");
  if (task) return href({ page: "task", id: task, ...(issue ? { from: issue } : {}) });
  if (issue) return href({ page: "issue", id: issue });
  if (url.searchParams.has("work_item")) {
    url.searchParams.delete("work_item");
    return url.pathname + url.search + url.hash;
  }
  return null;
}

type Entry = { session: string; key: number; previous?: number; state?: Record<string, unknown> };
type SavedPage = {
  disclosures: string[];
  scroll: { path: string; top: number; left: number }[];
  focus: HTMLElement | null;
  focusPath?: string;
  focusText?: string;
};
const state = new Store<Route>({ page: "im" });
const saved = new Map<number, SavedPage>();
let session = "";
let sequence = 0;
let current: Entry;
let started = false;
let returning = false;
let preserving = false;
function entry(): Entry | undefined {
  return window.history.state?.tendRoute as Entry | undefined;
}
function publish() {
  state.set(parse(window.location.href));
}
export function startRouter() {
  if (started) return;
  started = true;
  window.tendNavigate = navigate;
  session = crypto.randomUUID();
  current = { session, key: ++sequence };
  window.history.replaceState(
    { ...window.history.state, tendRoute: current },
    "",
    legacyURL(window.location.href) ?? window.location.href,
  );
  window.history.scrollRestoration = "manual";
  publish();
  window.addEventListener("popstate", () => {
    savePage();
    preserving = false;
    const next = entry();
    returning = next?.session === session && saved.has(next.key);
    current = next?.session === session ? next : { session, key: ++sequence };
    window.history.replaceState(
      { ...window.history.state, tendRoute: current },
      "",
      legacyURL(window.location.href) ?? window.location.href,
    );
    publish();
  });
}
const entryValues = new Map<number, Map<string, Store<unknown>>>();
/** Small view preferences belong to the history entry, not to mounted pages. */
export function useEntryState<T>(name: string, initial: T) {
  startRouter();
  let values = entryValues.get(current.key);
  if (!values) {
    values = new Map();
    entryValues.set(current.key, values);
  }
  let store = values.get(name);
  if (!store) {
    store = new Store<unknown>(current.state?.[name] ?? initial);
    values.set(name, store);
  }
  const selected = store;
  return [useStore(selected) as T, (next: T) => selected.set(next)] as const;
}

export function useRoute(): Route {
  startRouter();
  return useStore(state);
}
export function navigate(
  route: Route | string,
  {
    replace = false,
    state: viewState,
  }: { replace?: boolean; state?: Record<string, unknown> } = {},
) {
  startRouter();
  const url =
    typeof route === "string"
      ? new URL(route, window.location.href)
      : new URL(href(route), window.location.href);
  if (url.origin !== window.location.origin) {
    window.location.assign(url.href);
    return;
  }
  const target = legacyURL(url) ?? url.pathname + url.search + url.hash;
  if (target === window.location.pathname + window.location.search + window.location.hash) return;
  preserving = replace && new URL(target, url).pathname === window.location.pathname;
  // A new filter starts a fresh hierarchy; leaving and returning keeps its expansion.
  if (preserving && parse(target).page === "issues") entryValues.delete(current.key);
  savePage();
  returning = false;
  current = replace ? current : { session, key: ++sequence, previous: current.key };
  if (viewState) current = { ...current, state: viewState };
  window.history[replace ? "replaceState" : "pushState"](
    { ...window.history.state, tendRoute: current },
    "",
    target,
  );
  publish();
}
export function back(fallback: Route | string) {
  startRouter();
  if (current.previous !== undefined && saved.has(current.previous)) window.history.back();
  else navigate(fallback, { replace: true });
}
function elementPath(element: Element): string {
  const parts: string[] = [];
  let node: Element | null = element;
  while (node && node !== document.body) {
    const parent: Element | null = node.parentElement;
    if (!parent) break;
    parts.unshift(
      `${node.tagName.toLowerCase()}:nth-child(${Array.from(parent.children).indexOf(node) + 1})`,
    );
    node = parent;
  }
  return `body > ${parts.join(" > ")}`;
}
function savePage() {
  if (!current) return;
  const focus = navigationOpener(
    document.activeElement instanceof HTMLElement ? document.activeElement : null,
  );
  saved.set(current.key, {
    disclosures: Array.from(document.querySelectorAll("details[open]"))
      .filter((el) => el.getClientRects().length)
      .map(elementPath),
    scroll: Array.from(document.querySelectorAll<HTMLElement>("body *"))
      .filter((el) => el.getClientRects().length && (el.scrollTop || el.scrollLeft))
      .map((el) => ({ path: elementPath(el), top: el.scrollTop, left: el.scrollLeft })),
    focus,
    ...(focus ? { focusPath: elementPath(focus), focusText: focus.textContent ?? "" } : {}),
  });
}
/** Restore after both the route commit and asynchronous page content arrive. */
export function restorePage(root: HTMLElement) {
  if (preserving) return () => {};
  const snapshot = returning ? saved.get(current.key) : undefined;
  let focused: HTMLElement | null = null;
  const pending = [...(snapshot?.scroll ?? [])];
  const disclosures = [...(snapshot?.disclosures ?? [])];
  const apply = () => {
    for (let index = disclosures.length - 1; index >= 0; index--) {
      const el = document.querySelector(disclosures[index]!);
      if (el instanceof HTMLDetailsElement) {
        el.open = true;
        disclosures.splice(index, 1);
      }
    }
    for (let index = pending.length - 1; index >= 0; index--) {
      const position = pending[index]!;
      const el = document.querySelector(position.path);
      if (el instanceof HTMLElement && el.getClientRects().length) {
        el.scrollTop = position.top;
        el.scrollLeft = position.left;
        if (
          Math.abs(el.scrollTop - position.top) < 1 &&
          Math.abs(el.scrollLeft - position.left) < 1
        )
          pending.splice(index, 1);
      }
    }
    const candidate = snapshot?.focus?.isConnected
      ? snapshot.focus
      : snapshot?.focusPath
        ? document.querySelector<HTMLElement>(snapshot.focusPath)
        : null;
    const opener =
      candidate &&
      candidate.textContent === snapshot?.focusText &&
      candidate.getClientRects().length
        ? candidate
        : null;
    const heading = Array.from(root.querySelectorAll<HTMLElement>("h1")).find(
      (el) => el.getClientRects().length,
    );
    const target = opener ?? (!focused?.isConnected ? heading : null);
    if (target && focused !== target) {
      if (target === heading) target.tabIndex = -1;
      target.focus({ preventScroll: true });
      focused = target;
    }
  };
  apply();
  const frame = requestAnimationFrame(apply);
  const observer = new MutationObserver(apply);
  observer.observe(root, { childList: true, subtree: true });
  const stop = () => observer.disconnect();
  root.addEventListener("pointerdown", stop, { once: true });
  root.addEventListener("keydown", stop, { once: true });
  root.addEventListener("wheel", stop, { once: true });
  root.addEventListener("touchmove", stop, { once: true });
  return () => {
    cancelAnimationFrame(frame);
    observer.disconnect();
    root.removeEventListener("pointerdown", stop);
    root.removeEventListener("keydown", stop);
    root.removeEventListener("wheel", stop);
    root.removeEventListener("touchmove", stop);
  };
}

export function Link({
  to,
  onClick,
  ...props
}: Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & { to: Route | string }) {
  const target = typeof to === "string" ? to : href(to);
  return createElement("a", {
    ...props,
    href: target,
    onClick(event: MouseEvent<HTMLAnchorElement>) {
      onClick?.(event);
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey ||
        (props.target && props.target !== "_self") ||
        props.download !== undefined
      )
        return;
      if (new URL(target, window.location.href).origin !== window.location.origin) return;
      event.preventDefault();
      navigate(target);
    },
  });
}
