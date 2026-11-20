import { expect, test } from "vitest";
import { ApiError } from "../api/client.ts";
import type { IssueActivityPage, IssueActivityEntry } from "../api/generated/api.ts";
import { IssueActivityRead, projectActivityGroups } from "./activity.ts";
const entry = (id: string, position: number, title = id): IssueActivityEntry => ({
  id,
  position,
  title,
  kind: "issue.updated",
  created_at: 1,
  author: "system",
});
const page = (items: IssueActivityEntry[], more = false, next_before = 0): IssueActivityPage => ({
  items,
  more,
  next_before,
  event_seq: 10,
});

test("a rejected activity cursor rebuilds from the authoritative head and fences old history", async () => {
  const requests: URLSearchParams[] = [];
  let ledger = 300;
  let finishEarlier: (value: IssueActivityPage) => void = () => {};
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      const query = new URL(path, "https://example.test").searchParams;
      requests.push(query);
      if (query.has("before"))
        return (await new Promise<IssueActivityPage>((resolve) => {
          finishEarlier = resolve;
        })) as T;
      if (Number(query.get("since")) > ledger)
        throw new ApiError(400, { code: "invalid_request", message: "invalid activity cursor" });
      return {
        ...page([entry(String(ledger), ledger)], ledger === 300, ledger),
        readable: true,
        event_seq: ledger,
      } as T;
    },
  });
  try {
    await read.open("one");
    const history = read.earlier();
    ledger = 100;
    await read.refresh();
    finishEarlier(page([entry("stale", 99)]));
    await history;
    expect(requests.map((query) => query.get("since"))).toEqual([null, null, "300", null]);
    expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["100"]);
    expect(read.state.get().value?.event_seq).toBe(100);
  } finally {
    read.close();
  }
});

test("an authoritative reset requests a fresh head even when the old cursor is valid", async () => {
  let latest = "old";
  const read = new IssueActivityRead({
    get: async <T>(path: string) =>
      ({
        ...page(path.includes("since=") ? [] : [entry(latest, 1)]),
        readable: true,
      }) as T,
  });
  try {
    await read.open("one");
    latest = "rebuilt";
    await read.rebuild();
    expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["rebuilt"]);
  } finally {
    read.close();
  }
});

test("new activity retains loaded older records and replaces an existing streamed row", async () => {
  let latest = page([entry("a", 3), entry("b", 4)], true, 3);
  const read = new IssueActivityRead({
    get: async <T>(path: string) =>
      (path.includes("before=")
        ? page(
            [entry("older", 1), ...(Number(path.split("before=")[1]) > 3 ? [entry("a", 3)] : [])],
            false,
          )
        : latest) as T,
  });
  await read.open("one");
  await read.earlier();
  latest = page([entry("b", 4, "updated reply"), entry("c", 5)], true, 4);
  await read.refresh();
  expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["older", "a", "b", "c"]);
  expect(read.state.get().value?.items?.find((row) => row.id === "b")?.title).toBe("updated reply");
  expect(read.state.get().value?.more).toBe(false);
  read.close();
});
test("late pagination for another Issue cannot populate the newly selected Issue", async () => {
  let resolveEarlier: (value: IssueActivityPage) => void = () => {};
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      if (path.includes("before="))
        return (await new Promise<IssueActivityPage>((resolve) => {
          resolveEarlier = resolve;
        })) as T;
      return page([entry(path.includes("/two/") ? "two" : "one", 2)], true, 2) as T;
    },
  });
  await read.open("one");
  const pending = read.earlier();
  await read.open("two");
  resolveEarlier(page([entry("old one", 1)]));
  await pending;
  expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["two"]);
  read.close();
});

