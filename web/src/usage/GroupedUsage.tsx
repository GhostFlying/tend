import { useEffect, useId, useRef, useState, type CSSProperties } from "react";
import type {
  TokenUsageTotals,
  TokenUsageView,
  UsageExecution,
  UsageIssue,
  UsageSession,
} from "../api/generated/api.ts";
import { Link, navigate, useEntryState, useRoute, type UsageFilters } from "../app/router.ts";
import { useServices } from "../app/services.tsx";
import { Button, Input, Select, Sheet, Tabs } from "../ui/index.ts";
import { Chevron } from "../shell/icons.tsx";
import { field } from "./format.ts";
import styles from "./GroupedUsage.module.css";

const number = new Intl.NumberFormat("zh-CN");
const windows = [
  ["total", "全部历史"],
  ["24h", "最近 24 小时"],
  ["7d", "最近 7 天"],
  ["30d", "最近 30 天"],
  ["day", "自然日"],
  ["week", "自然周"],
  ["month", "自然月"],
];
const roles: Record<string, string> = {
  main: "主会话",
  consultation: "咨询",
  issue_manager: "Issue 负责人",
  implement: "实施",
  accept: "验收",
  internal_summary: "内部摘要",
  subagent: "子 Agent",
  background: "历史职责未记录",
};
const modelLabel = (model: string) => (model === "实际模型未提供" ? "执行模型未记录" : model);
const short = (id: string) =>
  id.startsWith("legacy:")
    ? "历史用量（Session 未记录）"
    : id.length > 20
      ? `${id.slice(0, 8)}…${id.slice(-4)}`
      : id;
const time = (at: number) => new Date(at).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" });

export function estimatedPrice(t: TokenUsageTotals): string {
  if (!t.estimated_requests || !t.estimated_usd) return "暂无报价";
  const usd = Number(t.estimated_usd);
  if (!Number.isFinite(usd)) return "暂无报价";
  if (usd > 0 && usd < 0.001) return "$<0.001";
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: usd < 1 ? 3 : 2,
  }).format(usd);
}
function Price({ totals }: { totals: TokenUsageTotals }) {
  return (
    <span className={styles.price}>
      {!!totals.estimated_requests && totals.estimated_requests < totals.requests && (
        <small>部分估算</small>
      )}
      {estimatedPrice(totals)}
    </span>
  );
}
export function CompactTokens({ totals: t }: { totals: TokenUsageTotals }) {
  return (
    <div className={styles.tokens}>
      <strong>{field(t.total, t.total_known, t.requests)}</strong>
      <small>
        <span>输入 {field(t.input, t.input_known, t.requests)}</span>
        <span title="缓存读取包含在输入中，不重复相加">
          缓存 {field(t.cache_read, t.cache_read_known, t.requests)}
        </span>
        {t.cache_write > 0 && (
          <span>缓存写入 {field(t.cache_write, t.cache_write_known, t.requests)}</span>
        )}
        <span>输出 {field(t.output, t.output_known, t.requests)}</span>
      </small>
    </div>
  );
}
function useUsage(key: string | null, retry: number) {
  const { live } = useServices();
  const lastRetry = useRef(retry);
  const [result, setResult] = useState<{
    key: string;
    value: TokenUsageView | null;
    loading: boolean;
    error?: string | undefined;
  } | null>(null);
  useEffect(() => {
    if (key === null) return;
    return live.usage.watch(key, (state) => setResult({ key, ...state }));
  }, [live, key]);
  useEffect(() => {
    if (key !== null && lastRetry.current !== retry) live.usage.refresh(key);
    lastRetry.current = retry;
  }, [live, key, retry]);
  const current = result?.key === key ? result : null;
  return {
    data: current?.value ?? null,
    failed: !!current?.error,
    loading: current?.loading ?? true,
  };
}

type Selection =
  { kind: "issue"; id: string; title: string } | { kind: "session"; id: string; title: string };
