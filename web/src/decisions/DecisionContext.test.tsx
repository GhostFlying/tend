import { expect, test } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { DecisionView } from "../api/generated/api.ts";
import { DecisionContext } from "./DecisionContext.tsx";

const decision: DecisionView = {
  decision_id: "dec_fixture",
  version: 1,
  status: "open",
  question: "Deliver with gaps?",
  options: ["Deliver", "Wait"],
  worker: {
    run_id: "run_fixture",
    role: "implement",
    spec_version: 1,
    question_key: "scope",
    question_message_id: "msg_fixture",
    evidence: "long raw output",
    issues: [
      {
        issue_id: "scratch",
        category: "environment",
        summary: "scratch unavailable",
        impact: "analysis continues in memory",
        next_action: "repair preparation",
        actor: "main_agent",
        status: "workaround",
        source: "command result",
      },
    ],
  },
};

test("material issues are visible outside folded evidence", () => {
  const html = renderToStaticMarkup(<DecisionContext decision={decision} />);
  expect(html).toContain("环境故障");
  expect(html).toContain("analysis continues in memory");
  expect(html).toContain("repair preparation");
  expect(html).not.toContain("<details");
  expect(html).not.toContain("long raw output");
});

test("coordinator disposition keeps original decision identity", () => {
  const briefing = {
    revision: 2,
    summary: "Separate environment and evidence",
    owner_choice: "Choose delivery scope",
    issues: [{ ...decision.worker!.issues![0]!, status: "resolved" }],
    source_message_id: "msg_context",
    created_at: 1,
  };
  const html = renderToStaticMarkup(<DecisionContext decision={{ ...decision, briefing }} />);
  expect(html).toContain("已解决");
  expect(html).toContain("Choose delivery scope");
  expect(decision.version).toBe(1);
  expect(decision.worker!.issues![0]!.status).toBe("workaround");
});

test("legacy decisions render unchanged", () => {
  const legacy = { ...decision };
  delete legacy.worker;
  expect(renderToStaticMarkup(<DecisionContext decision={legacy} />)).toBe("");
});
