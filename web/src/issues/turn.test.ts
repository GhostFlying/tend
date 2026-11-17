import { expect, test } from "vitest";
import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import { IssueTurnRead } from "./turn.ts";

test("loaded continuation replies refresh and expose newly appended pages", async () => {
  let text = "streaming";
  let appended = false;
  const read = new IssueTurnRead({
    get: async <T>(path: string) => {
      const after = Number(new URL(path, "https://example.test").searchParams.get("after"));
      return {
        trigger: { message_id: "trigger" },
        items: [
          { message_id: `reply-${after}`, body: after ? text : "first page", seq: after + 1 },
        ],
        more: after === 0 || (after === 50 && appended),
        next_seq: after + 50,
      } as T;
    },
  });
  await read.open("trigger");
  expect(read.state.get().value?.items?.at(-1)?.body).toBe("streaming");
  text = "final response";
  appended = true;
  await read.refresh();
  expect(read.state.get().value?.items?.at(-1)?.body).toBe("final response");
  expect(read.state.get().value?.more).toBe(true);
  await read.more();
  expect(read.state.get().value?.items).toHaveLength(3);
  expect((read.state.get().value as IssueTranscriptBlock).more).toBe(false);
  read.close();
});

test("process arrival pagination is independent of reply seq and rereads mutable steps", async () => {
  const calls: string[] = [];
  let status = "running";
  const read = new IssueTurnRead({
    get: async <T>(path: string) => {
      calls.push(path);
      const url = new URL(path, "https://example.test");
      const after = Number(url.searchParams.get("process_after"));
      return {
        trigger: { message_id: "trigger" },
        items: [],
        more: false,
        process: {
          items: [{ message_id: `tool-${after}`, tool: { status } }],
          more: after === 0,
          next: after ? undefined : 37,
          started_at: 1000,
        },
      } as T;
    },
  });
  await read.open("trigger");
  expect(calls.at(-1)).toContain("after=0&process_after=37");
  expect(read.state.get().value?.process?.items).toHaveLength(2);
  status = "completed";
  await read.refresh();
  expect(read.state.get().value?.process?.items?.[0]?.tool?.status).toBe("completed");
  read.close();
});
