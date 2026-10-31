import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import type { TokenUsageTotals, TokenUsageView } from "../api/generated/api.ts";
import { navigate } from "../app/router.ts";
import { ResourceRow } from "../shell/ResourceRow.tsx";
import styles from "./Usage.module.css";

const windows = [
  ["total", "全部历史"],
  ["24h", "最近 24 小时"],
  ["7d", "最近 7 天"],
  ["30d", "最近 30 天"],
  ["day", "自然日"],
  ["week", "自然周"],
  ["month", "自然月"],
] as const;
const number = new Intl.NumberFormat("zh-CN");
const roles: Record<string, string> = {
  main: "主 Agent",
  background: "后台 Agent",
  implement: "实施",
  accept: "验收",
  subagent: "子 Agent",
};
export function field(value: number, known: number, requests: number): string {
  if (!known) return "未提供";
  return `${number.format(value)}${known < requests ? `（部分，${known}/${requests} 个请求）` : ""}`;
}
export function TokenLine({ totals: t }: { totals: TokenUsageTotals }) {
  return (
    <div className={styles.line}>
      <span>
        输入 token <b>{field(t.input, t.input_known, t.requests)}</b>
        <small>
          缓存读取 {field(t.cache_read, t.cache_read_known, t.requests)}
          {t.cache_write_known > 0 &&
            ` · 缓存写入 ${field(t.cache_write, t.cache_write_known, t.requests)}`}
        </small>
      </span>
      <span>
        输出 token <b>{field(t.output, t.output_known, t.requests)}</b>
      </span>
    </div>
  );
}

export function UsageSummary({
  totals: t,
  title,
  historical,
}: {
  totals: TokenUsageTotals;
  title: string;
  historical: boolean;
}) {
  return (
    <section className={styles.primary} aria-label={title}>
      <h3>{title} · 已知用量</h3>
      {t.requests ? (
        <>
          <div className={styles.hero}>
            <strong>{t.total_known ? number.format(t.total) : "—"}</strong>
            {t.total_known > 0 && <span>token</span>}
            {t.total_known > 0 && t.total_known < t.requests && (
              <span className={styles.badge}>部分</span>
            )}
          </div>
          <p className={styles.caption}>
            {t.total_known}/{t.requests} 个请求提供总量
            {!t.total_known && " · 这些请求未提供总量"}
            {" · "}
            {t.final} 个请求已结算
          </p>
          <TokenLine totals={t} />
        </>
      ) : (
        <>
          <p className={styles.emptyTitle}>
            {historical ? "当前筛选下暂无已采集请求" : "此时段暂无可归属请求"}
          </p>
          <p className={styles.caption}>暂无可展示的已知用量，不代表实际消费为零。</p>
        </>
      )}
    </section>
  );
}

export function UsageEntry() {
  return <ResourceRow onOpen={() => navigate({ page: "usage" })}>Token 用量</ResourceRow>;
}

