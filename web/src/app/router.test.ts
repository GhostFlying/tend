import type { MouseEvent } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { href, legacyURL, Link, parse, paths, type Route } from "./router.ts";
import { navigationOpener, rememberOpener } from "../ui/focus.ts";

afterEach(() => vi.unstubAllGlobals());

test("a dialog autofocus target cannot become its own navigation opener", () => {
  class Element {
    dialog: Dialog | null = null;
    closest() {
      return this.dialog;
    }
  }
  class Dialog extends Element {
    contains(element: Element) {
      return element.dialog === this;
    }
  }
  vi.stubGlobal("HTMLElement", Element);
  const dialog = new Dialog();
  const input = new Element();
  input.dialog = dialog;
  const opener = new Element();
  rememberOpener(dialog as unknown as HTMLDialogElement, input as unknown as HTMLElement);
  expect(navigationOpener(input as unknown as HTMLElement)).toBe(input);
  rememberOpener(dialog as unknown as HTMLDialogElement, opener as unknown as HTMLElement);
  expect(navigationOpener(input as unknown as HTMLElement)).toBe(opener);
});

test("every planned route round trips, including encoded identities and filters", () => {
  for (const page of Object.keys(paths)) {
    const route = { page, id: "identity / 中文?#", from: "parent / issue" } as Route;
    const parsed = parse(href(route));
    expect(parsed.page).toBe(page);
    if ("id" in parsed) expect(parsed.id).toBe("identity / 中文?#");
    if (parsed.page === "task") expect(parsed.from).toBe("parent / issue");
  }
  const filters: Route = {
    page: "issues",
    view: "tree",
    project: "a & b",
    assignee: '{"kind":"owner","ref":""}',
    closed: false,
  };
  expect(parse(href(filters))).toEqual(filters);
  expect(parse("/issues?closed=true")).toEqual({ page: "issues", closed: true });
  expect(parse("/issues?view=bad")).toEqual({ page: "issues" });
  for (const path of ["/elsewhere", "/tasks/", "/issues/a/b", "/chat/%ZZ"])
    expect(parse(path)).toEqual({ page: "missing" });
});

test("legacy links replace identities and discard retired work item references", () => {
  expect(legacyURL("/?issue=A&task=B&work_item=old")).toBe("/tasks/B?from=A");
  expect(legacyURL("/?task=B")).toBe("/tasks/B");
  expect(legacyURL("/?issue=A%2FB")).toBe("/issues/A%2FB");
  expect(legacyURL("/issues?work_item=old&view=tree")).toBe("/issues?view=tree");
  expect(legacyURL("/inbox")).toBeNull();
});

test("links retain real hrefs and leave modified clicks to the browser", () => {
  const link = Link({ to: { page: "issue", id: "A/B" }, children: "Issue" });
  expect(link.props.href).toBe("/issues/A%2FB");
  for (const modifiers of [
    { button: 1 },
    { metaKey: true },
    { ctrlKey: true },
    { shiftKey: true },
    { altKey: true },
  ]) {
    const preventDefault = vi.fn();
    link.props.onClick({
      button: 0,
      ...modifiers,
      preventDefault,
    } as unknown as MouseEvent<HTMLAnchorElement>);
    expect(preventDefault).not.toHaveBeenCalled();
  }
});

test("history belongs to this document session; reload and foreign entries use the fallback", async () => {
  const listeners = new Map<string, () => void>();
  let location = new URL("https://tend.example/?issue=parent");
  const entries: { state: unknown; url: string }[] = [{ state: null, url: location.href }];
  let index = 0;
  const history = {
    get state() {
      return entries[index]!.state;
    },
    scrollRestoration: "auto",
    replaceState(state: unknown, _title: string, url?: string) {
      entries[index] = { state, url: new URL(url ?? location.href, location).href };
      location = new URL(entries[index]!.url);
    },
    pushState(state: unknown, _title: string, url: string) {
      entries.splice(++index);
      entries.push({ state, url: new URL(url, location).href });
      location = new URL(entries[index]!.url);
    },
    back: vi.fn(() => {
      index--;
      location = new URL(entries[index]!.url);
      listeners.get("popstate")?.();
    }),
  };
  vi.stubGlobal("window", {
    history,
    get location() {
      return location;
    },
    addEventListener: (name: string, listener: () => void) => listeners.set(name, listener),
  });
  vi.stubGlobal("HTMLElement", class {});
  vi.stubGlobal("document", { activeElement: null, querySelectorAll: () => [] });
  const router = await import(`./router.ts`);
  router.startRouter();
  expect(location.pathname).toBe("/issues/parent");
  expect(entries).toHaveLength(1);
  router.back({ page: "issues" });
  expect(history.back).not.toHaveBeenCalled();
  expect(location.pathname).toBe("/issues");
  router.navigate({ page: "issue", id: "parent" });
  router.navigate({ page: "task", id: "child", from: "parent" });
  router.back({ page: "tasks" });
  expect(history.back).toHaveBeenCalledOnce();
  expect(location.pathname).toBe("/issues/parent");
  router.navigate("/?task=legacy&issue=parent", { replace: true });
  expect(location.pathname + location.search).toBe("/tasks/legacy?from=parent");
  history.replaceState({}, "", "/?issue=foreign");
  listeners.get("popstate")?.();
  expect(location.pathname).toBe("/issues/foreign");
  router.back({ page: "issues" });
  expect(history.back).toHaveBeenCalledOnce();
  expect(location.pathname).toBe("/issues");
  const preventDefault = vi.fn();
  Link({ to: { page: "inbox" } }).props.onClick({
    button: 0,
    preventDefault,
  } as unknown as MouseEvent<HTMLAnchorElement>);
  expect(preventDefault).toHaveBeenCalledOnce();
  expect(location.pathname).toBe("/inbox");
  router.navigate({ page: "chat" });
  router.navigate("/?issue=legacy", { replace: true });
  const heading = { getClientRects: () => [{}], focus: vi.fn(), tabIndex: 0 };
  const root = {
    querySelectorAll: () => [heading],
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  vi.stubGlobal(
    "MutationObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  router.restorePage(root as unknown as HTMLElement)();
  expect(heading.tabIndex).toBe(-1);
  expect(heading.focus).toHaveBeenCalledWith({ preventScroll: true });
});
