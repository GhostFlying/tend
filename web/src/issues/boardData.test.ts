import { expect, test } from "vitest";
import type { IssuePage, IssueSummary } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { IssueBoardData, boardPagePath } from "./boardData.ts";

const flush = async () => {
  for (let i = 0; i < 30; i++) await Promise.resolve();
};
const item = (id: string): IssueSummary => ({
  issue_id: id,
  title: id,
  project: "sample",
  state: "active",
  assignee: { kind: "owner", ref: "owner" },
  assignment_epoch: 1,
  assignment_state: "active",
  revision: 1,
});
class Deferred implements Reader {
  requests: { path: string; signal?: AbortSignal | undefined; resolve(value: IssuePage): void }[] =
    [];
  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return new Promise((resolve) =>
      this.requests.push({ path, signal, resolve: (value) => resolve(value as T) }),
    );
  }
  async answer(status: string, page: IssuePage) {
    const i = this.requests.findIndex(
      (r) => new URL(r.path, "https://tend.invalid").searchParams.get("board_status") === status,
    );
    expect(i).toBeGreaterThanOrEqual(0);
    const request = this.requests.splice(i, 1)[0]!;
    request.resolve(page);
    await flush();
    return request;
  }
}
test("project and assignee filters always retain top-level scope", () => {
  const url = new URL(
    boardPagePath(
      {
        project: "sample",
        assignee: { kind: "agent", ref: "hosted/manager" },
        includeClosed: true,
      },
      "done",
    ),
    "https://tend.invalid",
  );
  expect(Object.fromEntries(url.searchParams)).toMatchObject({
    parent_id: "",
    board: "true",
    board_status: "done",
    assignee_ref: "hosted/manager",
    include_closed: "true",
  });
});
test("column pagination appends the loaded prefix, counts remain server totals, refresh removes moved cards", async () => {
  const api = new Deferred();
  const board = new IssueBoardData(api);
  board.open({});
  await api.answer("todo", {
    items: [item("a")],
    more: true,
    next: "a",
    counts: { todo: 3 },
    facts_seq: 2,
  });
  expect(api.requests.every((request) => !request.path.startsWith("/api/v1/issues/"))).toBe(true);
  const column = board.columns.get("todo")!;
  void column.more();
  await api.answer("todo", {
    items: [item("a")],
    more: true,
    next: "a",
    counts: { todo: 3 },
    facts_seq: 2,
  });
  const next = await api.answer("todo", {
    items: [item("b"), item("c")],
    more: false,
    counts: { todo: 3 },
    facts_seq: 2,
  });
  expect(new URL(next.path, "https://tend.invalid").searchParams.get("after")).toBe("a");
  expect(column.read.state.get().value?.items?.map((x) => x.issue_id)).toEqual(["a", "b", "c"]);
  expect(board.summary.get().counts?.todo).toBe(3);
  void column.read.refresh();
  await api.answer("todo", { items: [item("c")], more: false, counts: { todo: 1 }, facts_seq: 3 });
  expect(column.read.state.get().value?.items?.map((x) => x.issue_id)).toEqual(["c"]);
  void column.read.refresh();
  await api.answer("todo", { items: [item("a")], more: false, counts: { todo: 3 }, facts_seq: 2 });
  expect(column.read.state.get().value?.items?.map((x) => x.issue_id)).toEqual(["c"]);
  expect(board.summary.get().counts?.todo).toBe(1);
  board.close();
});
test("late responses from previous filters cannot replace current cards or counts", async () => {
  const api = new Deferred();
  const board = new IssueBoardData(api);
  board.open({ project: "old" });
  const old = api.requests.splice(0);
  board.open({ project: "new" });
  await api.answer("todo", {
    items: [item("new")],
    more: false,
    counts: { todo: 1 },
    facts_seq: 5,
  });
  for (const request of old) {
    expect(request.signal?.aborted).toBe(true);
    request.resolve({ items: [item("old")], more: false, counts: { todo: 99 }, facts_seq: 99 });
  }
  await flush();
  expect(board.summary.get().counts?.todo).toBe(1);
  expect(board.columns.get("todo")!.read.state.get().value?.items?.[0]?.issue_id).toBe("new");
  board.close();
});
