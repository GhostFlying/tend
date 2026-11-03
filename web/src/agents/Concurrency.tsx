import { useEffect, useMemo, useRef, useState } from "react";
import type {
  ConcurrencyChange,
  ConcurrencyGroup,
  ConcurrencyGroupPage,
  ConcurrencyHistoryPage,
  ConcurrencySource,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { navigate, useRoute } from "../app/router.ts";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { Button, EmptyState, Input, Select } from "../ui/index.ts";
import styles from "./Concurrency.module.css";

const reasonLabels: Record<string, string> = {
  initial: "初始预算",
  configuration: "人工修改参数",
  bind: "绑定运行方式",
  unbind: "解除绑定",
  congestion: "拥塞退避",
  minimum_cooldown: "下限冷却",
  stable_demand: "本组稳定满载，增加预算",
};
const time = (value: number) => (value ? new Date(value).toLocaleString("zh-CN") : "—");
const minutes = (value: number) => `${Math.floor(value / 60_000)} 分钟`;

function FeedbackSource({ source }: { source: ConcurrencySource }) {
  const { openIssue } = useServices();
  return (
    <div className={styles.source}>
      {source.issue_id ? (
        <Button variant="ghost" onClick={() => openIssue(source.issue_id!)}>
          {source.issue_title || source.issue_id}
        </Button>
      ) : (
        <span>
          {source.purpose === "main"
            ? "主会话，无 Issue"
            : source.purpose === "consultation"
              ? "咨询，无 Issue"
              : source.conversation_id
                ? "托管会话，无 Issue"
                : "无 Issue 关联"}
        </span>
      )}
      <dl>
        <dt>执行位置 / 方式</dt>
        <dd>
          {source.environment_id} / {source.runtime_id}
        </dd>
        <dt>Agent</dt>
        <dd>{source.agent_id}</dd>
        <dt>Tend session</dt>
        <dd>{source.session_id}</dd>
        <dt>原生 session / thread</dt>
        <dd>{source.native_session_id || "执行器未提供，或尚未创建"}</dd>
        <dt>Turn / attempt</dt>
        <dd>
          {source.turn_id || "尚未提供"} / {source.attempt}
        </dd>
        <dt>错误</dt>
        <dd>
          {source.code || "未提供分类"}
          {source.http_status ? ` · HTTP ${source.http_status}` : ""}
        </dd>
        <dt>发生 / 收到</dt>
        <dd>
          {time(source.occurred_at)} / {time(source.received_at)}
        </dd>
      </dl>
      {source.run_id && source.task_id ? (
        <Button
          variant="ghost"
          onClick={() => navigate({ page: "rollout", id: source.task_id!, run: source.run_id! })}
        >
          查看运行 session
        </Button>
      ) : source.conversation_id ? (
        <Button
          variant="ghost"
          onClick={() => navigate({ page: "conversation", id: source.conversation_id! })}
        >
          查看会话
        </Button>
      ) : null}
    </div>
  );
}

function Change({ value }: { value: ConcurrencyChange }) {
  return (
    <details className={styles.change}>
      <summary>
        <time dateTime={new Date(value.at).toISOString()}>{time(value.at)}</time>
        <strong>
          {value.before} → {value.after}
        </strong>
        <span>{reasonLabels[value.reason] || value.reason}</span>
        <span>{value.group_name}</span>
      </summary>
      {value.reason === "stable_demand" ? (
        <p>
          本组在 {time(value.window_start)} 至 {time(value.window_end)} 的有效观察中， 未收到 429 或
          overload（{value.congestion_count} 次）；满载累计 {minutes(value.saturated_ms)}， 成功完成{" "}
          {value.successful_turns} 个 turn，{value.waiting} 个执行等待容量。
          {value.feedback_healthy ? "反馈完整。" : "反馈不完整。"}
        </p>
      ) : value.sources?.length ? (
        <div>
          {value.sources?.map((source) => (
            <FeedbackSource key={source.event_id} source={source} />
          ))}
        </div>
      ) : (
        <p>
          操作人：{value.actor || "服务端"}；请求：{value.request_id || "—"}
        </p>
      )}
      {!!value.bindings?.length && (
        <p>
          关联运行方式：
          {value.bindings.map((b) => `${b.environment_id} / ${b.runtime_id}`).join("、")}
        </p>
      )}
      <p>
        当时配置：初始 {value.initial}，范围 {value.min}–{value.max}；占用 {value.occupied}。
        {value.cooldown_until ? ` 冷却至 ${time(value.cooldown_until)}。` : ""}
      </p>
    </details>
  );
}

export function ConcurrencyDirectory({ refreshVersion = 0 }: { refreshVersion?: number }) {
  const lastRefresh = useRef(refreshVersion);
  const { client } = useServices();
  const route = useRoute();
  const groupID = route.page === "agents" ? (route.group ?? "") : "";
  const [now, setNow] = useState(() => Date.now());
  const [search, setSearch] = useState("");
  const [environment, setEnvironment] = useState("");
  const [runtime, setRuntime] = useState("");
  const [issue, setIssue] = useState("");
  const [session, setSession] = useState("");
  const [reason, setReason] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [groupCursors, setGroupCursors] = useState<string[]>([]);
  const [historyCursors, setHistoryCursors] = useState<string[]>([]);
  const groups = useMemo(() => new WorkItemRead<ConcurrencyGroupPage>(client), [client]);
  const history = useMemo(() => new WorkItemRead<ConcurrencyHistoryPage>(client), [client]);
  const detail = useMemo(() => new WorkItemRead<ConcurrencyGroup>(client), [client]);
  const detailState = useStore(detail.state);
  const groupState = useStore(groups.state);
  const historyState = useStore(history.state);
  const groupQuery = new URLSearchParams({
    search,
    environment_id: environment,
    runtime_id: runtime,
    limit: "50",
    after: groupCursors.at(-1) ?? "",
  }).toString();
  const historyQuery = new URLSearchParams({
    group_id: groupID,
    issue_id: issue,
    session_id: session,
    reason,
    environment_id: environment,
    runtime_id: runtime,
    limit: "50",
    after: historyCursors.at(-1) ?? "",
    ...(since ? { since: String(new Date(since).getTime()) } : {}),
    ...(until ? { until: String(new Date(until).getTime()) } : {}),
  }).toString();
  useEffect(() => {
    void groups.open(`/api/v1/concurrency-groups?${groupQuery}`);
    return () => groups.close();
  }, [groups, groupQuery]);
  useEffect(() => {
    void history.open(`/api/v1/concurrency-history?${historyQuery}`);
    return () => history.close();
  }, [history, historyQuery]);
  useEffect(() => {
    if (groupID) void detail.open(`/api/v1/concurrency-groups/${encodeURIComponent(groupID)}`);
    else detail.close();
    return () => detail.close();
  }, [detail, groupID]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!document.hidden) {
        setNow(Date.now());
        void groups.refresh();
        void history.refresh();
        void detail.refresh();
      }
    }, 5_000);
    return () => window.clearInterval(timer);
  }, [groups, history, detail]);
  useEffect(() => {
    if (lastRefresh.current === refreshVersion) return;
    lastRefresh.current = refreshVersion;
    void groups.refresh();
    void history.refresh();
    void detail.refresh();
  }, [refreshVersion, groups, history, detail]);
  const selected = detailState.value ?? groupState.value?.items?.find((g) => g.id === groupID);
  const reset = () => {
    setGroupCursors([]);
    setHistoryCursors([]);
  };
  return (
    <section className={styles.root} aria-label="并发分组">
      <div className={styles.toolbar}>
        <Input
          aria-label="搜索并发分组"
          placeholder="搜索名称或 ID"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setGroupCursors([]);
          }}
        />
        <Input
          aria-label="执行环境筛选"
          placeholder="执行环境 ID"
          value={environment}
          onChange={(e) => {
            setEnvironment(e.target.value);
            reset();
          }}
        />
        <Input
          aria-label="运行方式筛选"
          placeholder="Runtime ID"
          value={runtime}
          onChange={(e) => {
            setRuntime(e.target.value);
            reset();
          }}
        />
        <Button
          variant="ghost"
          onClick={() => {
            void groups.refresh();
            void history.refresh();
            void detail.refresh();
          }}
        >
          重新读取
        </Button>
      </div>
      <p className={styles.muted}>
        分组由 CLI 管理。预算约束 Tend 托管执行；稳定升档要求观察 30 分钟、累计满载至少 15
        分钟，且仍有执行等待容量。
      </p>
      {groupState.error && <p role="status">无法读取分组，保留上次结果：{groupState.error}</p>}
      <div className={styles.groups}>
        {groupState.value?.items?.map((g) => (
          <button
            className={styles.group}
            key={g.id}
            aria-pressed={groupID === g.id}
            onClick={() => {
              navigate({ page: "agents", tab: "concurrency", group: groupID === g.id ? "" : g.id });
              setHistoryCursors([]);
            }}
          >
            <strong>{g.name}</strong>
            <span>{g.id}</span>
            <span>
              占用 / 有效上限{" "}
              <b>
                {g.occupied} / {g.current}
              </b>
            </span>
            <span>
              初始 {g.initial} · 最小 {g.min} · 最大 {g.max}
            </span>
            <span>{g.cooldown_until > now ? `退避至 ${time(g.cooldown_until)}` : "正常"}</span>
          </button>
        ))}
      </div>
      {!groupState.value?.items?.length && (
        <EmptyState>{groupState.loading ? "正在读取…" : "暂无匹配的并发分组。"}</EmptyState>
      )}
      <div className={styles.pager}>
        {!!groupCursors.length && (
          <Button onClick={() => setGroupCursors((v) => v.slice(0, -1))}>上一页分组</Button>
        )}
        {groupState.value?.more && (
          <Button onClick={() => setGroupCursors((v) => [...v, groupState.value!.next!])}>
            下一页分组
          </Button>
        )}
      </div>
      {groupID && detailState.error && <p role="status">无法读取所选分组：{detailState.error}</p>}
      {selected && (
        <section className={styles.details} aria-label="分组详情">
          <h2>{selected.name}</h2>
          <Button
            variant="ghost"
            onClick={() => {
              navigate({ page: "agents", tab: "concurrency" });
              setHistoryCursors([]);
            }}
          >
            查看全部分组记录
          </Button>
          <dl>
            <dt>已确认运行</dt>
            <dd>{selected.running}</dd>
            <dt>等待容量</dt>
            <dd>{selected.waiting}</dd>
            <dt>有效观察开始</dt>
            <dd>{time(selected.observation_since)}</dd>
            <dt>最近窗口满载</dt>
            <dd>{minutes(selected.saturated_ms)} / 至少 15 分钟</dd>
            <dt>成功 turn</dt>
            <dd>{selected.successful_turns}</dd>
            <dt>反馈状态</dt>
            <dd>{selected.feedback_healthy ? "正常" : "观察未建立或反馈不完整"}</dd>
            <dt>绑定</dt>
            <dd>
              {selected.bindings?.length
                ? selected.bindings?.map((b) => (
                    <div key={`${b.environment_id}/${b.runtime_id}`}>
                      {b.environment_id} / {b.runtime_id}
                    </div>
                  ))
                : "尚无绑定"}
            </dd>
          </dl>
        </section>
      )}
      <h2>最近 7 天变化{selected ? ` · ${selected.name}` : ""}</h2>
      <div className={styles.toolbar}>
        <Input
          aria-label="历史 Issue 筛选"
          placeholder="Issue ID"
          value={issue}
          onChange={(e) => {
            setIssue(e.target.value);
            setHistoryCursors([]);
          }}
        />
        <Input
          aria-label="历史 session 筛选"
          placeholder="Session ID"
          value={session}
          onChange={(e) => {
            setSession(e.target.value);
            setHistoryCursors([]);
          }}
        />
        <Select
          aria-label="变化原因"
          value={reason}
          onChange={(e) => {
            setReason(e.target.value);
            setHistoryCursors([]);
          }}
        >
          <option value="">全部原因</option>
          {Object.entries(reasonLabels).map(([key, label]) => (
            <option key={key} value={key}>
              {label}
            </option>
          ))}
        </Select>
        <label>
          起始时间
          <Input
            type="datetime-local"
            value={since}
            onChange={(e) => {
              setSince(e.target.value);
              setHistoryCursors([]);
            }}
          />
        </label>
        <label>
          结束时间
          <Input
            type="datetime-local"
            value={until}
            onChange={(e) => {
              setUntil(e.target.value);
              setHistoryCursors([]);
            }}
          />
        </label>
      </div>
      {historyState.error && <p role="status">无法读取变化记录：{historyState.error}</p>}
      {historyState.value && (
        <p className={styles.muted}>
          实际查询窗口：{time(historyState.value.since)} 至 {time(historyState.value.until)}
          。“未收到拥塞”仅指本组在决策时 Tend 已收到的反馈。
        </p>
      )}
      {historyState.value?.items?.map((change) => (
        <Change key={change.id} value={change} />
      ))}
      {!historyState.value?.items?.length && (
        <EmptyState>
          {historyState.loading ? "正在读取记录…" : "最近 7 天没有匹配的变化。"}
        </EmptyState>
      )}
      <div className={styles.pager}>
        {!!historyCursors.length && (
          <Button onClick={() => setHistoryCursors((v) => v.slice(0, -1))}>上一页记录</Button>
        )}
        {historyState.value?.more && (
          <Button onClick={() => setHistoryCursors((v) => [...v, historyState.value!.next!])}>
            下一页记录
          </Button>
        )}
      </div>
    </section>
  );
}
