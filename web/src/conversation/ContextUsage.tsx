import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Connection } from "../api/client.ts";
import type {
  AgentContextUsage,
  AgentCompactionPage,
  AgentConversation,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import styles from "./Agents.module.css";
import { useAnchoredPanel } from "./useAnchoredPanel.ts";

export function ContextUsage({ connection }: { connection: Connection }) {
  const { client, conversation } = useServices();
  const id = conversation?.id ?? "main";
  const [usage, setUsage] = useState<AgentContextUsage | null>(null);
  const [history, setHistory] = useState<AgentCompactionPage | null>(null);
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const focusOnOpen = useRef(false);
  const position = useAnchoredPanel(open, trigger, 340);
  useLayoutEffect(() => {
    if (open && position.visibility !== "hidden" && focusOnOpen.current) {
      panel.current?.focus();
      focusOnOpen.current = false;
    }
  }, [open, position.visibility]);
  const close = () => {
    setOpen(false);
    focusOnOpen.current = false;
    trigger.current?.focus();
  };
  const [error, setError] = useState("");
  const operation = useDurableOp(client, `compact:${id}`);
  const base = `/api/v1/agent-conversations/${encodeURIComponent(id)}`;
  useEffect(() => {
    if (connection.status !== "online") return;
    const abort = new AbortController();
    const read = () => {
      void Promise.all([
        client.get<AgentContextUsage>(`${base}/usage`, abort.signal),
        client.get<AgentCompactionPage>(`${base}/compactions`, abort.signal),
      ])
        .then(([u, h]) => {
          setUsage(u);
          setHistory(h);
        })
        .catch(() => {});
    };
    read();
    const timer = setInterval(read, 5000);
    return () => {
      abort.abort();
      clearInterval(timer);
    };
  }, [client, base, connection.status]);
  const compact = async (reconcile = false) => {
    try {
      const c = await client.get<AgentConversation>(base);
      const result = await operation.execute(
        reconcile
          ? undefined
          : {
              operation: "conversation.compact",
              target: { type: "conversation", id },
              expected_control_revision: c.revision,
              payload: {},
            },
      );
      setError(result.kind === "done" ? "已排队，当前回复结束后执行。" : result.message);
      setHistory(await client.get<AgentCompactionPage>(`${base}/compactions`));
    } catch (e) {
      setError(String(e));
    }
  };
  const pending = history?.items?.some((x) =>
    ["queued", "starting", "running", "unknown"].includes(x.status),
  );
  const percent = usage?.used_percentage;
  return (
    <div className={styles.usage}>
      <button
        ref={trigger}
        className={styles.ring}
        style={{ background: `conic-gradient(var(--accent) ${percent ?? 0}%,var(--border) 0)` }}
        aria-label={`上下文用量 ${percent === undefined ? "未测量" : percent + "%"}`}
        aria-expanded={open}
        onClick={() => {
          focusOnOpen.current = !open;
          setOpen(!open);
        }}
      >
        <span>{percent === undefined ? "—" : percent + "%"}</span>
      </button>
      {open &&
        createPortal(
          <div
            ref={panel}
            tabIndex={-1}
            className={styles.popover}
            style={position}
            role="dialog"
            aria-label="上下文用量"
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                close();
              }
            }}
          >
            <h3>上下文 · {conversation?.title ?? "主 Agent"}</h3>
            <strong>{percent === undefined ? "尚无当前窗口测量" : `${percent}% 已使用`}</strong>
            <p>
              {usage?.active_tokens?.toLocaleString() ?? "—"} /{" "}
              {usage?.effective_window?.toLocaleString() ?? "—"} tokens
              <br />
              {connection.status !== "online"
                ? "连接已断开，显示最后一次采样"
                : ({
                    observed: "运行时测量",
                    estimated: "估算",
                    mixed: "测量与估算",
                    pending: "等待测量",
                  }[usage?.measurement ?? "pending"] ?? "未测量")}{" "}
              · {usage?.sampled_at ? new Date(usage.sampled_at).toLocaleTimeString() : "尚未采样"}
            </p>
            <button
              disabled={
                connection.status !== "online" ||
                (!operation.pending && !usage?.compact_supported) ||
                (!operation.pending && pending) ||
                operation.busy ||
                !!operation.storageError
              }
              onClick={() => void compact(operation.pending)}
            >
              {operation.pending
                ? "核对原请求"
                : pending
                  ? "Compact 进行中 / 待核对"
                  : "手动 compact"}
            </button>
            {!usage?.compact_supported && <p>当前运行时尚未确认原生 compact 能力。</p>}
            {error && <p role="status">{error}</p>}
            {operation.storageError && <p role="alert">{operation.storageError}</p>}
            {operation.pending && (
              <p>请求回执待核对。未找到回执时仅重试原请求，不另建 compact 操作。</p>
            )}
            <h4>Compact 历史</h4>
            {history?.items?.length ? (
              <ul>
                {history.items.map((h) => (
                  <li key={h.operation_id}>
                    {h.source === "manual" ? "手动" : "自动"} ·{" "}
                    {compactLabels[h.status] ?? h.status} ·{" "}
                    {new Date(h.created_at).toLocaleTimeString()}
                  </li>
                ))}
              </ul>
            ) : (
              <p>暂无记录</p>
            )}
            <button onClick={close}>关闭</button>
          </div>,
          document.body,
        )}
    </div>
  );
}
const compactLabels: Record<string, string> = {
  queued: "排队中",
  starting: "启动中",
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  unknown: "结果待核对",
};