export function UsagePanel({ subject = "" }: { subject?: string }) {
  const { client } = useServices();
  const [window, setWindow] = useState("total");
  const [offset, setOffset] = useState(0);
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [before, setBefore] = useState(0);
  const [result, setResult] = useState<{ key: string; data: TokenUsageView } | null>(null);
  const [status, setStatus] = useState<{ key: string; loading: boolean; failed: boolean } | null>(
    null,
  );
  const [retry, setRetry] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const announcedRetry = useRef(0);
  const refresh = () => {
    setAnnouncement("");
    setRetry((x) => x + 1);
  };
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
      setStatus((old) => ({ key, loading: true, failed: old?.key === key && old.failed }));
      try {
        const data = await client.get<TokenUsageView>(`/api/v1/token-usage?${key}`, abort.signal);
        if (abort.signal.aborted || generation !== latest.current) return;
        setResult((old) =>
          old?.key === key && old.data.revision > data.revision ? old : { key, data },
        );
        // Cache names for the picker only. Numeric snapshots are never combined.
        setModels((old) =>
          [...new Set([...old, ...(data.models ?? []).map((m) => m.name)])].sort(),
        );
        setStatus({ key, loading: false, failed: false });
        if (retry > announcedRetry.current) {
          announcedRetry.current = retry;
          setAnnouncement("用量已更新");
        }
      } catch {
        if (abort.signal.aborted || generation !== latest.current) return;
        announcedRetry.current = retry;
        setStatus({ key, loading: false, failed: true });
      }
      if (!abort.signal.aborted) timer = setTimeout(() => void load(), 10_000);
    };
    void load();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [client, key, retry]);
  const data = result?.key === key ? result.data : null;
  const failed = status?.key === key && status.failed;
  const loading = status?.key !== key || status.loading;
  const calendar = ["day", "week", "month"].includes(window);
  const historical = window === "total";
  const time = (value: number) =>
    new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" });
  const changeWindow = (value: string) => {
    setWindow(value);
    setOffset(0);
    setBefore(0);
  };
  const changeModel = (value: string) => {
    setModel(value);
    setBefore(0);
  };
  const rangeTitle = subject
    ? "本次执行 · 全部已采集历史"
    : historical
      ? "全部已采集历史"
      : `所选时段 · ${windows.find(([value]) => value === window)?.[1] ?? window}`;
  const trendMax = Math.max(
    1,
    ...(data?.trend ?? []).filter((b) => b.total_known > 0).map((b) => b.total),
  );
  return (
    <section className={styles.panel} aria-label={subject ? "本次执行 token 用量" : "Token 用量"}>
      <header>
        {subject && <h2>本次执行用量</h2>}
        <small>{subject ? "本次执行 · " : ""}已采集的请求消费 · Asia/Shanghai</small>
      </header>
      <div className={styles.filters}>
        {!subject && (
          <label>
            范围
            <select value={window} onChange={(e) => changeWindow(e.target.value)}>
              {windows.map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        )}
        <label>
          实际模型
          <select value={model} onChange={(e) => changeModel(e.target.value)}>
            <option value="">全部模型</option>
            {[...new Set([...models, ...(model ? [model] : [])])].map((m) => (
              <option key={m}>{m}</option>
            ))}
          </select>
        </label>
        {calendar && (
          <div className={styles.period}>
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
          </div>
        )}
        <button disabled={loading} onClick={refresh}>
          {loading ? "更新中…" : "刷新"}
        </button>
      </div>
      {failed && (
        <div className={styles.notice} role="alert">
          <p>
            {data
              ? `更新失败，显示上次读数（截至 ${time(data.as_of)}）。`
              : "暂时无法读取此范围用量。"}
            将自动重试。
          </p>
          <button disabled={loading} onClick={refresh}>
            重新读取
          </button>
        </div>
      )}
      <span className={styles.srOnly} role="status">
        {announcement}
      </span>
      {!data ? (
        !failed && <p role="status">正在读取用量…</p>
      ) : (
        <>
          <p className={styles.range}>
            {data.start === null ? "全部已采集历史" : `${time(data.start)} 至 ${time(data.end)}`}
            {" · 数据截至 "}
            {time(data.as_of)}
          </p>
          <div className={styles.summaries}>
            <UsageSummary totals={data.totals} title={rangeTitle} historical={historical} />
            {data.unattributed.requests > 0 && (
              <section className={styles.unknown} aria-label="时间未知用量">
                <h3>{historical ? "其中时间未知" : "另有时间未知"}</h3>
                <div className={styles.unknownValue}>
                  <strong>
                    {data.unattributed.total_known ? number.format(data.unattributed.total) : "—"}
                  </strong>
                  {data.unattributed.total_known > 0 && <span>token</span>}
                </div>
                <p>
                  {data.unattributed.total_known}/{data.unattributed.requests} 个请求提供总量
                </p>
                <p>
                  {model ? "当前模型下 · " : ""}全部已采集历史；
                  {historical ? "已计入主摘要，不能再次相加。" : "未计入此时段，无法归入时间分布。"}
                </p>
                <details>
                  <summary>查看已知字段与时间说明</summary>
                  <TokenLine totals={data.unattributed} />
                  <p>请求未提供发生时间。采集时间仅供同步参考，不代替发生时间。</p>
                </details>
                {!historical && <button onClick={() => changeWindow("total")}>查看全部历史</button>}
              </section>
            )}
          </div>
          {!historical && !data.totals.requests && !data.unattributed.requests && (
            <button onClick={() => changeWindow("total")}>查看全部历史</button>
          )}
          {data.conflicts > 0 && (
            <p className={styles.notice} role="status">
              {data.conflicts} 个请求存在来源冲突，未计入已知用量，等待核对。
            </p>
          )}
          <section className={styles.block} aria-label="时间分布">
            <div className={styles.sectionHead}>
              <h3>时间分布</h3>
              <small>仅含提供发生时间的请求</small>
            </div>
            {!data.trend?.length ? (
              <p className={styles.caption}>本范围没有提供发生时间的请求，趋势暂不可用。</p>
            ) : !data.trend.some((b) => b.total_known > 0) ? (
              <p className={styles.caption}>这些请求未提供总量，暂不能绘制 token 趋势。</p>
            ) : (
              <div className={styles.trend}>
                {data.trend.map((bucket) => (
                  <div key={bucket.start} className={styles.bucket}>
                    <small>
                      {time(bucket.start)} – {time(bucket.end)}
                    </small>
                    {bucket.total_known > 0 ? (
                      <meter
                        min={0}
                        max={trendMax}
                        value={bucket.total}
                        aria-label={`${field(bucket.total, bucket.total_known, bucket.requests)} token`}
                      />
                    ) : (
                      <span>总量未提供</span>
                    )}
                    <span>{field(bucket.total, bucket.total_known, bucket.requests)} token</span>
                  </div>
                ))}
              </div>
            )}
          </section>
          {((data.models?.length ?? 0) > 0 || (data.roles?.length ?? 0) > 0) && (
            <section className={styles.block} aria-label="用量分布">
              <h3>用量分布</h3>
              <div className={styles.groups}>
                {(data.models?.length ?? 0) > 0 && (
                  <section>
                    <h4>实际模型</h4>
                    {data.models!.map((m) => (
                      <div className={styles.row} key={m.name}>
                        <button className={styles.groupName} onClick={() => changeModel(m.name)}>
                          {m.name}
                        </button>
                        <span>{field(m.total, m.total_known, m.requests)} token</span>
                        <small>
                          {m.total_known}/{m.requests} 个请求提供总量
                        </small>
                        <details>
                          <summary>输入、输出与缓存</summary>
                          <TokenLine totals={m} />
                        </details>
                      </div>
                    ))}
                  </section>
                )}
                {(data.roles?.length ?? 0) > 0 && (
                  <section>
                    <h4>来源</h4>
                    {data.roles!.map((r) => (
                      <div className={styles.row} key={r.name}>
                        <span>{roles[r.name] ?? r.name}</span>
                        <span>{field(r.total, r.total_known, r.requests)} token</span>
                        <small>
                          {r.total_known}/{r.requests} 个请求提供总量
                        </small>
                        <details>
                          <summary>输入、输出与缓存</summary>
                          <TokenLine totals={r} />
                        </details>
                      </div>
                    ))}
                  </section>
                )}
              </div>
            </section>
          )}
          <section className={styles.block} aria-label="请求明细">
            <div className={styles.sectionHead}>
              <h3>请求明细</h3>
              <small>
                共 {data.totals.requests} 个请求 · 本页 {data.records?.length ?? 0} 条
              </small>
            </div>
            {!data.records?.length && <p className={styles.caption}>当前范围没有请求明细。</p>}
            {(data.records ?? []).map((r) => (
              <details className={styles.record} key={r.seq}>
                <summary>
                  <span className={styles.recordTime}>
                    {r.occurred_at === null ? "发生时间未提供" : time(r.occurred_at)}
                  </span>
                  <span className={styles.recordModel}>{r.model || "实际模型未提供"}</span>
                  <strong>
                    {r.total === null ? "总量未提供" : `${number.format(r.total)} token`}
                  </strong>
                  <span className={styles.settlement}>
                    {r.settlement === "final" ? "请求已结算" : "实时读数"}
                  </span>
                </summary>
                <div className={styles.recordBody}>
                  <p>
                    所属 {r.subject} · 来源 {roles[r.role] ?? r.role}
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
                    发生时间 {r.occurred_at === null ? "未提供" : time(r.occurred_at)} · 采集时间{" "}
                    {time(r.received_at)} · {r.profile}
                  </small>
                </div>
              </details>
            ))}
            <div className={styles.pagination}>
              {before > 0 && <button onClick={() => setBefore(0)}>最新请求</button>}
              {data.more && <button onClick={() => setBefore(data.next)}>更早请求</button>}
            </div>
          </section>
          <details className={styles.coverage}>
            <summary>仅已采集请求 · 查看计量与覆盖范围</summary>
            <p>{data.coverage}</p>
            <p>
              缓存读取和缓存写入是输入明细，不再加到总量。缺失字段显示未提供；上下文占用和费用不参与消费统计。请求结算不代表整个执行的用量已完整结算。
            </p>
            <small>
              水位 {data.revision} · 数据截止 {time(data.as_of)}
            </small>
          </details>
        </>
      )}
    </section>
  );
}
