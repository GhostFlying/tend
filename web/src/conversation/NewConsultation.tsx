import { useEffect, useState } from "react";
import type {
  IssueAgentConversation as AgentConversation,
  CreateConsultationPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import { useStore } from "../state/store.ts";
import { consultationCommand } from "./actions.ts";
import styles from "../agents/Catalog.module.css";

export function NewConsultation({
  onCreated,
  onBack,
}: {
  onCreated(id: string): void;
  onBack(): void;
}) {
  const { client, live } = useServices();
  const hosted = useStore(live.agents.hosted.state);
  const operation = useDurableOp(client, "create-consultation");
  const [agent, setAgent] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    void live.agents.openHosted();
  }, [live]);
  const original = operation.original?.payload as CreateConsultationPayload | undefined;
  const candidates =
    hosted.value?.projection.agents?.filter((a) => a.purposes?.includes("consultation")) ?? [];
  const save = async (reconcile = false) => {
    try {
      const out = await operation.execute(
        reconcile ? undefined : consultationCommand({ agent_ref: agent, title }),
      );
      if (out.kind === "done") {
        const c = out.result as AgentConversation;
        if (c?.conversation_id) onCreated(c.conversation_id);
        else setError("已创建，刷新会话列表查看。");
      } else setError(out.message);
    } catch {
      setError("咨询创建结果待核实，请保留原请求。");
    }
  };
  const disabled = operation.busy || operation.pending;
  return (
    <section className={`${styles.root} ${styles.form}`} aria-label="新建咨询">
      <h1>新建咨询</h1>
      <p className={styles.muted}>
        选择已登记的托管Agent。普通问答不创建Issue或任务；你明确要求执行时，咨询Agent可将实际来源委派为Issue。
      </p>
      <label>
        咨询Agent
        <select
          disabled={disabled}
          value={original?.agent_ref ?? agent}
          onChange={(e) => setAgent(e.target.value)}
        >
          <option value="">选择托管Agent</option>
          {candidates.map((a) => {
            const ref = `${a.identity.execution_environment_id}/${a.identity.local_id}`;
            return (
              <option value={ref} key={ref} disabled={!a.enabled}>
                {a.display_name} · {a.identity.local_id}
                {a.enabled ? "" : "（配置未启用）"}
              </option>
            );
          })}
        </select>
      </label>
      {hosted.error && (
        <p className={styles.warning}>
          配置目录暂时无法读取。
          <button onClick={() => void live.agents.hosted.refresh()}>重新读取</button>
        </p>
      )}
      {hosted.value && !candidates.length && (
        <p>没有登记可用于咨询的托管Agent。执行专用Agent不会作为咨询候选。</p>
      )}
      <label>
        标题
        <input
          value={original?.title ?? title}
          disabled={disabled}
          onChange={(e) => setTitle(e.target.value)}
        />
      </label>
      {(error || operation.storageError) && (
        <p className={styles.warning} role="status">
          {error || operation.storageError}
        </p>
      )}
      <div className={styles.actions}>
        {operation.pending ? (
          <button
            className={styles.primary}
            disabled={operation.busy}
            onClick={() => void save(true)}
          >
            核实原创建请求
          </button>
        ) : (
          <button
            className={styles.primary}
            disabled={disabled || !agent || !title.trim()}
            onClick={() => void save()}
          >
            开始咨询
          </button>
        )}
        <button className={styles.button} onClick={onBack}>
          返回会话
        </button>
      </div>
    </section>
  );
}