test("a disconnected refresh bridges all new pages even after older history was exhausted", async () => {
  let latest = 4;
  const requested: string[] = [];
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      requested.push(path);
      const before =
        Number(new URL(path, "https://example.test").searchParams.get("before")) || latest + 1;
      const end = Math.min(latest, before - 1);
      return page(
        [entry(String(end - 1), end - 1), entry(String(end), end)],
        end > 2,
        end - 1,
      ) as T;
    },
  });
  await read.open("one");
  await read.earlier();
  expect(read.state.get().value?.more).toBe(false);
  latest = 10;
  await read.refresh();
  expect(read.state.get().value?.items?.map((row) => row.position)).toEqual([
    1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
  ]);
  expect(read.state.get().value?.more).toBe(false);
  expect(requested.some((path) => path.includes("before=5"))).toBe(true);
  read.close();
});

test("returning to a parent restores its loaded boundary before bridging the new head", async () => {
  const read = new IssueActivityRead({
    get: async <T>(path: string) =>
      (path.includes("before=")
        ? page([entry("old", 1)], false)
        : page([entry("latest", 3), entry("new", 4)], true, 3)) as T,
  });
  await read.open("parent", page([entry("old", 1), entry("latest", 3)], false));
  expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["old", "latest", "new"]);
  expect(read.state.get().value?.more).toBe(false);
  read.close();
});

test("active blocks outside the latest page receive their final streamed revision", async () => {
  const streaming = {
    ...entry("turn", 1),
    block: {
      position: 1,
      trigger: { message_id: "trigger", state: "processing" },
      items: [{ message_id: "reply", body: "partial", assistant_state: "streaming" }],
      more: false,
    },
  } as IssueActivityEntry;
  let first = true;
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      if (path.includes("before="))
        return page(
          [
            {
              ...streaming,
              block: {
                ...streaming.block!,
                trigger: { ...streaming.block!.trigger, state: "replied" },
                items: [
                  { ...streaming.block!.items![0]!, body: "final", assistant_state: "completed" },
                ],
              },
            },
          ],
          false,
        ) as T;
      const result = first
        ? page([streaming, entry("overlap", 2)], false)
        : page([entry("overlap", 2), entry("new", 3)], true, 2);
      first = false;
      return result as T;
    },
  });
  await read.open("issue");
  await read.refresh();
  const turn = read.state.get().value?.items?.find((item) => item.id === "turn")?.block;
  expect(turn?.items?.[0]?.body).toBe("final");
  expect(turn?.trigger.state).toBe("replied");
  read.close();
});

test("withdrawn entries omitted by an authoritative page do not remain cached", async () => {
  let current = page([entry("withdrawn", 2), entry("kept", 3)], false);
  const read = new IssueActivityRead({ get: async <T>() => current as T });
  await read.open("issue");
  current = page([entry("kept", 3)], false);
  await read.refresh();
  expect(read.state.get().value?.items?.map((row) => row.id)).toEqual(["kept"]);
  read.close();
});

test("accepted supplements outside the latest page are refreshed after consumption", async () => {
  const supplement = {
    ...entry("accepted", 1),
    block: {
      position: 1,
      trigger: {
        message_id: "supplement",
        state: "accepted",
        author: "owner",
        kind: "message",
        seq: 1,
        created_at: "2026-01-01T00:00:00Z",
      },
      items: [],
      more: false,
    },
  } as IssueActivityEntry;
  let first = true;
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      if (path.includes("before="))
        return page(
          [
            {
              ...supplement,
              block: {
                ...supplement.block!,
                trigger: { ...supplement.block!.trigger, state: "replied" },
                items: [],
              },
            },
          ],
          false,
        ) as T;
      const result = first
        ? page([supplement, entry("overlap", 2)], false)
        : page([entry("overlap", 2), entry("new", 3)], true, 2);
      first = false;
      return result as T;
    },
  });
  await read.open("issue");
  await read.refresh();
  expect(
    read.state.get().value?.items?.find((row) => row.id === "accepted")?.block?.trigger.state,
  ).toBe("replied");
  read.close();
});

