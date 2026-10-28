import { expect, test } from "vitest";
import type { RunLogView, RunRolloutPage } from "../api/generated/api.ts";
import { buildSteps, mergePage, statusLabel } from "./model.ts";

function record(
  seq: number,
  kind: string,
  thread = "parent",
  turn = "turn",
  data: object = {},
): RunLogView {
  return {
    seq,
    kind,
    native_id: "call",
    created_at: "2026-10-27T03:00:00Z",
    body: {
      thread_id: thread,
      turn_id: turn,
      item_id: "call",
      data: { type: "commandExecution", command: "go test ./...", ...data },
    },
  };
}
test("one native call keeps its row when the result arrives, including out-of-order starts", () => {
  const start = record(1, "native.item_started");
  const end = record(3, "item.completed", "parent", "turn", {
    aggregatedOutput: "ok",
    exitCode: 0,
  });
  const steps = buildSteps([start, end, { ...start, seq: 4 }]);
  expect(steps).toHaveLength(1);
  expect(steps[0]).toMatchObject({
    key: buildSteps([start])[0]!.key,
    pending: false,
    output: "ok",
    input: "go test ./...",
    gap: true,
  });
  expect(steps[0]!.rows).toHaveLength(3);
  expect(buildSteps([end])[0]!.key).toBe(steps[0]!.key);
});
test("the same item ID never pairs across thread, turn or absent historical scope", () => {
  const rows = [
    record(1, "native.item_started"),
    record(2, "item.completed", "child"),
    record(3, "item.completed", "parent", "next"),
    {
      seq: 4,
      kind: "item.completed",
      native_id: "call",
      created_at: "",
      body: { type: "commandExecution", command: "legacy" },
    },
  ];
  expect(buildSteps(rows)).toHaveLength(4);
  expect(buildSteps(rows)[0]!.pending).toBe(true);
  const legacy = rows[3]!;
  expect(buildSteps([legacy, { ...legacy, seq: 5 }])).toHaveLength(2);
});
test("child envelopes pair explicit item identity; unidentified calls stay separate", () => {
  const child = (seq: number, kind: string) => ({
    ...record(seq, "native.child"),
    native_id: undefined,
    body: {
      thread_id: "child",
      turn_id: "turn",
      kind,
      data: {
        id: "child-call",
        type: "mcpToolCall",
        tool: "read_file",
        arguments: { path: "src/index.ts" },
      },
    },
  });
  const rows = [child(1, "item_started"), child(2, "item_completed")].map(
    ({ native_id: _unused, ...row }) => row,
  );
  expect(buildSteps(rows)).toHaveLength(1);
  expect(buildSteps(rows)[0]).toMatchObject({
    title: "read_file",
    summary: "src/index.ts",
    pending: false,
  });
  expect(
    buildSteps(
      [1, 2].map((seq) => ({
        seq,
        kind: "item.completed",
        created_at: "",
        body: { type: "agentMessage", text: "reply" },
      })),
    ),
  ).toHaveLength(2);
});
test("errors, truncation and observed execution states are explicit", () => {
  const [step] = buildSteps([
    record(1, "item.completed", "parent", "turn", {
      exitCode: 1,
      aggregatedOutput: "failed",
      truncated: true,
    }),
  ]);
  expect(step).toMatchObject({ failed: true, truncated: true, output: "failed" });
  expect(statusLabel("finished")).toBe("已结束");
  expect(statusLabel("unknown")).toBe("状态待确认");
});
function page(from: number, to: number, moreBefore = true): RunRolloutPage {
  return {
    run_id: "run",
    observed: "running",
    before: from,
    after: to,
    watermark: 1000,
    more_before: moreBefore,
    more_after: true,
    logs: Array.from({ length: Math.max(0, to - from + 1) }, (_, index) =>
      record(from + index, "item.completed"),
    ),
  };
}
test("history prepends keep the current window and both boundaries; after pages remain bounded", () => {
  const result = mergePage(page(101, 200), page(1, 100, false), "before");
  expect(result.logs).toHaveLength(200);
  expect(result).toMatchObject({ before: 1, after: 200, more_before: false, more_after: true });
  const full = mergePage(result, { ...page(201, 600), more_after: false }, "after");
  expect(full).toMatchObject({ before: 201, after: 600, more_before: true, more_after: false });
  expect(full.logs).toHaveLength(400);
});
test("empty live polls preserve historical cursor and do not fabricate an older boundary", () => {
  const old = page(1, 10, false);
  const next = { ...page(0, 0, false), logs: [], before: 0, after: 10, more_after: false };
  expect(mergePage(old, next, "after")).toMatchObject({
    before: 1,
    after: 10,
    more_before: false,
    more_after: false,
  });
});

test("an empty live window preserves the incoming backlog boundary", () => {
  const empty = { ...page(0, 0, false), logs: [], before: 0, after: 0, more_after: false };
  const tail = { ...page(151, 250, true), more_after: false };
  const live = mergePage(empty, tail, "after");
  expect(live).toMatchObject({ before: 151, after: 250, more_before: true, more_after: false });
  const middle = mergePage(live, page(51, 150, true), "before");
  const full = mergePage(middle, page(1, 50, false), "before");
  expect(full).toMatchObject({ before: 1, after: 250, more_before: false, more_after: false });
  expect(full.logs).toHaveLength(250);
});

test("Orca's persisted reply is readable prose rather than an empty lifecycle event", () => {
  const [step] = buildSteps([
    {
      seq: 1,
      kind: "turn.completed",
      created_at: "",
      body: { reply: "## Result\n\nChecks passed." },
    },
  ]);
  expect(step).toMatchObject({ kind: "text", text: "## Result\n\nChecks passed." });
});
test("native query, image path and subagent prompt remain available as inputs", () => {
  for (const [type, field] of [
    ["webSearch", "query"],
    ["imageView", "path"],
    ["collabAgentToolCall", "prompt"],
  ]) {
    const [step] = buildSteps([
      {
        seq: 1,
        kind: "item.completed",
        created_at: "",
        body: { type, [field!]: "collected input" },
      },
    ]);
    expect(step).toMatchObject({
      kind: "tool",
      input: "collected input",
      summary: "collected input",
    });
  }
});
test("backgrounded commands stay pending and declined commands show failure", () => {
  const [backgrounded] = buildSteps([
    record(1, "item.completed", "parent", "turn", { status: "backgrounded", exitCode: 0 }),
  ]);
  expect(backgrounded).toMatchObject({ pending: true, failed: false });
  const [declined] = buildSteps([
    record(1, "item.completed", "parent", "turn", { status: "declined" }),
  ]);
  expect(declined).toMatchObject({ pending: false, failed: true });
});
test("bounded completion metadata settles its start and retains the known command", () => {
  const [step] = buildSteps([
    record(1, "native.item_started"),
    {
      seq: 2,
      kind: "item.completed",
      created_at: "",
      native_id: "call",
      body: {
        thread_id: "parent",
        turn_id: "turn",
        item_id: "call",
        type: "commandExecution",
        status: "completed",
        truncated: true,
      },
    },
  ]);
  expect(step).toMatchObject({
    pending: false,
    truncated: true,
    input: "go test ./...",
    summary: "go test ./...",
  });
});

test("server receipt timestamps never become execution durations", () => {
  const start = record(1, "native.item_started");
  const end = record(2, "item.completed");
  expect(buildSteps([start, end])[0]!.durationMs).toBeNull();
  expect(
    buildSteps([start, record(2, "item.completed", "parent", "turn", { durationMs: 1234 })])[0]!
      .durationMs,
  ).toBe(1234);
});
