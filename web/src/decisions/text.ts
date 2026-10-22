// How decisions read in the Web (D71). tend opens its decisions with fixed
// English questions and options (internal/domain); those are shown in
// Chinese. A decision the main agent opened is shown as it wrote it. The
// answer the Web sends for an option tend does not carry out is a
// readable Chinese message that names the task and the option.
import type { DecisionEffect, DecisionView } from "../api/generated/api.ts";

/** tend's fixed options, as internal/domain/task.go names them. */
const fixedOptions: Record<string, string> = {
  "retry the implementation": "重新实现",
  "raise the budget": "提高预算",
  "retry acceptance": "重新验收",
  "revise the spec": "修改 spec",
  "fix the acceptance environment": "修好验收环境",
  "cancel the task": "取消任务",
  "wait for the host": "等待这台 host",
  "start over on another host": "换一台从头再来",
};

export const effectLabels: Record<DecisionEffect, string> = {
  implement: "重新实现",
  accept: "重新验收",
  cancel: "取消任务",
  start_over: "换一台从头再来",
};

const proceed = "这个任务接下来怎么办？";

/** tend's fixed questions: the Chinese text and a short reason for lists. */
const fixedQuestions: [RegExp, (m: RegExpExecArray) => { text: string; short: string }][] = [
  [
    /^The implementation run failed \((.*)\)\. How should this task proceed\?$/s,
    (m) => ({ text: `实现失败（${m[1]}）。${proceed}`, short: "实现失败" }),
  ],
  [
    /^The acceptance run failed \((.*)\)\. How should this task proceed\?$/s,
    (m) => ({ text: `验收失败（${m[1]}）。${proceed}`, short: "验收失败" }),
  ],
  [
    /^Acceptance could not reach a verdict\. How should this task proceed\?$/,
    () => ({ text: `验收没能得出结论。${proceed}`, short: "验收没有结论" }),
  ],
  [
    /^The implement attempt budget is exhausted and acceptance still requires changes\. How should this task proceed\?$/,
    () => ({ text: `实现次数已经用完，验收仍然要求修改。${proceed}`, short: "实现次数用完" }),
  ],
  [
    /^The task's workspace is on host (\S+), which (.*)\. Its work can only go on there\. How should this task proceed\?$/s,
    (m) => ({
      text: `任务的工作区在 host ${m[1]} 上，这台 host ${hostGone(m[2]!)}。工作只能在那里继续。${proceed}`,
      short: "工作区不可用",
    }),
  ],
];

function hostGone(why: string): string {
  if (why === "was retired") {
    return "已经停用";
  }
  const since = /^has not been heard from since (.+)$/.exec(why);
  return since ? `从 ${since[1]} 起就没有消息` : why;
}

/** Whether tend opened the decision itself: only those have effects (D53). */
export function isTends(d: Pick<DecisionView, "effects">): boolean {
  return Object.keys(d.effects ?? {}).length > 0;
}

/** The decision's options, as tend names them. */
export function optionsOf(d: Pick<DecisionView, "options">): string[] {
  return Array.isArray(d.options) ? d.options.filter((o) => typeof o === "string") : [];
}

/** How an option is shown: tend's in Chinese, the main agent's as written. */
export function optionLabel(d: Pick<DecisionView, "effects">, option: string): string {
  return (isTends(d) && fixedOptions[option]) || option;
}

/** The effect tend carries out for an option, if any. */
export function effectOf(
  d: Pick<DecisionView, "effects">,
  option: string,
): DecisionEffect | undefined {
  return d.effects?.[option];
}

/** The question as shown, and a short form for one-line lists. */
export function question(d: Pick<DecisionView, "effects" | "question">): {
  text: string;
  short: string;
} {
  if (isTends(d)) {
    for (const [pattern, show] of fixedQuestions) {
      const m = pattern.exec(d.question);
      if (m) {
        return show(m);
      }
    }
  }
  return { text: d.question, short: d.question };
}

/**
 * The message that answers a decision with an option tend does not carry
 * out, for the main agent to act on: which task, and the option as shown.
 */
export function answerMessage(
  d: Pick<DecisionView, "effects" | "question">,
  taskTitle: string | undefined,
  option: string,
): string {
  const about = taskTitle ? taskTitle : clip(question(d).text, 40);
  return `关于『${about}』：${optionLabel(d, option)}`;
}

function clip(s: string, n: number): string {
  const chars = [...s];
  return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
}