test("newly admitted older blocks are discovered even when the head overlaps exhausted history", async () => {
  let admitted = false;
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      if (path.includes("before="))
        return page([entry("created", 1), entry("backfilled-block", 3)], false) as T;
      return (
        admitted
          ? page([entry("overlap", 4), entry("new", 5)], true, 4)
          : page([entry("created", 1), entry("overlap", 4)], false)
      ) as T;
    },
  });
  await read.open("issue");
  admitted = true;
  await read.refresh();
  expect(read.state.get().value?.items?.map((item) => item.id)).toEqual([
    "created",
    "backfilled-block",
    "overlap",
    "new",
  ]);
  expect(read.state.get().value?.more).toBe(false);
  read.close();
});

test("merges only adjacent background events by actor, Issue and a bounded window", async () => {
  const { groupActivityEvents } = await import("./activity.ts");
  const event = (id: string, at: number, author = "负责人", issue = "one") => ({
    ...entry(id, at),
    author,
    issue_id: issue,
    created_at: at,
    kind: "issue.progress_updated",
  });
  const rows = [
    event("a", 1000),
    event("b", 2000),
    event("c", 3000, "你"),
    event("d", 4000, "你", "child"),
    event("e", 200000, "你", "child"),
    { ...event("f", 201000, "你", "child"), kind: "task.failed" },
    event("g", 202000, "你", "child"),
  ];
  expect(groupActivityEvents(rows).map((group) => group.map((row) => row.id))).toEqual([
    ["a", "b"],
    ["c"],
    ["d"],
    ["e"],
    ["f"],
    ["g"],
  ]);
});

test("gap-based groups survive prepend across the original first-event window", async () => {
  const { groupActivityEvents, identifyActivityGroups } = await import("./activity.ts");
  const event = (second: number) => ({
    ...entry(String(second), second),
    author: "actor",
    created_at: second * 1000,
  });
  const identities = new Map<string, string>();
  const original = identifyActivityGroups(
    groupActivityEvents([50, 60, 130, 150].map(event)),
    identities,
  );
  expect(original).toHaveLength(1);
  const earlier = identifyActivityGroups(
    groupActivityEvents([0, 50, 60, 130, 150].map(event)),
    identities,
  );
  expect(earlier).toHaveLength(1);
  expect(earlier[0]!.key).toBe(original[0]!.key);
});

test("merged and split groups claim unique identities with original-member priority", async () => {
  const { identifyActivityGroups } = await import("./activity.ts");
  const identities = new Map<string, string>();
  const a = entry("a", 1),
    b = entry("b", 2),
    c = entry("c", 3),
    d = entry("d", 4);
  identifyActivityGroups(
    [
      [a, b],
      [c, d],
    ],
    identities,
  );
  const merged = identifyActivityGroups([[a, b, c, d]], identities);
  expect(merged[0]!.key).toBe("a");
  const split = identifyActivityGroups([[b], [a, c, d]], identities);
  expect(split[1]!.key).toBe("a");
  expect(new Set(split.map((group) => group.key)).size).toBe(split.length);
  const combined = identifyActivityGroups([[a, b, c, d]], identities);
  expect(new Set(combined.map((group) => group.key)).size).toBe(combined.length);
});

test.each([
  "run.unknown",
  "run.lost",
  "run.recovery",
  "issue.paused",
  "issue.cancelled",
  "attention.changed",
])("keeps %s visible between routine updates", async (kind) => {
  const { groupActivityEvents } = await import("./activity.ts");
  const rows = [entry("before", 1), { ...entry("incident", 2), kind }, entry("after", 3)];
  expect(groupActivityEvents(rows)).toEqual(rows.map((row) => [row]));
});

