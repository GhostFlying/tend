import { expect, test } from "vitest";
import { actorLabel } from "./actorLabel.ts";

test("protocol run principals distinguish worker questions from manager questions", () => {
  expect(actorLabel({ kind: "owner" })).toBe("你");
  expect(actorLabel({ kind: "main_agent" })).toBe("主 Agent");
  expect(actorLabel({ kind: "run", role: "implement" })).toBe("执行 Agent");
  expect(actorLabel({ kind: "run", role: "accept" })).toBe("执行 Agent");
  expect(actorLabel({ kind: "run", role: "issue_manager" })).toBe("负责人 Agent");
});
