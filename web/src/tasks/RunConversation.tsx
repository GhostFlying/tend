import { runLabel } from "./runLabel.ts";
import { useEffect, useState, type ReactNode } from "react";
import type { RunView, TaskDetail, SendRunMessagePayload } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import { runSupplementCommand } from "../conversation/actions.ts";
import type { OpOutcome } from "../app/ops.ts";
import styles from "../agents/Catalog.module.css";
import layout from "./RunConversation.module.css";
import form from "../attention/Inbox.module.css";

export function RunConversation({ task, controls }: { task: TaskDetail; controls: ReactNode }) {
  const [explicitSelection, setSelected] = useState("");
  const selected = explicitSelection || task.execution?.run_id || task.runs?.at(-1)?.run_id || "";
  const run = task.runs?.find((r) => r.run_id === selected);
  return (
    <section className={layout.root} aria-label="运行与补充">
      <label>
        执行记录
        <select value={selected} onChange={(e) => setSelected(e.target.value)}>
          {task.runs?.map((r) => (
            <option key={r.run_id} value={r.run_id}>
              {runLabel(task.runs ?? [], r)}
            </option>
          ))}
        </select>
      </label>
      {run ? (
        <RunPanel key={run.run_id} task={task} run={run} controls={controls} />
      ) : (
        <p className={styles.muted}>尚无执行记录。</p>
      )}
    </section>
  );
}
function RunPanel({
  task,
  run,
  controls,
}: {
  task: TaskDetail;
  run: RunView;
  controls: ReactNode;
}) {
  const { live, client } = useServices();
  const native = useStore(live.directChats.nativeEntry.state);
  const messages = useStore(live.directChats.runMessages.state);
  const [showInfo, setShowInfo] = useState(false);
  const [text, setText] = useState("");
  const [result, setResult] = useState<OpOutcome | null>(null);
  const operation = useDurableOp(client, `run-supplement:${run.run_id}`);
  useEffect(() => {
    void live.directChats.openNativeEntry(run.run_id);
    void live.directChats.openRunMessages(run.run_id);
    return () => {
      live.directChats.nativeEntry.close();
      live.directChats.runMessages.close();
    };
  }, [live, run.run_id]);
  const n = native.value;
  const original = operation.original?.payload as SendRunMessagePayload | undefined;
  const active =
    task.control_mode === "managed" &&
    ["running", "waiting"].includes(run.observed) &&
    run.spec_version !== undefined;
  const send = async (reconcile = false) => {
    try {
      if (!reconcile && run.spec_version === undefined) return;
      const out = await operation.execute(
        reconcile
          ? undefined
          : runSupplementCommand(run.run_id, task.control_revision, {
              task_id: task.task_id,
              spec_version: run.spec_version!,
              generation: run.generation,
              text,
            }),
      );
      setResult(out);
      if (out.kind === "done") {
        setText("");
        void live.directChats.runMessages.refresh();
      }
    } catch {
      setResult({ kind: "unknown", message: "尚未确认接收，请核实原消息。" });
    }
  };
  return (
    <>
      <section className={layout.native}>
        <h3>{n?.provider === "orca" ? "Orca 会话" : "原生会话信息"}</h3>
        {native.error ? (
          <p className={styles.warning}>
            会话信息暂时无法读取。
            <button onClick={() => void live.directChats.nativeEntry.refresh()}>重新读取</button>
          </p>
        ) : n ? (
          <div>
            <div className={layout.execution}>
              <div>
                <span>工作区 / 主机</span>
                <strong>
                  {n.workspace_id || n.workspace_path || "工作区未记录"} · {n.host_id || "未记录"}
                </strong>
              </div>
              <div>
                <span>执行 Agent</span>
                <strong>{n.execution?.identity.local_id || "未记录"}</strong>
              </div>
              <div>
                <span>运行时</span>
                <strong>{n.provider === "orca" ? "Orca" : n.provider || "未记录"}</strong>
              </div>
              <div>
                <span>模型</span>
                <strong>
                  {n.execution
                    ? `${n.execution.profile.model} ${n.execution.profile.effort || ""}`
                    : "未记录"}
                </strong>
              </div>
            </div>
            {!n.adapter_implemented || !n.session_recorded ? (
              <p className={styles.muted}>{n.reason || "当前没有可查看的原生入口。"}</p>
            ) : (
              <>
                <button
                  className={styles.button}
                  aria-expanded={showInfo}
                  onClick={() => setShowInfo(!showInfo)}
                >
                  查看会话信息
                </button>
                {showInfo && (
                  <div className={layout.identity}>
                    <div>
                      <span>会话</span>
                      <strong>
                        {n.identity?.session_id || n.identity?.observed_session_id || "未记录"}
                      </strong>
                    </div>
                    <div>
                      <span>终端</span>
                      <strong>{n.identity?.terminal || "未记录"}</strong>
                    </div>
                    <div>
                      <span>pane</span>
                      <strong>{n.identity?.pane || "未记录"}</strong>
                    </div>
                    <div>
                      <span>控制状态</span>
                      <strong>
                        {n.controller_observation === "unknown" ? "未知" : n.controller_observation}
                      </strong>
                    </div>
                    {n.workspace_path && (
                      <div>
                        <span>工作区路径</span>
                        <strong>{n.workspace_path}</strong>
                      </div>
                    )}
                    {(n.provider_id || n.provider_instance_id) && (
                      <div>
                        <span>工作区后端 / 实例</span>
                        <strong>
                          {n.provider_id || "未记录"} · {n.provider_instance_id || "未记录"}
                        </strong>
                      </div>
                    )}
                  </div>
                )}
              </>
            )}
            <p className={styles.muted}>查看不改变控制权。</p>
          </div>
        ) : (
          <p>正在读取原生信息…</p>
        )}
      </section>
      <section className={layout.control}>
        <h3>管理操作</h3>
        {controls}
        <p className={styles.muted}>接管后暂停 Agent 自动执行；交还后再恢复管理。</p>
      </section>
      <section className={layout.supplement}>
        <h3>补充给当前运行</h3>
        <textarea
          className={form.textarea}
          aria-label="补充给当前运行"
          value={original?.text ?? text}
          onChange={(e) => setText(e.target.value)}
          disabled={operation.busy || operation.pending || !active}
        />
        <p className={styles.muted}>
          补充不修改任务约定，不会转发给其它执行。接收回执不表示模型已采用。
        </p>
        {!active && !operation.pending && (
          <p className={styles.muted}>
            {run.spec_version === undefined
              ? "运行版本未记录，不能发送精确补充。"
              : "仅在该运行处于托管执行或等待状态时可发送补充。"}
          </p>
        )}
        {operation.storageError && <p role="alert">{operation.storageError}</p>}
        {result && !operation.pending && (
          <p role="status">{result.kind === "done" ? "补充请求已记录。" : result.message}</p>
        )}
        <div className={styles.actions}>
          {operation.pending ? (
            <div className={styles.warning}>
              尚未确认接收，请勿重复发送。
              <button
                className={styles.primary}
                disabled={operation.busy}
                onClick={() => void send(true)}
              >
                核实原消息状态
              </button>
            </div>
          ) : (
            <button
              className={styles.primary}
              disabled={operation.busy || !active || !text.trim()}
              onClick={() => void send()}
            >
              发送给当前执行
            </button>
          )}
        </div>
      </section>
      <h3>补充记录</h3>
      {messages.error && (
        <p className={styles.warning}>
          记录暂时无法读取。
          <button onClick={() => void live.directChats.runMessages.refresh()}>重新读取</button>
        </p>
      )}
      {(messages.value?.messages ?? []).map((message) => (
        <article key={message.message_id}>
          <p>{message.text}</p>
          <p className={styles.muted}>
            {new Date(message.created_at).toLocaleString("zh-CN")} ·{" "}
            {message.status === "accepted"
              ? "已接收"
              : message.status === "unknown"
                ? "结果待核实"
                : message.status}
            {message.reason ? ` · ${message.reason}` : ""}
          </p>
        </article>
      ))}
      {messages.value?.more && (
        <button
          className={styles.button}
          onClick={() => void live.directChats.openRunMessages(run.run_id, messages.value?.next)}
        >
          下一页补充
        </button>
      )}
    </>
  );
}