test("readable refresh merges deltas without rereading retained pages", async () => {
  const requested: URL[] = [];
  const readable = (items: IssueActivityEntry[], more = false, before = 0, seq = 20) => ({
    ...page(items, more, before),
    readable: true,
    event_seq: seq,
  });
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      const url = new URL(path, "https://example.test");
      requested.push(url);
      const since = url.searchParams.get("since");
      const before = url.searchParams.get("before");
      if (!since && !before) return readable([entry("newer", 3)], true, 3) as T;
      if (!since) return readable([entry("old", 1), entry("withdraw", 2)]) as T;
      if (since === "20") return readable([]) as T;
      if (before)
        return {
          ...readable([entry("old", 1, "updated reply")], false, 0, 22),
          removed: ["withdraw"],
        } as T;
      return readable([entry("new", 4)], true, 4, 22) as T;
    },
  });
  await read.open("issue");
  await read.earlier();
  // Advance the mock head through one empty observation.
  read.state.set({ value: { ...read.state.get().value!, event_seq: 21 }, loading: false });
  requested.length = 0;
  await read.refresh();
  expect(requested.map((url) => [...url.searchParams])).toEqual([
    [
      ["view", "readable"],
      ["since", "21"],
      ["from", "0"],
    ],
    [
      ["view", "readable"],
      ["since", "21"],
      ["from", "0"],
      ["through", "22"],
      ["before", "4"],
    ],
  ]);
  expect(read.state.get().value?.items?.map(({ id, title }) => [id, title])).toEqual([
    ["old", "updated reply"],
    ["newer", "newer"],
    ["new", "new"],
  ]);
  expect(read.state.get().value?.more).toBe(false);
  expect(read.state.get().value?.event_seq).toBe(22);
  read.close();
});

test("a pending history page does not delay live activity or lose earlier changes", async () => {
  const initial = { ...page([entry("current", 10)], true, 10), event_seq: 20, readable: true };
  let releaseHistory!: (value: IssueActivityPage) => void;
  const historyPage = new Promise<IssueActivityPage>((resolve) => {
    releaseHistory = resolve;
  });
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      const query = new URL(path, "https://example.test").searchParams;
      if (query.has("before")) return (await historyPage) as T;
      if (!query.has("since")) return initial as T;
      const updated = [entry("current", 10, "live reply")];
      if (query.get("from") === "0") updated.unshift(entry("old", 4, "changed earlier reply"));
      return { ...initial, items: updated, more: false, event_seq: 21 } as T;
    },
  });
  await read.open("issue");
  const earlier = read.earlier();
  await read.refresh();
  expect(read.state.get().value?.items?.map((row) => row.title)).toEqual(["live reply"]);
  releaseHistory({ ...initial, items: [entry("old", 4)], more: false });
  await earlier;
  expect(read.state.get().value?.items?.map((row) => row.title)).toEqual([
    "changed earlier reply",
    "live reply",
  ]);
  expect(read.state.get().value?.more).toBe(false);
  read.close();
});

test.each(["delta-first", "history-first"])(
  "activity keeps the new history boundary when reads resolve together (%s)",
  async (order) => {
    const initial = { ...page([entry("current", 10)], true, 10), event_seq: 20, readable: true };
    let finishDelta!: (value: IssueActivityPage) => void;
    let finishHistory!: (value: IssueActivityPage) => void;
    const delta = new Promise<IssueActivityPage>((resolve) => {
      finishDelta = resolve;
    });
    const history = new Promise<IssueActivityPage>((resolve) => {
      finishHistory = resolve;
    });
    const ranges: string[] = [];
    const read = new IssueActivityRead({
      get: async <T>(path: string) => {
        const query = new URL(path, "https://example.test").searchParams;
        if (query.has("before")) return (await history) as T;
        if (!query.has("since")) return initial as T;
        ranges.push(`${query.get("since")}:${query.get("from")}`);
        if (ranges.length === 1) return (await delta) as T;
        const items = query.get("from") === "0" ? [entry("old", 4, "updated old")] : [];
        return { ...initial, items, more: false, event_seq: 21 } as T;
      },
    });
    await read.open("issue");
    const earlier = read.earlier();
    const refresh = read.refresh();
    const releaseDelta = () =>
      finishDelta({
        ...initial,
        items: [entry("current", 10, "updated current")],
        more: false,
        event_seq: 21,
      });
    const releaseHistory = () =>
      finishHistory({ ...initial, items: [entry("old", 4)], more: false, next_before: 0 });
    if (order === "delta-first") {
      releaseDelta();
      releaseHistory();
    } else {
      releaseHistory();
      releaseDelta();
    }
    await Promise.all([earlier, refresh]);
    const value = read.state.get().value;
    read.close();
    expect(ranges).toEqual(["20:10", "20:0"]);
    expect(value?.items?.map(({ title }) => title)).toEqual(["updated old", "updated current"]);
    expect(value).toMatchObject({ more: false, next_before: 0, event_seq: 21 });
  },
);

