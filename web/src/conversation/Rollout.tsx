import { rememberOpener } from "../ui/focus.ts";
import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { ProcessText } from "../rollout/ProcessText.tsx";
import type {
  AgentCompactionPage,
  IssueAgentConversationPage,
  AgentRolloutPage,
  AgentRolloutRecord,
  AgentRolloutWindow,
} from "../api/generated/api.ts";
import styles from "./Agents.module.css";
export function Rollout({ onClose }: { onClose(): void }) {
  const { client } = useServices();
  const [id, setID] = useState("main");
  const [history, setHistory] = useState(false);
  const [conversations, setConversations] = useState<IssueAgentConversationPage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const request = useRef<AbortController | null>(null);
  const ref = useRef<HTMLDialogElement>(null);
  const readConversations = async (after?: string) => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError("");
    try {
      const query = new URLSearchParams({ projection: "issues" });
      if (after) query.set("after", after);
      const page = await client.get<IssueAgentConversationPage>(
        `/api/v1/agent-conversations?${query}`,
        controller.signal,
      );
      if (!controller.signal.aborted)
        setConversations((old) => ({
          ...page,
          items: after ? [...(old?.items ?? []), ...(page.items ?? [])] : page.items,
        }));
    } catch (e) {
      if (!controller.signal.aborted) setError(String(e));
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };
  useEffect(() => {
    const dialog = ref.current;
    if (dialog && !dialog.open) {
      rememberOpener(dialog, document.activeElement);
      dialog.showModal();
    }
    return () => request.current?.abort();
  }, []);
  const path = `/api/v1/agent-conversations/${encodeURIComponent(id)}`;
  return (
    <dialog
      ref={ref}
      className={styles.editor}
      aria-label="持久处理记录"
      onCancel={onClose}
      onClose={onClose}
    >
      <h3>会话诊断</h3>
      <label>
        会话
        <select value={id} onChange={(event) => setID(event.target.value)}>
          <option value="main">主 Agent</option>
          {conversations?.items?.map((item) => (
            <option key={item.conversation_id} value={item.conversation_id}>
              {item.title}
            </option>
          ))}
        </select>
      </label>
      {(!conversations || conversations.next) && (
        <button
          type="button"
          disabled={loading}
          onClick={() => void readConversations(conversations?.next)}
        >
          {conversations ? "加载更多会话" : "加载其他会话"}
        </button>
      )}
      {error && <p role="alert">{error}</p>}
      <div>
        <button type="button" aria-pressed={!history} onClick={() => setHistory(false)}>
          执行记录
        </button>
        <button type="button" aria-pressed={history} onClick={() => setHistory(true)}>
          Compact 历史
        </button>
      </div>
      {history ? (
        <CompactionHistory key={id} path={`${path}/compactions`} />
      ) : (
        <ExecutionRecords key={id} path={`${path}/rollout`} />
      )}
      <footer>
        <button onClick={onClose}>关闭</button>
      </footer>
    </dialog>
  );
}

function ExecutionRecords({ path }: { path: string }) {
  const { client } = useServices();
  const [page, setPage] = useState<AgentRolloutPage | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    request.current = abort;
    void client
      .get<AgentRolloutPage>(path, abort.signal)
      .then((result) => {
        if (abort.signal.aborted) return;
        setPage(result);
        setError("");
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [client, path, refresh]);
  const more = async () => {
    const signal = request.current?.signal;
    if (!signal || signal.aborted) return;
    setPending(true);
    try {
      const next = await client.get<AgentRolloutPage>(`${path}?after=${page?.next ?? 0}`, signal);
      if (!signal.aborted)
        setPage((old) => ({
          ...next,
          records: [...(old?.records ?? []), ...(next.records ?? [])],
        }));
    } catch (e) {
      if (!signal.aborted) setError(String(e));
    } finally {
      if (!signal.aborted) setPending(false);
    }
  };
  return (
    <>
      <p>这里展示已采集的原生记录。早期过程的覆盖范围取决于实际采集情况。</p>
      <p>Thread 代次：{page?.threads?.length ?? 0}</p>
      {error && <p role="alert">{error}</p>}
      <ProcessText parts={page?.live_text} title="正在生成的回复片段" />
      {page?.records?.map((r) => (
        <Record key={r.seq} record={r} path={path} />
      ))}
      {page?.more && (
        <button disabled={pending} onClick={() => void more()}>
          加载更多
        </button>
      )}
      <button type="button" disabled={pending} onClick={() => setRefresh((version) => version + 1)}>
        刷新记录
      </button>
    </>
  );
}

function CompactionHistory({ path }: { path: string }) {
  const { client } = useServices();
  const [page, setPage] = useState<AgentCompactionPage | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    request.current = abort;
    void client
      .get<AgentCompactionPage>(path, abort.signal)
      .then((value) => {
        if (!abort.signal.aborted) setPage(value);
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(String(e));
      });
    return () => abort.abort();
  }, [client, path]);
  const more = async () => {
    const signal = request.current?.signal;
    if (!signal || signal.aborted || !page?.next) return;
    setPending(true);
    try {
      const next = await client.get<AgentCompactionPage>(`${path}?before=${page.next}`, signal);
      if (!signal.aborted)
        setPage((old) => ({ ...next, items: [...(old?.items ?? []), ...(next.items ?? [])] }));
    } catch (e) {
      if (!signal.aborted) setError(String(e));
    } finally {
      if (!signal.aborted) setPending(false);
    }
  };
  return (
    <>
      {error && <p role="alert">{error}</p>}
      {page &&
        (page.items?.length ? (
          <ul>
            {page.items.map((entry) => (
              <li key={entry.operation_id}>
                {entry.source === "manual" ? "手动" : "自动"} ·{" "}
                {compactLabels[entry.status] ?? entry.status} ·{" "}
                {new Date(entry.created_at).toLocaleTimeString()}
              </li>
            ))}
          </ul>
        ) : (
          <p>暂无记录</p>
        ))}
      {page?.next && (
        <button disabled={pending} onClick={() => void more()}>
          加载更早的记录
        </button>
      )}
    </>
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

function Record({ record: r, path }: { record: AgentRolloutRecord; path: string }) {
  const { client } = useServices();
  const [window, setWindow] = useState<AgentRolloutWindow>({
    text: r.data,
    next: [...r.data].length,
    more: r.truncated,
  });
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const more = async () => {
    setPending(true);
    try {
      const next = await client.get<AgentRolloutWindow>(`${path}/${r.seq}?offset=${window.next}`);
      setWindow((old) => ({ ...next, text: old.text + next.text }));
    } catch (e) {
      setError(String(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <details>
      <summary>
        {new Date(r.created_at).toLocaleTimeString()} · {r.kind}
      </summary>
      <p>Turn：{r.turn_id || "—"}</p>
      <pre
        style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", fontSize: "var(--text-micro)" }}
      >
        {window.text}
      </pre>
      {error && <p role="alert">{error}</p>}
      {window.more && (
        <button disabled={pending} onClick={() => void more()}>
          继续读取原记录
        </button>
      )}
    </details>
  );
}
