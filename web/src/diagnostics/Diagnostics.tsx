import { useEffect, useRef, useState } from "react";
import type {
  BackgroundSourcePage,
  TriggerEvent,
  TriggerEventPage,
  TriggerSchedulePage,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { back, Link, navigate, type DiagnosticFilters } from "../app/router.ts";
import { Badge, Button, EmptyState, Input, Select } from "../ui/index.ts";
import {
  labels,
  tone,
  timeLabel,
  scheduleLabel,
  reasonLabel,
  issueStateLabel,
  eventSummary,
} from "./model.ts";
import styles from "./Diagnostics.module.css";

export function Diagnostics({
  filters,
  eventID,
}: {
  filters: DiagnosticFilters;
  eventID?: string | undefined;
}) {
  const { client, live } = useServices();
  const tab = eventID ? "events" : (filters.tab ?? "schedules");
  const [list, setList] = useState<{
    key: string;
    data: TriggerEventPage | TriggerSchedulePage;
  } | null>(null);
  const [selectedID, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<TriggerEvent | null>(null);
  const [error, setError] = useState("");
  const query = new URLSearchParams({
    issue_id: filters.issue ?? "",
    source: filters.source ?? "",
    status: filters.status ?? "",
    search: filters.search ?? "",
    after: filters.after ?? "",
    limit: "50",
    since: filters.since ?? "0",
  }).toString();
  const key = `${tab}?${query}`;
  useEffect(() => {
    let stopped = false;
    let pending: AbortController | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function load() {
      pending?.abort();
      const abort = new AbortController();
      pending = abort;
      try {
        const [data, event] = await Promise.all([
          tab === "events"
            ? client.get<TriggerEventPage>(`/api/v1/diagnostics/events?${query}`, abort.signal)
            : client.get<TriggerSchedulePage>(
                `/api/v1/diagnostics/schedules?${query}`,
                abort.signal,
              ),
          eventID
            ? client.get<TriggerEvent>(
                `/api/v1/diagnostics/events/${encodeURIComponent(eventID)}`,
                abort.signal,
              )
            : Promise.resolve(null),
        ]);
        if (!stopped && !abort.signal.aborted) {
          setList({ key, data });
          setDetail(event);
          setError("");
        }
      } catch (e) {
        if (!stopped && !abort.signal.aborted)
          setError(e instanceof Error ? e.message : "读取失败");
      }
    }
    const refresh = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void load(), 120);
    };
    void load();
    const stopEvents = live.subscribeEvents(refresh);
    const stopReload = live.subscribeReload(refresh);
    const visible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("online", refresh);
    return () => {
      stopped = true;
      pending?.abort();
      clearTimeout(timer);
      stopEvents();
      stopReload();
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("online", refresh);
    };
  }, [client, live, tab, query, key, eventID]);
  const page = list?.key === key ? list.data : null;
  const selected =
    tab === "schedules"
      ? ((page as TriggerSchedulePage | null)?.items ?? []).find((r) => r.id === selectedID)
      : undefined;
  const update = (patch: Partial<DiagnosticFilters>, replace = false) =>
    navigate({ ...filters, after: "", ...patch, page: "diagnostics" }, { replace });
  const openEvent = (id: string) =>
    navigate({ ...filters, tab: "events", page: "diagnosticEvent", id });
  const closeEvent = () => back({ ...filters, tab: "events", page: "diagnostics" });
  const selectedEvent = detail?.id === eventID ? detail : null;
  return (
    <section
      className={styles.page}
      data-event={eventID ? "open" : undefined}
      aria-label="调度与事件诊断"
    >
      <header className={styles.header}>
        <div>
          {filters.issue && (
            <Link to={{ page: "issue", id: filters.issue }} className={styles.back}>
              ‹ 返回 Issue
            </Link>
          )}
          <h1>诊断</h1>
          <p>查看触发、分发与处理的完整记录</p>
        </div>
        <span className={styles.muted}>
          {page ? `数据截至 ${timeLabel(page.data_at)}` : "正在读取账本…"}
        </span>
      </header>
      <nav className={styles.tabs} aria-label="诊断视图">
        <button
          type="button"
          aria-pressed={tab === "schedules"}
          onClick={() => update({ tab: "schedules", status: "" })}
        >
          调度列表
        </button>
        <button
          type="button"
          aria-pressed={tab === "events"}
          onClick={() => update({ tab: "events", status: "" })}
        >
          事件记录
        </button>
      </nav>
      <div className={styles.filters}>
        <Input
          aria-label="搜索记录"
          placeholder="搜索名称、Issue 或关联 ID"
          value={filters.search ?? ""}
          onChange={(e) => update({ search: e.target.value }, true)}
        />
        <Select
          aria-label="来源"
          value={filters.source ?? ""}
          onChange={(e) => update({ source: e.target.value })}
        >
          <option value="">全部来源</option>
          <option value="followup">定时与条件跟进</option>
          <option value="observation">交付观察</option>
          <option value="webhook">Webhook</option>
        </Select>
        <Select
          aria-label="状态"
          value={filters.status ?? ""}
          onChange={(e) => update({ status: e.target.value })}
        >
          <option value="">全部状态</option>
          {(tab === "events"
            ? [
                "pending",
                "queued",
                "processing",
                "completed",
                "not_dispatched",
                "failed",
                "unknown",
                "cancelled",
              ]
            : ["active", "pending", "processing", "paused", "finished", "cancelled"]
          ).map((s) => (
            <option key={s} value={s}>
              {labels[s]}
            </option>
          ))}
        </Select>
        {tab === "events" && (
          <Select
            aria-label="时间范围"
            value={filters.since ? "bounded" : "all"}
            onChange={(e) =>
              update({
                since:
                  e.target.value === "all"
                    ? ""
                    : String(Date.now() - (e.target.value === "week" ? 7 : 1) * 86400000),
              })
            }
          >
            <option value="all">全部时间</option>
            {filters.since && <option value="bounded">所选时间起</option>}
            <option value="day">最近 24 小时</option>
            <option value="week">最近 7 天</option>
          </Select>
        )}
        {filters.issue && <span className={styles.scope}>仅显示此 Issue</span>}
      </div>
      {error && (
        <p role="alert" className={styles.error}>
          读取失败：{error}。保留已有记录，连接恢复后重新核对。
        </p>
      )}
      <div className={styles.workspace}>
        <div className={styles.list}>
          {page && (page.items ?? []).length === 0 && (
            <EmptyState>没有匹配的记录。调整筛选，或从 Issue 查看已登记的安排。</EmptyState>
          )}
          {tab === "schedules" && page && (
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>安排 / Issue</th>
                  <th>来源</th>
                  <th>调度状态</th>
                  <th>下次检查</th>
                  <th>最近结果</th>
                </tr>
              </thead>
              <tbody>
                {((page as TriggerSchedulePage).items ?? []).map((row) => (
                  <tr key={row.id} data-selected={selected?.id === row.id || undefined}>
                    <td>
                      <button
                        className={styles.rowTitle}
                        type="button"
                        onClick={() => setSelected(row.id)}
                      >
                        {row.title || row.issue_title}
                      </button>
                      <Link to={{ page: "issue", id: row.issue_id }}>{row.issue_title}</Link>
                    </td>
                    <td data-label="来源">{scheduleLabel(row)}</td>
                    <td data-label="调度状态">
                      <Badge tone={tone(row.status)}>{labels[row.status] ?? row.status}</Badge>
                    </td>
                    <td data-label="下次检查">
                      {row.status !== "active" && row.status !== "pending"
                        ? "—"
                        : row.kind === "condition"
                          ? "事实变化时"
                          : row.next_at
                            ? timeLabel(row.next_at)
                            : "—"}
                      {row.status === "active" &&
                        (row.next_at ?? 0) > 0 &&
                        (row.next_at ?? 0) < page.data_at && (
                          <small className={styles.error}>已到期，等待扫描</small>
                        )}
                    </td>
                    <td data-label="最近结果">{row.reason ? reasonLabel(row.reason) : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {tab === "events" && page && (
            <div className={styles.events} role="list">
              {((page as TriggerEventPage).items ?? []).map((row) => (
                <button
                  type="button"
                  role="listitem"
                  className={styles.eventRow}
                  data-selected={row.id === eventID || undefined}
                  key={row.id}
                  onClick={() => openEvent(row.id)}
                >
                  <time>{timeLabel(row.created_at)}</time>
                  <span>
                    <strong>{row.title}</strong>
                    <small>
                      {row.issue_title} · {labels[row.source] ?? row.source}
                    </small>
                    <small>{eventSummary(row)}</small>
                  </span>
                  <Badge tone={tone(row.status)}>{labels[row.status] ?? row.status}</Badge>
                </button>
              ))}
            </div>
          )}
          {page && (
            <footer className={styles.pagination}>
              <span>
                {(page.items ?? []).length} 条记录{page.more ? " · 还有更多" : ""}
              </span>
              {filters.after && (
                <Button variant="link" onClick={() => update({ after: "" })}>
                  返回第一页
                </Button>
              )}
              {page.more && (
                <Button onClick={() => update({ after: page.next ?? "" })}>下一页 →</Button>
              )}
            </footer>
          )}
          {tab === "schedules" && selected && (
            <section className={styles.scheduleDetail}>
              <h2>{selected.title}</h2>
              <dl>
                <dt>所属 Issue</dt>
                <dd>
                  <Link to={{ page: "issue", id: selected.issue_id }}>{selected.issue_title}</Link>
                </dd>
                <dt>调度类型</dt>
                <dd>{scheduleLabel(selected)}</dd>
                <dt>最近说明</dt>
                <dd>{reasonLabel(selected.reason)}</dd>
                {selected.rule && (
                  <>
                    <dt>检查说明</dt>
                    <dd>{selected.rule.instruction}</dd>
                    <dt>次数 / 上限</dt>
                    <dd>
                      {selected.rule.trigger_count} / {selected.rule.max_triggers}
                    </dd>
                    <dt>有效期</dt>
                    <dd>{timeLabel(selected.rule.expires_at)}</dd>
                  </>
                )}
              </dl>
              <Button variant="link" onClick={() => update({ tab: "events", search: selected.id })}>
                查看关联事件 →
              </Button>
            </section>
          )}
        </div>
        {eventID && (
          <aside className={styles.inspector} aria-label="事件详情">
            <Button variant="link" onClick={closeEvent}>
              ‹ 返回事件记录
            </Button>
            {selectedEvent ? (
              <EventDetails key={selectedEvent.id} event={selectedEvent} />
            ) : (
              <p>{error ? "事件详情未能读取" : "正在读取事件…"}</p>
            )}
          </aside>
        )}
      </div>
    </section>
  );
}
function EventDetails({ event }: { event: TriggerEvent }) {
  const [copyStatus, setCopyStatus] = useState("");
  const { client } = useServices();
  const [sources, setSources] = useState<BackgroundSourcePage | null>(null);
  const [sourceStatus, setSourceStatus] = useState("");
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => pending.current?.abort(), []);
  const readSources = async () => {
    pending.current?.abort();
    const abort = new AbortController();
    pending.current = abort;
    setSourceStatus("读取中…");
    const after = sources?.more ? (sources.next ?? 0) : 0;
    try {
      const page = await client.get<BackgroundSourcePage>(
        `/api/v1/diagnostics/events/${encodeURIComponent(event.id)}/sources?after=${after}`,
        abort.signal,
      );
      if (abort.signal.aborted) return;
      setSources((old) => ({
        ...page,
        sources: [...(after ? (old?.sources ?? []) : []), ...(page.sources ?? [])],
      }));
      setSourceStatus("");
    } catch {
      if (!abort.signal.aborted) setSourceStatus("来源读取失败，可以重试。");
    }
  };

  return (
    <>
      <h2>{event.title}</h2>
      <p>
        <Link to={{ page: "issue", id: event.issue_id }}>{event.issue_title}</Link> ·{" "}
        {labels[event.source]}
      </p>
      <div className={styles.statuses}>
        <Badge tone={tone(event.status)}>{labels[event.status] ?? event.status}</Badge>
        <span className={styles.muted}>Issue：{issueStateLabel(event.issue_state)}</span>
      </div>
      <ol className={styles.timeline}>
        {event.planned_at && event.planned_at <= event.created_at ? (
          <li>
            <time>{timeLabel(event.planned_at)}</time>
            <div>
              <strong>规则到期</strong>
              <p>已登记的计划时间</p>
            </div>
          </li>
        ) : null}
        {(event.steps ?? []).map((step, i) => (
          <li key={`${i}-${step.kind}`}>
            <time>{timeLabel(step.at)}</time>
            <div>
              <strong>{labels[step.kind] ?? step.kind}</strong>
              {step.detail && <p>{reasonLabel(step.detail)}</p>}
            </div>
          </li>
        ))}
      </ol>
      <section className={styles.result}>
        <h3>处理说明</h3>
        <p>
          {event.status === "pending"
            ? eventSummary(event)
            : event.reason
              ? reasonLabel(event.reason)
              : "尚未记录处理结果。事件处理与 Issue 收尾分别核对。"}
        </p>
      </section>
      <details open>
        <summary>来源与证据</summary>
        {event.batch_id && (
          <>
            <Button onClick={() => void readSources()} disabled={sourceStatus === "读取中…"}>
              {sources ? (sources.more ? "读取更多来源" : "重新读取来源") : "读取批次来源"}
            </Button>
            <p role="status">{sourceStatus}</p>
          </>
        )}
        {sources && (
          <>
            <p>本次读取：批次保留 {sources.batch.source_count} 条有效来源，可逐条核对原始输入。</p>
            {(sources.sources ?? []).map((source) => (
              <details key={source.seq}>
                <summary>
                  {labels[source.kind] ?? source.kind}
                  {source.cancelled_at ? " · 已取消" : ""}
                </summary>
                <pre className={styles.sourceBody}>{source.body}</pre>
              </details>
            ))}
          </>
        )}
      </details>
      <details>
        <summary>关联记录</summary>
        <dl className={styles.ids}>
          {[
            ["事件", event.id],
            ["安排", event.schedule_id],
            ["批次", event.batch_id],
            ["请求", event.request_id],
            ["来源消息", event.message_id],
          ]
            .filter(([, v]) => v)
            .map(([label, value]) => (
              <div key={label}>
                <dt>{label}</dt>
                <dd>
                  <code>{value}</code>
                  <Button
                    variant="link"
                    aria-label={`复制${label} ID`}
                    onClick={() => {
                      void navigator.clipboard.writeText(value!).then(
                        () => setCopyStatus("已复制"),
                        () => setCopyStatus("复制失败，可选择 ID 手动复制"),
                      );
                    }}
                  >
                    复制
                  </Button>
                </dd>
              </div>
            ))}
        </dl>
        <p role="status">{copyStatus}</p>
        <p className={styles.muted}>已保留记录的时间点按实际事实展示，未记录的历史阶段不会补造。</p>
      </details>
      <Link to={{ page: "issue", id: event.issue_id }}>打开 Issue →</Link>
    </>
  );
}
