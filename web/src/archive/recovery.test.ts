import { expect, test } from "vitest";
import type { Envelope } from "../api/generated/api.ts";
import { pendingArchiveIssues } from "./recovery.ts";

const envelope: Envelope = {
  request_id: "req-original",
  operation: "issues.create",
  target: { type: "issue", id: "wi-original" },
  payload: {
    source_message_id: "msg-original",
    expected_source_revision: 7,
    plan: { title: "Original goal" },
  },
};
const pending = [{ scope: "issue-create:msg-original", envelope }];

test("archive resume uses the unfinished Issue's source and frozen revision, not completed request history", async () => {
  const paths: string[] = [];
  const get = async <T>(path: string): Promise<T> => {
    paths.push(path);
    return {
      message_id: "msg-original",
      metadata: { url: "https://fixture.invalid/api/v1/archives/archive-a" },
      content_revision: 8,
    } as T;
  };
  expect(
    await pendingArchiveIssues({ get }, "archive-a", "https://fixture.invalid", pending),
  ).toEqual({
    items: [{ requestID: "req-original", sourceMessageID: "msg-original", sourceRevision: 7 }],
    unreadable: false,
  });
  expect(paths).toEqual(["/api/v1/sources/msg-original"]);
  expect(envelope.payload).toMatchObject({ expected_source_revision: 7 });
});

test("foreign archive/source, malformed pending drafts, and unreadable sources do not authorize resume", async () => {
  for (const source of [
    {
      message_id: "foreign",
      metadata: { url: "https://fixture.invalid/api/v1/archives/archive-a" },
    },
    {
      message_id: "msg-original",
      metadata: { url: "https://fixture.invalid/api/v1/archives/foreign" },
    },
  ]) {
    const get = async <T>() => source as T;
    expect(
      await pendingArchiveIssues({ get }, "archive-a", "https://fixture.invalid", pending),
    ).toEqual({ items: [], unreadable: false });
  }
  const paths: string[] = [];
  const get = async <T>(path: string): Promise<T> => {
    paths.push(path);
    throw new Error("forbidden or missing");
  };
  expect(
    await pendingArchiveIssues({ get }, "archive-a", "https://fixture.invalid", pending),
  ).toEqual({ items: [], unreadable: true });
  paths.length = 0;
  expect(
    await pendingArchiveIssues({ get }, "archive-a", "https://fixture.invalid", [
      { scope: "issue-create:other", envelope },
      {
        scope: "issue-create:msg-original",
        envelope: { ...envelope, payload: { source_message_id: "msg-original" } },
      },
    ]),
  ).toEqual({ items: [], unreadable: false });
  expect(paths).toEqual([]);
});