export function GroupedUsage({ subject }: { subject: string }) {
  const route = useRoute();
  const [localFilters, setLocalFilters] = useState<UsageFilters>({});
  const filters: UsageFilters = !subject && route.page === "usage" ? route : localFilters;
  const group = filters.group_by ?? "issue";
  const [showFilters, setShowFilters] = useState(false);
  const [retry, setRetry] = useState(0);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [expanded, setExpanded] = useEntryState<string[]>(`usage-expanded-${subject}`, []);
  const query = new URLSearchParams({
    window: filters.window ?? "total",
    group_by: group,
    subject,
  });
  for (const key of ["offset", "model", "agent", "role", "issue", "session", "sort"] as const)
    if (filters[key]) query.set(key, filters[key]!);
  const key = query.toString();
  const main = useUsage(key, retry);
  const detailQuery = new URLSearchParams(query);
  detailQuery.set("details", "1");
  if (selection?.kind === "session") detailQuery.set("session", selection.id);
  if (selection?.kind === "issue") detailQuery.set("issue", selection.id || "_unassigned");
  const detail = useUsage(selection ? detailQuery.toString() : null, retry);
  const [page, setPage] = useState({ key, limit: 50 });
  const limit = page.key === key ? page.limit : 50;
  const update = (patch: UsageFilters) => {
    setSelection(null);
    if (subject) setLocalFilters((old) => ({ ...old, ...patch }));
    else navigate({ ...filters, ...patch, page: "usage" }, { replace: true });
  };
  const issues = main.data?.issues ?? [];
  const children = new Map<string, UsageIssue[]>();
  for (const issue of issues) {
    const siblings = children.get(issue.parent_id) ?? [];
    siblings.push(issue);
    children.set(issue.parent_id, siblings);
  }
  const visible: { issue: UsageIssue; depth: number }[] = [];
  const visit = (parent: string, depth: number, seen: Set<string>) => {
    const siblings = [...(children.get(parent) ?? [])];
    if (filters.sort === "total") siblings.sort((a, b) => b.totals.total - a.totals.total);
    for (const issue of siblings) {
      if (seen.has(issue.id)) continue;
      visible.push({ issue, depth });
      if (issue.id && expanded.includes(issue.id))
        visit(issue.id, depth + 1, new Set([...seen, issue.id]));
    }
  };
  visit("", 0, new Set());
  const sessions = main.data?.sessions ?? [];
  const count = group === "issue" ? visible.length : sessions.length;
  const totals = detail.data?.totals;
  const detailSessions = detail.data?.sessions ?? [];
  const selectedIssue = detail.data?.issues?.find((i) => i.id === selection?.id);
  return (
    <section className={styles.panel} aria-label={subject ? "本次执行 Token 用量" : "Token 用量"}>
      <div className={styles.topline}>
        {subject && <h2>本次执行用量</h2>}
        {main.data && (
          <p>
            已记录{" "}
            {field(main.data.totals.total, main.data.totals.total_known, main.data.totals.requests)}{" "}
            Token{" "}
            <span>
              · 估算 {estimatedPrice(main.data.totals)}
              {(main.data.totals.estimated_requests ?? 0) > 0 &&
              (main.data.totals.estimated_requests ?? 0) < main.data.totals.requests
                ? "（部分）"
                : ""}
            </span>
          </p>
        )}
      </div>
      <div className={styles.toolbar}>
        <Tabs
          className={styles.tabs!}
          label="用量视角"
          value={group}
          items={[
            { value: "issue", label: "按 Issue" },
            { value: "session", label: "按 Session" },
          ]}
          onChange={(value) => update({ group_by: value })}
        />
        <div className={styles.actions}>
          <Select
            aria-label="用量时间范围"
            value={filters.window ?? "total"}
            onChange={(e) => update({ window: e.target.value, offset: "" })}
          >
            {windows.map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
          <Button aria-expanded={showFilters} onClick={() => setShowFilters((v) => !v)}>
            筛选
          </Button>
        </div>
      </div>
      {showFilters && (
        <div className={styles.filters}>
          <label>
            执行模型
            <Select value={filters.model ?? ""} onChange={(e) => update({ model: e.target.value })}>
              <option value="">全部模型</option>
              {[
                ...new Set([
                  ...(main.data?.models ?? []).map((m) => m.name),
                  ...(filters.model ? [filters.model] : []),
                ]),
              ].map((m) => (
                <option key={m} value={m}>
                  {modelLabel(m)}
                </option>
              ))}
            </Select>
          </label>
          <label>
            Agent 名字
            <Input
              value={filters.agent ?? ""}
              onChange={(e) => update({ agent: e.target.value })}
            />
          </label>
          <label>
            职责
            <Select value={filters.role ?? ""} onChange={(e) => update({ role: e.target.value })}>
              <option value="">全部职责</option>
              {Object.entries(roles).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </label>
          <label>
            Issue ID
            <Input
              value={filters.issue ?? ""}
              onChange={(e) => update({ issue: e.target.value })}
            />
          </label>
          <label>
            排序
            <Select
              value={filters.sort ?? "recent"}
              onChange={(e) => update({ sort: e.target.value })}
            >
              <option value="recent">最近消费</option>
              <option value="total">Token 用量</option>
            </Select>
          </label>
          {["day", "week", "month"].includes(filters.window ?? "") && (
            <Button onClick={() => update({ offset: String(Number(filters.offset || 0) - 1) })}>
              上一周期
            </Button>
          )}
          <Button
            onClick={() =>
              update({ model: "", agent: "", role: "", issue: "", session: "", offset: "" })
            }
          >
            清除筛选
          </Button>
          <Button disabled={main.loading} onClick={() => setRetry((v) => v + 1)}>
            刷新
          </Button>
        </div>
      )}
      {main.failed && (
        <p role="alert" className={styles.notice}>
          {main.data
            ? `更新失败，显示截至 ${time(main.data.as_of)} 的读数。`
            : "读取失败，当前筛选暂无可显示的数据。"}
          <Button onClick={() => setRetry((v) => v + 1)}>重试</Button>
        </p>
      )}
      {!main.data && !main.failed && <p role="status">正在读取用量…</p>}
      {main.data && (
        <>
          {!count && (
            <p className={styles.empty}>
              {main.data.totals.requests > 0
                ? "已知总量已展示，分组明细尚未提供。"
                : "当前筛选下暂无已记录用量。"}
            </p>
          )}
          {!!count && (
            <div
              role="table"
              aria-label={group === "issue" ? "Issue 用量" : "Session 用量"}
              className={styles.list}
            >
              <div role="row" className={styles.columnHead}>
                <span role="columnheader">{group === "issue" ? "Issue" : "Session"}</span>
                <span role="columnheader">执行模型</span>
                <span role="columnheader">Token 消耗</span>
                <span role="columnheader">估算价格</span>
              </div>
              {group === "issue"
                ? visible
                    .slice(0, limit)
                    .map(({ issue, depth }) => (
                      <UsageRow
                        key={issue.id}
                        title={issue.title}
                        subtitle={issue.id}
                        depth={depth}
                        models={issue.models ?? []}
                        totals={issue.totals}
                        hasChildren={!!issue.id && children.has(issue.id)}
                        expanded={expanded.includes(issue.id)}
                        onExpand={() =>
                          setExpanded(
                            expanded.includes(issue.id)
                              ? expanded.filter((id) => id !== issue.id)
                              : [...expanded, issue.id],
                          )
                        }
                        onOpen={() =>
                          setSelection({ kind: "issue", id: issue.id, title: issue.title })
                        }
                      />
                    ))
                : sessions.slice(0, limit).map((session) => (
                    <UsageRow
                      key={session.id}
                      title={short(session.id)}
                      subtitle={sessionIssueLabel(session)}
                      models={session.models ?? []}
                      totals={session.totals}
                      onOpen={() =>
                        setSelection({
                          kind: "session",
                          id: session.id,
                          title: short(session.id),
                        })
                      }
                    />
                  ))}
            </div>
          )}
          {count > limit && (
            <Button onClick={() => setPage({ key, limit: limit + 50 })}>加载更多</Button>
          )}
          <footer className={styles.footer}>
            <span>
              采集截止 {time(main.data.collection?.last_synced_at ?? main.data.as_of)}
              {main.data.collection?.pending
                ? ` · ${number.format(main.data.collection.pending)} 个来源待对账`
                : ""}
              {(main.data.collection?.retired_incomplete ?? 0) > 0
                ? ` · ${number.format(main.data.collection.retired_incomplete ?? 0)} 个来源已停止采集，用量不完整`
                : ""}
              {(main.data.collection?.empty ?? 0) > 0
                ? ` · ${number.format(main.data.collection.empty ?? 0)} 个来源未执行`
                : ""}
              {main.data.unattributed.requests > 0
                ? ` · ${number.format(main.data.unattributed.requests)} 个请求时间未知`
                : ""}
              {main.data.collection?.legacy_requests
                ? ` · ${number.format(main.data.collection.legacy_requests)} 条历史测量待核对`
                : ""}
              {main.data.conflicts > 0
                ? ` · ${number.format(main.data.conflicts)} 个请求待核对`
                : ""}
            </span>
            <span
              title={`报价时间：${main.data.pricing?.quoted_at ? time(main.data.pricing.quoted_at) : "暂无报价"}。仅估算已记录 Token；缓存包含在输入，不重复计价。`}
            >
              价格参考 OpenRouter · USD
            </span>
          </footer>
        </>
      )}
      <Sheet
        className={styles.drawer}
        open={selection !== null}
        title={selection?.title ?? "用量详情"}
        onClose={() => setSelection(null)}
      >
        {selection && (
          <>
            {selectedIssue && (
              <p className={styles.breadcrumb}>
                {selectedIssue.path?.map((i) => (
                  <Link key={i.id} to={{ page: "issue", id: i.id }}>
                    {i.id} {i.title}
                  </Link>
                ))}
              </p>
            )}
            {detail.failed && (
              <p role="alert">
                详情更新失败。<Button onClick={() => setRetry((v) => v + 1)}>重试</Button>
              </p>
            )}
            {!detail.data && !detail.failed && <p role="status">正在读取详情…</p>}
            {totals && (
              <>
                <p className={styles.detailTotal}>
                  {field(totals.total, totals.total_known, totals.requests)} Token{" "}
                  <span>· 估算 {estimatedPrice(totals)}</span>
                </p>
                <CompactTokens totals={totals} />
                {selectedIssue &&
                  selectedIssue.direct.requests !== selectedIssue.totals.requests && (
                    <p className={styles.caption}>
                      直接消费{" "}
                      {field(
                        selectedIssue.direct.total,
                        selectedIssue.direct.total_known,
                        selectedIssue.direct.requests,
                      )}{" "}
                      · 含子 Issue 汇总{" "}
                      {field(
                        selectedIssue.totals.total,
                        selectedIssue.totals.total_known,
                        selectedIssue.totals.requests,
                      )}
                      （已去重）
                    </p>
                  )}
                <div className={styles.executions}>
                  {detailSessions.map((session) => (
                    <SessionDetail
                      key={session.id}
                      session={session}
                      selectedIssueID={selectedIssue?.id}
                      showDistribution={detailSessions.length > 1}
                      onFullSession={() => {
                        update({ group_by: "session", session: session.id, issue: "" });
                      }}
                    />
                  ))}
                </div>
                <details className={styles.explanation}>
                  <summary>报价与估算说明</summary>
                  <p>
                    按 OpenRouter 当前公开 Token
                    报价逐请求计算，缓存已从普通输入中扣除，推理包含在输出中。仅估算 Token
                    消费，不包含搜索等附加费用。
                  </p>
                  <p>
                    报价时间{" "}
                    {detail.data?.pricing?.quoted_at
                      ? time(detail.data.pricing.quoted_at)
                      : "暂无报价"}{" "}
                    · {number.format(totals.estimated_requests ?? 0)} /{" "}
                    {number.format(totals.requests)} 个请求可估算
                  </p>
                  <p>{detail.data?.coverage}</p>
                </details>
              </>
            )}
          </>
        )}
      </Sheet>
    </section>
  );
}
function UsageRow({
  title,
  subtitle,
  models,
  totals,
  depth = 0,
  hasChildren,
  expanded,
  onExpand,
  onOpen,
}: {
  title: string;
  subtitle?: string;
  models: string[];
  totals: TokenUsageTotals;
  depth?: number;
  hasChildren?: boolean;
  expanded?: boolean;
  onExpand?(): void;
  onOpen(): void;
}) {
  const knownModels = models.filter((model) => model && model !== "实际模型未提供");
  const hasUnknownModel = knownModels.length !== models.length;
  return (
    <div role="row" className={styles.usageRow} style={{ "--depth": depth } as CSSProperties}>
      <div role="cell" className={styles.identity}>
        {hasChildren ? (
          <Button
            className={styles.disclosure}
            icon
            variant="ghost"
            aria-label={`${expanded ? "收起" : "展开"}${title}`}
            aria-expanded={expanded}
            onClick={onExpand}
          >
            <Chevron className={expanded ? styles.expanded : ""} />
          </Button>
        ) : (
          <span className={styles.indent} />
        )}
        <Button variant="ghost" className={styles.title} onClick={onOpen}>
          {subtitle && <small>{subtitle}</small>}
          <span>{title}</span>
        </Button>
      </div>
      <div role="cell" className={styles.model}>
        {knownModels.length > 1 || (knownModels.length > 0 && hasUnknownModel) ? (
          <Button variant="link" onClick={onOpen}>
            {number.format(knownModels.length)} 个{hasUnknownModel ? "已知" : ""}模型
            {hasUnknownModel && " · 含未记录"}
          </Button>
        ) : (
          modelLabel(knownModels[0] ?? "实际模型未提供")
        )}
      </div>
      <div role="cell" className={styles.consumption}>
        <CompactTokens totals={totals} />
      </div>
      <div role="cell" className={styles.cost}>
        <Price totals={totals} />
      </div>
    </div>
  );
}
function sessionIssueLabel(session: UsageSession): string {
  const paths = new Map<string, { id: string; title: string }[]>();
  for (const attribution of session.attributions ?? []) {
    const path = attribution.issue_path;
    if (path?.length) paths.set(path.at(-1)!.id, path);
  }
  if (paths.size > 1) return `${number.format(paths.size)} 个 Issue`;
  return [...paths.values()][0]?.map((issue) => issue.title).join(" / ") || "主会话与未归属用量";
}

function SessionDetail({
  session,
  selectedIssueID,
  showDistribution,
  onFullSession,
}: {
  session: UsageSession;
  selectedIssueID?: string | undefined;
  showDistribution: boolean;
  onFullSession(): void;
}) {
  return (
    <section className={styles.sessionDetail} aria-label="执行分布">
      {(session.executions ?? []).map((execution, index) => (
        <ExecutionDetail
          key={`${execution.attribution.run_id}/${index}`}
          execution={execution}
          session={session}
          selectedIssueID={selectedIssueID}
          showTotals={showDistribution || (session.executions?.length ?? 0) > 1}
        />
      ))}
      <div className={styles.sessionId}>
        <span title={session.id}>
          Session {session.id.startsWith("legacy:") ? "未记录" : session.id}
        </span>
        {!session.id.startsWith("legacy:") && (
          <Button variant="link" onClick={onFullSession}>
            查看完整 Session
          </Button>
        )}
      </div>
    </section>
  );
}
function ExecutionDetail({
  execution,
  session,
  selectedIssueID,
  showTotals,
}: {
  execution: UsageExecution;
  session: UsageSession;
  selectedIssueID?: string | undefined;
  showTotals: boolean;
}) {
  const a = execution.attribution;
  const tooltip = useId();
  const measures = execution.measurements ?? [];
  const models = [...new Set(measures.map((m) => m.model || "执行模型未记录"))];
  const clis = [
    ...new Set(
      measures.map((m) =>
        m.cli
          ? `${m.cli === "codex" ? "Codex CLI" : m.cli === "traex" ? "TraeX CLI" : m.cli}${m.cli_version ? ` · ${m.cli_version}` : ""}`
          : "CLI 未记录",
      ),
    ),
  ];
  return (
    <div className={styles.execution}>
      {!!a.issue_path?.length && a.issue_path.at(-1)?.id !== selectedIssueID && (
        <p className={styles.breadcrumb}>
          {a.issue_path.map((issue) => (
            <Link key={issue.id} to={{ page: "issue", id: issue.id }}>
              {issue.id} {issue.title}
            </Link>
          ))}
        </p>
      )}
      {showTotals && (
        <>
          <p className={styles.detailTotal}>
            {field(execution.totals.total, execution.totals.total_known, execution.totals.requests)}{" "}
            Token
            <span> · 估算 {estimatedPrice(execution.totals)}</span>
          </p>
          <CompactTokens totals={execution.totals} />
        </>
      )}
      <dl>
        <dt>任务</dt>
        <dd>
          {a.task_id ? (
            <Link to={{ page: "task", id: a.task_id }}>{a.task_title || a.task_id}</Link>
          ) : (
            "未归属任务"
          )}
        </dd>
        <dt>Agent</dt>
        <dd>
          {a.agent ? (
            <button
              type="button"
              className={styles.agent}
              aria-label={a.agent}
              aria-describedby={tooltip}
              title={
                a.categories?.length ? `Category: ${a.categories.join(", ")}` : "Category 未记录"
              }
            >
              {a.agent}
              <span id={tooltip} role="tooltip" className={styles.tooltip}>
                {a.categories?.length ? `Category: ${a.categories.join(", ")}` : "Category 未记录"}
              </span>
            </button>
          ) : (
            "名字未记录"
          )}
        </dd>
        <dt>执行模型</dt>
        <dd>{models.join("、") || "未记录"}</dd>
        <dt>CLI</dt>
        <dd>{clis.join("、") || "未记录"}</dd>
        <dt>职责</dt>
        <dd>{roles[a.role] ?? a.role}</dd>
        <dt>状态</dt>
        <dd>
          {a.execution_state
            ? ["finished", "completed", "failed", "interrupted"].includes(a.execution_state)
              ? "执行已结束"
              : ["starting", "started", "running", "waiting"].includes(a.execution_state)
                ? "执行中"
                : "执行状态未知"
            : "执行状态未知"}{" "}
          ·{" "}
          {session.metering_status === "caught_up"
            ? "计量已对账"
            : session.metering_status === "unavailable"
              ? "采集已结束，用量不完整"
              : session.metering_status === "needs_reconcile"
                ? "采集已暂停，需对账"
                : session.metering_status === "syncing"
                  ? "计量待对账"
                  : "计量覆盖未确认"}
        </dd>
        <dt>完成记录时间</dt>
        <dd>
          {session.first_at ? time(session.first_at) : "未知"}
          {session.last_at && session.last_at !== session.first_at
            ? ` 至 ${time(session.last_at)}`
            : ""}
        </dd>
      </dl>
      {measures.length > 1 && (
        <div className={styles.distribution}>
          {measures.map((m, i) => (
            <div key={i}>
              <span>
                {m.model || "执行模型未记录"} · {m.cli || "CLI 未记录"}
              </span>
              <span>
                {field(m.totals.total, m.totals.total_known, m.totals.requests)} Token ·{" "}
                {estimatedPrice(m.totals)}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
