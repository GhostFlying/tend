import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import type { TokenUsageTotals, TokenUsageView } from "../api/generated/api.ts";
import { DetailOverlay, DetailPane } from "../shell/DetailOverlay.tsx";
import styles from "./Usage.module.css";

const windows = [
  ["day", "自然日"],
  ["week", "自然周"],
  ["month", "自然月"],
  ["24h", "最近 24h"],
  ["7d", "最近 7d"],
  ["30d", "最近 30d"],
  ["total", "总共"],
] as const;
const number = new Intl.NumberFormat("zh-CN");
export function field(value: number, known: number, requests: number): string {
  if (!known) return "未提供";
  return `${number.format(value)}${known < requests ? "（部分）" : ""}`;
}
export function TokenLine({ totals: t }: { totals: TokenUsageTotals }) {
  return (
    <div className={styles.line}>
      <span>
        输入 token <b>{field(t.input, t.input_known, t.requests)}</b>
        <small>
          （缓存读取 {field(t.cache_read, t.cache_read_known, t.requests)}
          {t.cache_write_known > 0 &&
            `，缓存写入 ${field(t.cache_write, t.cache_write_known, t.requests)}`}
          ）
        </small>
      </span>
      <span>
        输出 token <b>{field(t.output, t.output_known, t.requests)}</b>
      </span>
    </div>
  );
}

export function UsageEntry() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Token 用量 ›
      </button>
      {open && (
        <DetailOverlay task={false} onClose={() => setOpen(false)}>
          <DetailPane active>
            <UsagePanel />
          </DetailPane>
        </DetailOverlay>
      )}
    </>
  );
}

