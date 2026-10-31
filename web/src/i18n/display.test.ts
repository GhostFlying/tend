import { expect, test } from "vitest";
import { shortHash } from "./identifiers.tsx";
import { assigneeDisplay } from "../issues/assigneeDisplay.ts";

test("commit hashes use seven characters without changing the original", () => {
  const hash = "abcdef1234567890";
  expect(shortHash(hash)).toBe("abcdef1");
  expect(shortHash("abc")).toBe("abc");
  expect(hash).toBe("abcdef1234567890");
});
test("responsibility uses catalog names without leaking raw references", () => {
  expect(assigneeDisplay({ kind: "owner", ref: "owner" })).toBe("你");
  expect(assigneeDisplay({ kind: "agent", ref: "hosted/manager" })).toBe("负责人 Agent");
  expect(assigneeDisplay({ kind: "agent", ref: "hosted/manager" }, "清竹")).toBe("清竹");
});
