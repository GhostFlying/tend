import { useState } from "react";
import type { WorkItemChoice } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import styles from "./WorkItems.module.css";
import controls from "../shell/controls.module.css";

const labels: Record<string, string> = {
  answered: "已回答",
  withdrawn: "已撤回",
  superseded: "已替代",
};
export function ChoiceCard({
  choice,
  readOnly = false,
}: {
  choice: WorkItemChoice;
  readOnly?: boolean;
}) {
  const { client, live, openWorkItem } = useServices();
  const connection = useStore(client.connection);
  const response = live.workItems.choiceResponse(
    choice.choice_id,
    client,
    () => void live.choiceChanged(choice.choice_id),
  );
  const state = useStore(response.state);
  const reading = useStore(live.choiceRead(choice.choice_id).state);
  const [text, setText] = useState("");
  if (choice.status !== "open")
    return (
      <details className={styles.history}>
        <summary>
          {labels[choice.status] ?? choice.status} · {choice.question}
        </summary>
        {choice.answer_source_kind === "addon_relay" && (
          <p className={styles.meta}>由外接 agent 转交用户回答</p>
        )}
        {choice.answer && <Markdown text={choice.answer} />}
        {choice.reason && <Markdown text={choice.reason} />}
        {choice.replaced_by && (
          <button
            type="button"
            className={controls.link}
            onClick={() => openWorkItem(choice.work_item_id)}
          >
            查看最新安排
          </button>
        )}
      </details>
    );
  const disabled =
    readOnly ||
    connection.status !== "online" ||
    state.pending ||
    !!state.unknown ||
    !!reading.error;
  return (
    <section className={styles.choice} aria-label="下一步安排">
      <h3>{choice.question}</h3>
      {reading.error && (
        <p role="status" className={styles.notice}>
          选择状态更新失败，正在重试。暂不能回答旧选择。
          <button
            type="button"
            className={controls.link}
            disabled={reading.loading || connection.status !== "online"}
            onClick={() => void live.choiceChanged(choice.choice_id)}
          >
            刷新选择状态
          </button>
        </p>
      )}
      {choice.recommendation && <p className={styles.meta}>建议：{choice.recommendation}</p>}
      <div className={styles.actions}>
        {(choice.options ?? []).map((option) => (
          <button
            type="button"
            key={option}
            className={controls.chipBtn}
            disabled={disabled}
            onClick={() => void response.answer(choice, option)}
          >
            {option}
          </button>
        ))}
      </div>
      <details className={styles.choiceOther}>
        <summary>其他安排</summary>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void response.answer(choice, text);
          }}
        >
          <label>
            你的安排
            <textarea
              value={text}
              onChange={(event) => setText(event.target.value)}
              disabled={disabled}
              rows={2}
            />
          </label>
          <button type="submit" className={controls.chipBtn} disabled={disabled || !text.trim()}>
            发送回答
          </button>
        </form>
      </details>
      {state.unknown && (
        <div className={styles.actions}>
          <button
            className={controls.link}
            disabled={state.pending || connection.status !== "online"}
            onClick={() => void response.check()}
          >
            核对回答
          </button>
          {state.unknown.missing && (
            <button
              className={controls.link}
              disabled={state.pending || connection.status !== "online"}
              onClick={() => void response.retry()}
            >
              重试原回答
            </button>
          )}
        </div>
      )}
      {state.message && <p role="status">{state.message}</p>}
    </section>
  );
}