test("returning to an Issue resumes from the retained cursor and removes a withdrawn old row", async () => {
  const head = entry("block:head", 100);
  const withdrawn = entry("block:withdrawn", 25);
  const retained = { ...page([withdrawn, head]), event_seq: 110, readable: true };
  const requests: string[] = [];
  const read = new IssueActivityRead({
    get: async <T>(path: string) => {
      const query = new URL(path, "https://example.test").searchParams;
      requests.push(`${query.get("since")}:${query.get("from")}`);
      return {
        ...page([], false),
        readable: true,
        event_seq: 111,
        removed: [withdrawn.id],
      } as T;
    },
  });
  await read.open("issue", retained);
  expect(requests).toEqual(["110:0"]);
  expect(read.state.get().value?.items).toEqual([head]);
  expect(read.state.get().value).toMatchObject({ event_seq: 111, more: false });
  read.close();
});

test("keeps a delivery card stable as observations arrive and earlier pages are loaded", () => {
  const changed = (
    position: number,
    head: string,
    previous: string,
    number = 372,
  ): IssueActivityEntry => ({
    ...entry(String(position), position),
    kind: "work_item.delivery_changed",
    issue_id: "issue",
    delivery: {
      delivery: {
        delivery_id: `github:repo#${number}`,
        provider: "github",
        repository: "Org/Repo",
        number,
      },
      observation: {
        issue_id: "issue",
        delivery_id: `github:repo#${number}`,
        generation: 1,
        checked_at: position,
        state: "open",
        head_sha: head,
        previous_head_sha: previous,
      },
    },
  });
  const earlier = changed(1, "b", "a");
  const firstLoaded = changed(2, "b", "a");
  const other = changed(3, "b", "", 373);
  const milestone = entry("milestone", 4);
  const same = changed(5, "b", "a");
  const retired: IssueActivityEntry = {
    ...changed(6, "b", "a"),
    kind: "work_item.delivery_candidate_retired",
    delivery: {
      ...same.delivery!,
      delivery: { ...same.delivery!.delivery, delivery_id: "retired", repository: "org/repo" },
    },
  };
  const next = changed(7, "c", "b");
  const returned = changed(8, "b", "c");
  const rows = [firstLoaded, other, milestone, same, retired, next, returned];
  const unchanged = structuredClone(rows);
  const identities = new Map<string, string>();
  const firstRead = projectActivityGroups(rows, identities);
  const fullRead = projectActivityGroups([earlier, ...rows], identities);
  expect(
    fullRead.map(({ entries, candidate }) => [
      entries.map((row) => row.id),
      candidate?.newCandidate,
    ]),
  ).toEqual([
    [["1", "2", "5", "6"], true],
    [["3"], false],
    [["milestone"], undefined],
    [["7"], true],
    [["8"], true],
  ]);
  expect(fullRead[0]?.key).toBe(firstRead[0]?.key);
  expect(rows).toEqual(unchanged);
});
