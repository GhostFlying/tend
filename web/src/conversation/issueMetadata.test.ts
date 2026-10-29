import { expect, test } from "vitest";
import type { Reader } from "../live/data.ts";
import { issueAssociationCommand, issueConversationReader } from "./issueMetadata.ts";

test("canonical chat metadata uses the same scoped reader and exact original source versions", async () => {
  const calls: { path: string; signal?: AbortSignal }[] = [];
  const api: Reader = {
    get<T>(path: string, signal?: AbortSignal): Promise<T> {
      calls.push({ path, ...(signal ? { signal } : {}) });
      return Promise.resolve({} as T);
    },
  };
  const read = issueConversationReader(api, "issue:original/identity");
  const controller = new AbortController();
  await read.get("/api/v1/conversation?before=8", controller.signal);
  const query = new URL(calls[0]!.path, "https://tend.invalid").searchParams;
  expect(query.get("conversation_id")).toBe("issue:original/identity");
  expect(query.get("projection")).toBe("issues");
  expect(query.get("before")).toBe("8");
  expect(calls[0]!.signal).toBe(controller.signal);
  for (const path of [
    "/api/v1/conversation/queue?category=background&after=18",
    "/api/v1/conversation/queue?category=withdrawn&after=19",
    "/api/v1/conversation/transcript?before=21",
    "/api/v1/conversation/blocks/trigger-original?after=22",
  ]) {
    await read.get(path, controller.signal);
    const call = calls.at(-1)!;
    const actual = new URL(call.path, "https://tend.invalid");
    const requested = new URL(path, "https://tend.invalid");
    expect(actual.pathname).toBe(requested.pathname);
    for (const [key, value] of requested.searchParams)
      expect(actual.searchParams.get(key)).toBe(value);
    expect(actual.searchParams.get("projection")).toBe("issues");
    expect(actual.searchParams.get("conversation_id")).toBe("issue:original/identity");
    expect(call.signal).toBe(controller.signal);
  }
  await read.get("/api/v1/runs/original/messages?after=7");
  expect(calls.at(-1)!.path).toBe("/api/v1/runs/original/messages?after=7");
  const snapshot = { message_id: "original", revision: 3, content_revision: 5 };
  const command = issueAssociationCommand(snapshot, "linked", ["same-opaque-id"]);
  snapshot.revision = 10;
  snapshot.content_revision = 12;
  expect(command.expected_control_revision).toBe(3);
  expect(command.payload).toEqual({
    disposition: "linked",
    content_revision: 5,
    issue_ids: ["same-opaque-id"],
  });
});