export function UsagePanel({ subject = "" }: { subject?: string }) {
  const { client } = useServices();
  const [window, setWindow] = useState(subject ? "total" : "7d");
  const [offset, setOffset] = useState(0);
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [before, setBefore] = useState(0);
  const [result, setResult] = useState<{ key: string; data: TokenUsageView } | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const key = new URLSearchParams({
    window,
    offset: String(offset),
    model,
    subject,
    before: String(before),
  }).toString();
  const latest = useRef(0);
  useEffect(() => {
    const abort = new AbortController();
    const generation = ++latest.current;
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const data = await client.get<TokenUsageView>(`/api/v1/token-usage?${key}`, abort.signal);
        if (abort.signal.aborted || generation !== latest.current) return;
        setResult((old) =>
          old?.key === key && old.data.revision > data.revision ? old : { key, data },
        );
        if (!model) setModels((data.models ?? []).map((m) => m.name));
        setError("");
      } catch (e) {
        if (!abort.signal.aborted) setError(String(e));
      }
      if (!abort.signal.aborted) timer = setTimeout(() => void load(), 10_000);
    };
    void load();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [client, key, model, retry]);
  const data = result?.key === key ? result.data : null;
  const calendar = ["day", "week", "month"].includes(window);
  return (
    <section className={styles.panel} aria-label={subject ? "本次执行 token 用量" : "Token 用量"}>
      <header>
        <div>
          <h2>{subject ? "本次执行用量" : "Token 用量"}</h2>
          <small>Asia/Shanghai · 已采集请求</small>
        </div>
        <button type="button" onClick={() => setRetry((x) => x + 1)}>
          刷新
        </button>
      </header>
      {!subject && (
        <nav className={`${styles.filters} ${styles.timeFilters}`} aria-label="用量时间范围">
          {windows.map(([value, label]) => (
            <button
              key={value}
              type="button"
              aria-pressed={window === value}
              onClick={() => {
                setWindow(value);
                setOffset(0);
                setBefore(0);
              }}
            >
              {label}
            </button>
          ))}
        </nav>
      )}
      <div className={styles.filters}>
        {calendar && (
          <>
            <button
              onClick={() => {
                setOffset((x) => x - 1);
                setBefore(0);
              }}
            >
              上一周期
            </button>
            <button
              disabled={offset === 0}
              onClick={() => {
                setOffset(0);
                setBefore(0);
              }}
            >
              当前周期
            </button>
          </>
        )}
        <label>
          实际模型{" "}
          <select
            value={model}
            onChange={(e) => {
              setModel(e.target.value);
              setBefore(0);
            }}
          >
            <option value="">全部模型</option>
            {models.map((m) => (
              <option key={m}>{m}</option>
            ))}
          </select>
        </label>
      </div>
      {error && <p role="alert">用量更新失败：{error}。保留上次读数，将自动重试。</p>}
      {!data ? (
        <p>正在读取用量…</p>
      ) : (
        <>
          <small>
            {data.start === null
              ? "全部采集历史"
              : new Date(data.start).toLocaleString("zh-CN", { timeZone: data.timezone })}{" "}
            至 {new Date(data.end).toLocaleString("zh-CN", { timeZone: data.timezone })}
          </small>
          <div className={styles.hero}>
            <span>已知总用量</span>
            <strong>
              {field(data.totals.total, data.totals.total_known, data.totals.requests)}
            </strong>
            <span>token</span>
          </div>
          <TokenLine totals={data.totals} />
          <p className={styles.caption}>
            {data.totals.requests} 个已识别请求 · {data.totals.total_known} 个提供总量 ·{" "}
            {data.totals.final} 个请求已结算
          </p>
          {!data.totals.requests && <p>本范围尚无可归属的请求记录；这不表示消费为零。</p>}
          {data.unattributed.requests > 0 && (
            <details className={styles.notice} open={window !== "total"}>
              <summary>发生时点未提供 · {data.unattributed.requests} 个请求</summary>
              <p>以下已知读数无法归入精确时间窗口；总共包含这些请求。采集时间仅供同步参考。</p>
              <TokenLine totals={data.unattributed} />
              <small>
                总量{" "}
                {field(
                  data.unattributed.total,
                  data.unattributed.total_known,
                  data.unattributed.requests,
                )}{" "}
                token
              </small>
            </details>
          )}
          {data.conflicts > 0 && (
            <p role="status">{data.conflicts} 个请求存在来源冲突，未计入已知用量，等待核对。</p>
          )}
          <div className={styles.groups}>
            <section>
              <h3>实际模型</h3>
              {(data.models ?? []).map((m) => (
                <div className={styles.row} key={m.name}>
                  <button
                    onClick={() => {
                      setModel(m.name);
                      setBefore(0);
                    }}
                  >
                    {m.name}
                  </button>
                  <TokenLine totals={m} />
                  <small>
                    {m.requests} 个请求 · 总量 {field(m.total, m.total_known, m.requests)}
                  </small>
                </div>
              ))}
            </section>
            <section>
              <h3>来源</h3>
              {(data.roles ?? []).map((r) => (
                <div className={styles.row} key={r.name}>
                  <strong>
                    {{
                      main: "主 Agent",
                      background: "后台 Agent",
                      implement: "实施",
                      accept: "验收",
                      subagent: "子 Agent",
                    }[r.name] ?? r.name}
                  </strong>
                  <TokenLine totals={r} />
                </div>
              ))}
            </section>
          </div>
          <details>
            <summary>计量与覆盖范围</summary>
            <p>{data.coverage}</p>
            <p>
              缓存读取和缓存写入是输入明细，不再加到总量。缺失字段显示未提供；上下文占用和费用不参与消费统计。请求结算不代表整个执行的用量已完整结算。
            </p>
            <small>
              水位 {data.revision} · 数据截止{" "}
              {new Date(data.as_of).toLocaleString("zh-CN", { timeZone: data.timezone })}
            </small>
          </details>
          <h3>按发生时间</h3>
          {!data.trend?.length ? (
            <p>本范围没有提供发生时点的请求，趋势暂不可用。</p>
          ) : (
            <div className={styles.trend} aria-label="按发生时间的已知 token">
              {data.trend.map((bucket) => (
                <div key={bucket.start} className={styles.row}>
                  <small>
                    {new Date(bucket.start).toLocaleString("zh-CN", { timeZone: data.timezone })} –{" "}
                    {new Date(bucket.end).toLocaleString("zh-CN", { timeZone: data.timezone })}
                  </small>
                  <meter
                    min={0}
                    max={Math.max(1, ...data.trend!.map((b) => b.total))}
                    value={bucket.total}
                    aria-label={`${bucket.total} token`}
                  />
                  <span>{field(bucket.total, bucket.total_known, bucket.requests)} token</span>
                </div>
              ))}
            </div>
          )}
          <h3>请求明细</h3>
          {(data.records ?? []).map((r) => (
            <details className={styles.row} key={r.seq}>
              <summary>
                {r.model || "实际模型未提供"} ·{" "}
                {r.settlement === "final" ? "请求已结算" : "实时读数"} ·{" "}
                {r.total === null ? "总量未提供" : `${number.format(r.total)} token`}
              </summary>
              <p>
                {r.subject} · {r.role}
              </p>
              <p>
                原生请求 {r.request_id} · thread {r.thread_id} · turn {r.turn_id}
              </p>
              <p>
                输入 token {r.input ?? "未提供"}（缓存读取 {r.cache_read ?? "未提供"}
                {r.cache_write !== null && `，缓存写入 ${r.cache_write}`}），输出 token{" "}
                {r.output ?? "未提供"}
              </p>
              <small>
                发生时间 {r.occurred_at === null ? "未提供" : new Date(r.occurred_at).toISOString()}{" "}
                · 采集 {new Date(r.received_at).toISOString()} · {r.profile}
              </small>
            </details>
          ))}
          <div className={styles.filters}>
            {before > 0 && <button onClick={() => setBefore(0)}>最新请求</button>}
            {data.more && <button onClick={() => setBefore(data.next)}>更早请求</button>}
          </div>
        </>
      )}
    </section>
  );
}
