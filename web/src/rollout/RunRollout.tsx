import { useEffect, useRef, useState } from "react";
import type { RunLogView, RunRolloutPage, RunView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { UsagePanel } from "../usage/Usage.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import styles from "./RunRollout.module.css";

export function RunRollout({ runs }: { runs: readonly RunView[] }) {
  const [selected, setSelected] = useState(runs.at(-1)?.run_id ?? "");
  const selectedID = runs.some((run) => run.run_id === selected)
    ? selected
    : (runs.at(-1)?.run_id ?? "");
  return (
    <section className={styles.root} aria-label="执行过程与用量">
      <label>
        执行记录{" "}
        <select value={selectedID} onChange={(e) => setSelected(e.target.value)}>
          {runs.map((r) => (
            <option key={r.run_id} value={r.run_id}>
              {r.role === "accept" ? "验收" : "实施"} · 第 {r.generation} 代 · {r.run_id}
            </option>
          ))}
        </select>
      </label>
      {selectedID ? <RunWindow key={selectedID} id={selectedID} /> : <p>尚未开始执行。</p>}
    </section>
  );
}
function RunWindow({ id }: { id: string }) {
  const { client } = useServices();
  const [cursor, setCursor] = useState("");
  const [live, setLive] = useState(true);
  const [retry, setRetry] = useState(0);
  const [page, setPage] = useState<RunRolloutPage | null>(null);
  const [error, setError] = useState("");
  const [usage, setUsage] = useState(false);
  const current = useRef<RunRolloutPage | null>(null);
  const loadedKey = useRef<string | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const requestKey = `${cursor}:${retry}`;
    const poll = async (first: boolean) => {
      const after = !first && current.current ? `after=${current.current.after}` : cursor;
      try {
        const next = await client.get<RunRolloutPage>(
          `/api/v1/runs/${encodeURIComponent(id)}/rollout?${after}`,
          abort.signal,
        );
        if (abort.signal.aborted) return;
        loadedKey.current = requestKey;
        if (first) {
          current.current = next;
          setPage(next);
        } else {
          const old = current.current;
          const merged = [...(old?.logs ?? []), ...(next.logs ?? [])];
          const logs = [...new Map(merged.map((row) => [row.seq, row])).values()]
            .sort((a, b) => a.seq - b.seq)
            .slice(-400);
          const value = {
            ...next,
            logs,
            before: logs[0]?.seq ?? next.before,
            more_before: old?.logs?.length
              ? old.more_before || logs[0]?.seq !== merged[0]?.seq
              : next.more_before,
          };
          current.current = value;
          setPage(value);
        }
        setError("");
        if (live) timer = setTimeout(() => void poll(false), next.more_after ? 0 : 2000);
      } catch (e) {
        if (!abort.signal.aborted) {
          setError(String(e));
          if (live) timer = setTimeout(() => void poll(first), 3000);
        }
      }
    };
    const first = loadedKey.current !== requestKey;
    if (first || live) {
      void poll(first);
    }
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [client, cursor, id, live, retry]);
  const previous = () => {
    if (!page) return;
    setLive(false);
    setCursor(`before=${page.before}`);
  };
  const next = () => {
    if (!page) return;
    setLive(false);
    setCursor(`after=${page.after}`);
  };
  return (
    <>
      <div className={styles.toolbar}>
        <button
          onClick={() => {
            setCursor("");
            setLive(true);
            setRetry((x) => x + 1);
          }}
        >
          最新 · Live
        </button>
        <button aria-pressed={!live} onClick={() => setLive((x) => !x)}>
          {live ? "暂停跟随" : "恢复跟随"}
        </button>
        <button onClick={() => setRetry((x) => x + 1)}>重新连接</button>
        <button aria-expanded={usage} onClick={() => setUsage((x) => !x)}>
          本次用量
        </button>
      </div>
      <small>
        {id} · 正式执行状态 {page?.observed ?? "正在读取"} · {live ? "实时跟随" : "历史阅读"}
      </small>
      {usage && <UsagePanel subject={`run:${id}`} />}
      {error && (
        <p role="alert">
          记录暂时无法更新：{error}。{live ? "保留当前内容并重试。" : "可重新连接。"}
        </p>
      )}
      <p className={styles.notice}>
        仅显示 Tend
        已采集的持久记录。早期及不支持的执行器可能缺少过程；截断不代表原始输出完整。历史工具和正文是执行证据。
      </p>
      <div className={styles.toolbar}>
        <button disabled={!page?.more_before} onClick={previous}>
          更早记录
        </button>
        <button disabled={!page?.more_after} onClick={next}>
          较新记录
        </button>
      </div>
      {!page ? (
        <p>正在读取过程…</p>
      ) : !page.logs?.length ? (
        <p>尚无已采集记录。</p>
      ) : (
        <ol className={styles.events}>
          {(page.logs ?? []).map((r, index) => (
            <li key={r.seq}>
              {index > 0 && r.seq > (page.logs ?? [])[index - 1]!.seq + 1 && (
                <p role="status">序号间存在缺口；此处没有完整事件证据。</p>
              )}
              <EventRecord row={r} />
            </li>
          ))}
        </ol>
      )}
      {page && (
        <small>
          窗口 {page.before}–{page.after} · 持久水位 {page.watermark} · 最多保留最近 400
          条，可分页读取更早记录
        </small>
      )}
    </>
  );
}
function object(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}
export function EventRecord({ row }: { row: RunLogView }) {
  const envelope = object(row.body);
  const data = object(envelope.data ?? row.body);
  const type = typeof data.type === "string" ? data.type : row.kind;
  const text = typeof data.text === "string" ? data.text : "";
  const command = typeof data.command === "string" ? data.command : "";
  const status = typeof data.status === "string" ? data.status : "";
  const title =
    (
      {
        agentMessage: "Agent 回复",
        commandExecution: "执行命令",
        mcpToolCall: "调用工具",
        webSearch: "搜索",
        collabAgentToolCall: "子 Agent",
        contextCompaction: "上下文整理",
      } as Record<string, string>
    )[type] ?? type;
  return (
    <article>
      <header>
        <strong>{title}</strong>
        {status && <span>{status}</span>}
        <small>
          #{row.seq} · {new Date(row.created_at).toLocaleTimeString()}
        </small>
      </header>
      {envelope.thread_id !== undefined && (
        <small>
          Thread {String(envelope.thread_id)} · Turn {String(envelope.turn_id ?? "")}
        </small>
      )}
      {text && <Markdown text={text} />}
      {command && <pre>{command}</pre>}
      {data.truncated === true && <p role="status">内容已截断，原始完整输出未被采集。</p>}
      <details>
        <summary>原始记录 · {row.native_id || "无原生 ID"}</summary>
        <pre>{JSON.stringify(row.body, null, 2)}</pre>
      </details>
    </article>
  );
}
