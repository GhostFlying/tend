import { expect, test } from "vitest";
import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import { IssueProcessReads } from "./process.ts";

const block = (id: string): IssueTranscriptBlock => ({
  trigger: { message_id: id, author: "owner", kind: "message", seq: 1, created_at: "" },
  position: 1,
  items: [],
  more: false,
});

test("opening a process shares its in-flight prefetch and bypasses another turn's queue", async () => {
  const requests: { path: string; priority?: RequestPriority | undefined }[] = [];
  const complete = new Map<string, (reply: IssueTranscriptBlock) => void>();
  const reader = new IssueProcessReads({
    get: async <T>(path: string, _signal?: AbortSignal, priority?: RequestPriority) => {
      requests.push({ path, priority });
      const id = path.split("/").at(-1)!.split("?")[0]!;
      return (await new Promise<IssueTranscriptBlock>((finish) => complete.set(id, finish))) as T;
    },
  });
  const latest = block("latest");
  const foreground = block("foreground");
  const prefetch = reader.prefetch([latest, block("queued")]);
  const same = reader.load(latest);
  expect(reader.load(latest)).toBe(same);
  const other = reader.load(foreground);
  expect(requests.map((request) => request.priority)).toEqual(["low", "high"]);
  complete.get("foreground")!(foreground);
  await other;
  complete.get("latest")!(latest);
  await same;
  await Promise.resolve();
  expect(requests.at(-1)?.path).toContain("queued");
  complete.get("queued")!(block("queued"));
  await prefetch;
  reader.close();
});

test("failed prefetch can be retried and leaving the issue aborts the remaining read", async () => {
  const signals: AbortSignal[] = [];
  const reader = new IssueProcessReads({
    get: async <T>(_path: string, signal?: AbortSignal) => {
      signals.push(signal!);
      if (signals.length === 1) throw new Error("unavailable");
      return (await new Promise((_, reject) => {
        signal!.addEventListener("abort", () => reject(signal!.reason));
      })) as T;
    },
  });
  const latest = block("latest");
  await reader.prefetch([latest]);
  const explicit = reader.load(latest);
  const failed = expect(explicit).rejects.toThrow();
  reader.close();
  await failed;
  expect(signals).toHaveLength(2);
  expect(signals[1]?.aborted).toBe(true);
});

test("a revised turn reloads all disclosed pages and shares the depth read", async () => {
  const requested: string[] = [];
  const start = block("turn");
  const revision = { ...start, items: [] };
  const reader = new IssueProcessReads({
    get: async <T>(path: string) => {
      requested.push(path);
      const second = path.includes("process_after=");
      return {
        ...start,
        process: {
          items: [{ ...start.trigger, message_id: second ? "tool-2" : "tool-1" }],
          more: !second,
          ...(second ? {} : { next: 11 }),
        },
      } as T;
    },
  });
  const first = await reader.load(start);
  const second = await reader.load(start, "high", first.depth + 1);
  expect(second.depth).toBe(2);
  const reload = reader.load(revision, "high", second.depth);
  expect(reader.load(revision, "high", second.depth)).toBe(reload);
  expect((await reload).items.map((item) => item.message_id)).toEqual(["tool-1", "tool-2"]);
  expect(requested.map((path) => path.includes("process_after="))).toEqual([
    false,
    true,
    false,
    true,
  ]);
  reader.close();
});

test("public process pages order records whose transcript callbacks arrived early", async () => {
  const turn = block("ordered");
  const tools = Array.from({ length: 3 }, (_, i) => ({
    ...turn.trigger,
    message_id: `tool-${i + 1}`,
    kind: "tool_use",
    seq: i + 1,
    tool: {
      name: "fixture",
      input: "",
      output: "",
      status: "completed",
      turn_id: "ordered",
      updated_at: "",
    } as const,
  }));
  const reader = new IssueProcessReads({
    get: async <T>(path: string) => {
      const second = path.includes("process_after=1");
      return {
        ...turn,
        items: second ? [] : [{ ...tools[2], seq: 12 }],
        process: {
          items: second ? tools.slice(1) : tools.slice(0, 1),
          more: !second,
          ...(second ? {} : { next: 1 }),
        },
      } as T;
    },
  });
  const result = await reader.load(turn, "high", 2);
  expect(result.items.map(({ message_id }) => message_id)).toEqual(["tool-1", "tool-2", "tool-3"]);
  reader.close();
});
