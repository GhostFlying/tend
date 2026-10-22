import { describe, expect, test } from "vitest";
import type { DecisionView } from "../api/generated/api.ts";
import { answerMessage, effectOf, optionLabel, optionsOf, question } from "./text.ts";

/** A decision tend opened after a failed implementation (internal/domain). */
const tends: DecisionView = {
  decision_id: "dec_1",
  task_id: "task_1",
  question: "The implementation run failed (no_changes). How should this task proceed?",
  options: ["retry the implementation", "revise the spec", "cancel the task"],
  effects: { "retry the implementation": "implement", "cancel the task": "cancel" },
  version: 1,
  status: "open",
};

/** A decision the main agent opened. */
const agents: DecisionView = {
  decision_id: "dec_2",
  question: "Which part first?",
  options: ["cancel the task", "先改 CLI"],
  version: 3,
  status: "open",
};

describe("tend's fixed options", () => {
  test("are shown in Chinese", () => {
    expect(optionsOf(tends).map((o) => optionLabel(tends, o))).toEqual([
      "重新实现",
      "修改 spec",
      "取消任务",
    ]);
    const others: [string, string][] = [
      ["raise the budget", "提高预算"],
      ["retry acceptance", "重新验收"],
      ["fix the acceptance environment", "修好验收环境"],
      ["wait for the host", "等待这台 host"],
      ["start over on another host", "换一台从头再来"],
    ];
    for (const [option, label] of others) {
      expect(optionLabel(tends, option)).toBe(label);
    }
  });

  test("the main agent's options are shown as written, even when they match", () => {
    expect(optionsOf(agents).map((o) => optionLabel(agents, o))).toEqual([
      "cancel the task",
      "先改 CLI",
    ]);
  });

  test("only options with an effect are tend's to carry out", () => {
    expect(effectOf(tends, "retry the implementation")).toBe("implement");
    expect(effectOf(tends, "revise the spec")).toBeUndefined();
    expect(effectOf(agents, "cancel the task")).toBeUndefined();
  });
});

describe("questions", () => {
  test("tend's are shown in Chinese with a short reason", () => {
    expect(question(tends)).toEqual({
      text: "实现失败（no_changes）。这个任务接下来怎么办？",
      short: "实现失败",
    });
    const ws = {
      ...tends,
      question:
        "The task's workspace is on host host-2, which was retired. Its work can only go on there. How should this task proceed?",
    };
    expect(question(ws).text).toBe(
      "任务的工作区在 host host-2 上，这台 host 已经停用。工作只能在那里继续。这个任务接下来怎么办？",
    );
  });

  test("anything else is shown as asked", () => {
    expect(question(agents)).toEqual({ text: "Which part first?", short: "Which part first?" });
    const odd = { ...tends, question: "Something new?" };
    expect(question(odd).text).toBe("Something new?");
  });
});

describe("answerMessage", () => {
  test("names the task and the option as shown", () => {
    expect(answerMessage(tends, "CLI 输出加版本号", "revise the spec")).toBe(
      "关于『CLI 输出加版本号』：修改 spec",
    );
    expect(answerMessage(agents, "CLI 输出加版本号", "先改 CLI")).toBe(
      "关于『CLI 输出加版本号』：先改 CLI",
    );
  });

  test("without a task, names the question", () => {
    expect(answerMessage(agents, undefined, "先改 CLI")).toBe(
      "关于『Which part first?』：先改 CLI",
    );
    const long = { ...agents, question: "这".repeat(60) };
    expect(answerMessage(long, undefined, "好")).toBe(`关于『${"这".repeat(39)}…』：好`);
  });
});

test("options that are not a list of strings are ignored", () => {
  expect(optionsOf({ options: null })).toEqual([]);
  expect(optionsOf({ options: ["a", 2, "b"] })).toEqual(["a", "b"]);
});
