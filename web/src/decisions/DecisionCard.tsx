// A question for the owner, with its options as buttons (D71). Options
// tend carries out itself are marked "tend 执行"; the others are sent to
// the main agent as a message.
import { useEffect, useState, type ReactNode } from "react";
import type { DecisionView, TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { noOutbox } from "../queue/sendQueue.ts";
import { StatusDot } from "../shell/StatusDot.tsx";
import { useStore } from "../state/store.ts";
import { setControl } from "../tasks/actions.ts";
import { answer, messageAnswer } from "./answer.ts";
import styles from "./DecisionCard.module.css";
import { effectOf, optionLabel, optionsOf, question } from "./text.ts";

export function DecisionCard({
  decision: d,
  task,
  more,
}: {
  decision: DecisionView;
  task: TaskSummary | undefined;
  /** What goes at the end of the head, such as the pager. */
  more?: ReactNode;
}) {
  const s = useServices();
  const conversation = useStore(s.live.conversation);
  const outbox = useStore(s.queue?.list ?? noOutbox);
  const [busy, setBusy] = useState(false);
  const byMessage = messageAnswer(d, conversation.items, outbox);
  const failed = byMessage?.state === "failed";

  useEffect(() => {
    // tend refused the answer, most likely because the decision changed.
    if (failed) {
      void s.live.refreshSnapshot();
    }
  }, [failed, s.live]);

  const options = optionsOf(d);
  const primary = options.find((o) => effectOf(d, o));
  const choose = async (option: string) => {
    setBusy(true);
    try {
      await answer(s, d, option, task?.title);
    } finally {
      setBusy(false);
    }
  };
  const pending = byMessage?.state === "pending";

  return (
    <div className={styles.card}>
      <div className={styles.head}>
        <StatusDot tone="warn" />
        <span className={styles.what}>待决{task ? ` · ${task.title}` : ""}</span>
        <span className={styles.more}>
          {more ??
            (d.task_id && (
              <button type="button" onClick={() => s.openTask(d.task_id!)}>
                详情 ›
              </button>
            ))}
        </span>
      </div>
      <q className={styles.question}>{question(d).text}</q>
      {pending ? (
        <div className={styles.ctx}>
          已回答「{optionLabel(d, byMessage.option)}」，等主 Agent 处理
        </div>
      ) : (
        <div className={styles.opts}>
          {options.map((o) => (
            <button
              key={o}
              type="button"
              className={styles.opt}
              data-primary={o === primary || undefined}
              disabled={busy}
              onClick={() => void choose(o)}
            >
              {optionLabel(d, o)}
              {effectOf(d, o) && <span className={styles.eff}>tend 执行</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** A task the owner took over: tend waits until it is handed back. */
export function HandbackCard({ task }: { task: TaskSummary }) {
  const s = useServices();
  const [busy, setBusy] = useState(false);
  const handBack = async () => {
    setBusy(true);
    try {
      await setControl(s, task, "managed");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className={styles.card}>
      <div className={styles.head}>
        <StatusDot tone="warn" />
        <span className={styles.what}>接管中 · {task.title}</span>
        <span className={styles.more}>
          <button type="button" onClick={() => s.openTask(task.task_id)}>
            详情 ›
          </button>
        </span>
      </div>
      <q className={styles.question}>你接手了这个任务的 worker，tend 暂停推进。处理完后交还。</q>
      {task.workspace && <div className={styles.ctx}>工作区 {task.workspace}</div>}
      <div className={styles.opts}>
        <button
          type="button"
          className={styles.opt}
          data-primary
          disabled={busy}
          onClick={() => void handBack()}
        >
          交还给 tend
        </button>
      </div>
    </div>
  );
}
