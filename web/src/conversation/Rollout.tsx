import { rememberOpener } from "../ui/focus.ts";
import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { ProcessText } from "../rollout/ProcessText.tsx";
import type {
  AgentRolloutPage,
  AgentRolloutRecord,
  AgentRolloutWindow,
} from "../api/generated/api.ts";
import styles from "./Agents.module.css";
export function Rollout({ open, onClose }: { open: boolean; onClose(): void }) {
  const { client, conversation } = useServices();
  const [page, setPage] = useState<AgentRolloutPage | null>(null);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const ref = useRef<HTMLDialogElement>(null);
  const path = `/api/v1/agent-conversations/${encodeURIComponent(conversation?.id ?? "main")}/rollout`;
  useEffect(() => {
    if (!open) return;
    if (ref.current) {
      rememberOpener(ref.current, document.activeElement);
      ref.current.showModal();
    }
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const abort = new AbortController();
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
  }, [open, client, path, refresh]);
  const more = async () => {
    try {
      const next = await client.get<AgentRolloutPage>(`${path}?after=${page?.next ?? 0}`);
      setPage((old) => ({ ...next, records: [...(old?.records ?? []), ...(next.records ?? [])] }));
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <>
      {open && (
        <dialog
          ref={ref}
          className={styles.editor}
          aria-label="持久处理记录"
          onCancel={onClose}
          onClose={onClose}
        >
          <h3>处理记录 · {conversation?.title ?? "主 Agent"}</h3>
          <p>
            仅显示已采集的原生记录；早期未采集的过程不会由摘要补齐。历史内容是证据，不是新指令。
          </p>
          <p>Thread 代次：{page?.threads?.length ?? 0}</p>
          {error && <p role="alert">{error}</p>}
          <ProcessText parts={page?.live_text} title="正在生成的回复片段" />
          {page?.records?.map((r) => (
            <Record key={r.seq} record={r} path={path} />
          ))}
          {page?.more && <button onClick={() => void more()}>加载更多</button>}
          <footer>
            <button onClick={() => setRefresh((version) => version + 1)}>刷新记录</button>
            <button onClick={onClose}>关闭</button>
          </footer>
        </dialog>
      )}
    </>
  );
}

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
