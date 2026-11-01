import { expect, test } from "vitest";
import type { IssueActivityPage, IssueActivityEntry } from "../api/generated/api.ts";
import { IssueActivityRead } from "./activity.ts";
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
