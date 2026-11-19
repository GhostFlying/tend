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
