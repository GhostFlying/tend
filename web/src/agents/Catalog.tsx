import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { AgentCategory } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { navigate, useRoute, type AgentFilters } from "../app/router.ts";
import { useStore } from "../state/store.ts";
import { Button, Input, Select, EmptyState } from "../ui/index.ts";
import { Back, Chevron } from "../shell/icons.tsx";
import { PageHeader } from "../shell/PageHeader.tsx";
import { StatusDot } from "../shell/StatusDot.tsx";
import { agentKey } from "./facts.ts";
import {
  directoryEntry,
  activityOrder,
  taskActivity,
  entryKey,
  observation,
  purposeLabel,
  type Entry,
  type Group,
} from "./entries.ts";
import { cliLabel, runtimeLabel, runtimeMethod } from "./runtimeLabel.ts";
import { ExtensionsDirectory } from "./Extensions.tsx";
import { ConcurrencyDirectory } from "./Concurrency.tsx";
import { useAgentIssueModel } from "./Issues.tsx";
import { AgentDetails } from "./Details.tsx";
import styles from "./Directory.module.css";

function RefreshIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
      <path d="M20 5v6h-6M4 19v-6h6" />
      <path d="M19.3 10a7.5 7.5 0 0 0-12.7-4L4 9m16 6-2.6 3A7.5 7.5 0 0 1 4.7 14" />
    </svg>
  );
}

export function AgentCatalogView() {
  const [historyOpen, setHistoryOpen] = useState(false);
  const [concurrencyRefresh, setConcurrencyRefresh] = useState(0);
  const root = useRef<HTMLElement>(null);
  const listPosition = useRef(0);
  const returnRow = useRef<HTMLButtonElement | null>(null);
  const wasStandaloneDetail = useRef(false);
  const { live } = useServices();
  const route = useRoute();
  const filters: AgentFilters = route.page === "agents" ? route : {};
  const categories = useStore(live.agents.categories.state);
  const hosted = useStore(live.agents.hosted.state);
  const catalogs = useStore(live.agents.catalogs);
  const snapshot = useStore(live.snapshot);
  const hostIDs = (snapshot?.hosts ?? []).map((h) => h.host_id).join("\n");
  useEffect(() => {
    void live.agents.openCategories("all");
    void live.agents.openHosted();
  }, [live]);
  useEffect(() => {
    for (const id of hostIDs.split("\n").filter(Boolean)) live.agents.ensureRunner(id, true);
  }, [live, hostIDs]);
  const update = (patch: AgentFilters) =>
    navigate({ page: "agents", ...filters, ...patch }, { replace: true });
  const groups: Group[] = [];
  if (hosted.value?.state !== "unconfigured" && hosted.value?.projection)
    groups.push({
      id: "服务端",
      taskActivity: hosted.value.task_activity,
      projection: hosted.value.projection,
      configurationRevision: hosted.value.configuration_revision,
      concurrencyBindings: hosted.value.concurrency_bindings,
    });
  for (const id of hostIDs.split("\n").filter(Boolean)) {
    const c = catalogs.get(id)?.value;
    if (c?.projection) groups.push({ id, projection: c.projection, catalog: c });
  }
  const entries = groups.flatMap((group) => [
    ...(group.projection.agents ?? []).map((agent) => directoryEntry(group, agent)),
    ...(group.catalog?.retained_definitions ?? [])
      .filter(
        (history) =>
          !(
            history.pin.snapshot_id === group.catalog?.current?.snapshot_id &&
            history.pin.public_sha256 === group.catalog?.current?.public_sha256 &&
            group.projection.agents?.some((a) => agentKey(a) === agentKey(history.definition))
          ),
      )
      .map((history) => directoryEntry(group, history.definition, history)),
  ]);
  // Hosted roles have a useful reading order independent of their stable slugs.
  const roleOrder = (entry: Entry) =>
    entry.agent.purposes?.includes("main")
      ? 0
      : entry.agent.purposes?.includes("issue_manager")
        ? 1
        : 2;
  const current = entries
    .filter((e) => !e.history)
    .sort((a, b) => (a.group === b.group && !a.group.catalog ? roleOrder(a) - roleOrder(b) : 0));
  const query = (filters.search ?? "").trim().toLocaleLowerCase();
  const matches = ({ agent, runtime, group }: Entry) =>
    (!query ||
      `${agent.display_name} ${agent.identity.local_id} ${group.id}`
        .toLocaleLowerCase()
        .includes(query)) &&
    (!filters.category || agent.categories?.includes(filters.category)) &&
    (!filters.location || group.projection.execution_environment_id === filters.location) &&
    (!filters.environment || runtimeLabel(runtime) === filters.environment);
  const visible = current.filter(matches).sort((a, b) => activityOrder(a) - activityOrder(b));
  const historical = entries.filter((e) => e.history && matches(e));
  const selectable = [...visible, ...historical];
  const selected =
    selectable.find((e) => entryKey(e) === filters.selected) ??
    selectable
      .filter((e) => agentKey(e.agent) === filters.selected)
      .sort(
        (a, b) =>
          (b.history?.pin.inventory_revision ?? 0) - (a.history?.pin.inventory_revision ?? 0),
      )[0];
  const issueAssociations = useAgentIssueModel(filters.tab ? undefined : selected);
  const allCategories = categories.value?.items ?? [];
  const environments = [...new Set(entries.map((e) => runtimeLabel(e.runtime)))].sort();
  const methods = environments.map((value) => {
    const runtime = entries.find((entry) => runtimeLabel(entry.runtime) === value)?.runtime;
    return {
      value,
      title: runtimeMethod(runtime),
      scope: runtime?.kind === "hosted-manager" ? "服务端" : "主机",
    };
  });
  const errors = hosted.error || categories.error || [...catalogs.values()].some((c) => c.error);
  const title = (id: string) => allCategories.find((c) => c.category_id === id)?.title || id;
  const detailKey = !filters.tab && selected ? entryKey(selected) : "";
  useLayoutEffect(() => {
    const scroller = root.current?.closest<HTMLElement>('[aria-label="Agents"]');
    if (!scroller || !window.matchMedia("(max-width: 1399px)").matches) return;
    if (detailKey) {
      scroller.scrollTop = 0;
      root.current
        ?.querySelector<HTMLElement>(`.${styles.mobileDetails} h2`)
        ?.focus({ preventScroll: true });
    } else if (wasStandaloneDetail.current) {
      scroller.scrollTop = listPosition.current;
      returnRow.current?.focus({ preventScroll: true });
    }
    wasStandaloneDetail.current = !!detailKey;
  }, [detailKey]);
  const pick = (entry: Entry, button: HTMLButtonElement) => {
    listPosition.current =
      root.current?.closest<HTMLElement>('[aria-label="Agents"]')?.scrollTop ?? 0;
    returnRow.current = button;
    update({ selected: selected === entry ? "" : entryKey(entry) });
  };
  const refresh = (
    <Button
      variant="ghost"
      icon
      aria-label="重新读取"
      onClick={() => {
        live.agents.refresh();
        setConcurrencyRefresh((value) => value + 1);
      }}
    >
      <RefreshIcon />
    </Button>
  );
  const toolbar = (
    <div className={styles.toolbar}>
      <label className={styles.search}>
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          aria-hidden="true"
        >
          <circle cx="10.5" cy="10.5" r="6.5" />
          <path d="m16 16 4 4" />
        </svg>
        <Input
          aria-label={filters.tab ? "搜索类别" : "搜索名称或 ID"}
          placeholder={filters.tab ? "搜索类别" : "搜索名称或 ID"}
          value={filters.search ?? ""}
          onChange={(e) => update({ search: e.target.value })}
        />
      </label>
      {!filters.tab && (
        <>
          <Select
            aria-label="类别筛选"
            value={filters.category ?? ""}
            onChange={(e) => update({ category: e.target.value })}
          >
            <option value="">全部类别</option>
            {allCategories.map((c) => (
              <option key={c.category_id} value={c.category_id}>
                {c.title}
              </option>
            ))}
          </Select>
          <Select
            aria-label="运行位置或方式筛选"
            value={
              filters.environment
                ? `method:${filters.environment}`
                : filters.location
                  ? `location:${filters.location}`
                  : ""
            }
            onChange={(e) => {
              const value = e.target.value;
              update({
                location: value.startsWith("location:") ? value.slice(9) : "",
                environment: value.startsWith("method:") ? value.slice(7) : "",
              });
            }}
          >
            <option value="">全部位置</option>
            <optgroup label="运行位置">
              {groups.map((g) => (
                <option
                  key={g.projection.execution_environment_id}
                  value={`location:${g.projection.execution_environment_id}`}
                >
                  {g.id}
                </option>
              ))}
            </optgroup>
            <optgroup label="运行方式">
              {methods.map((method) => (
                <option key={method.value} value={`method:${method.value}`}>
                  {method.title}
                  {methods.some(
                    (other) => other.value !== method.value && other.title === method.title,
                  )
                    ? `（${method.scope}）`
                    : ""}
                </option>
              ))}
            </optgroup>
          </Select>
        </>
      )}
      <span className={styles.toolbarRefresh}>{refresh}</span>
    </div>
  );
  return (
    <>
      <PageHeader
        className={styles.pageHeader}
        title="Agents"
        actions={<span className={styles.headerRefresh}>{refresh}</span>}
      />
      <section
        ref={root}
        className={`${styles.root} ${selected && !filters.tab ? styles.hasSelection : ""}`}
      >
        {hosted.value?.state === "unconfigured" && (
          <p className={styles.muted}>尚未配置服务端 Agent</p>
        )}
        {errors && (
          <p role="status" className={styles.warning}>
            部分目录暂时无法读取，保留上次结果。
          </p>
        )}
        <div className={styles.catalogContent}>
          <div
            className={styles.tabs}
            role="tablist"
            aria-label="目录内容"
            onKeyDown={(event) => {
              if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
              event.preventDefault();
              const tabs = [undefined, "categories", "concurrency", "extensions"] as const;
              const current = tabs.indexOf(filters.tab);
              const next =
                event.key === "Home"
                  ? 0
                  : event.key === "End"
                    ? 3
                    : (current + (event.key === "ArrowRight" ? 1 : 3)) % 4;
              update({ tab: tabs[next], search: "" });
              event.currentTarget
                .querySelectorAll<HTMLButtonElement>('[role="tab"]')
                .item(next)
                ?.focus();
            }}
          >
            <Button
              variant="ghost"
              role="tab"
              aria-selected={!filters.tab}
              tabIndex={filters.tab ? -1 : 0}
              onClick={() => update({ tab: undefined, search: "" })}
            >
              Agents
            </Button>
            <Button
              variant="ghost"
              role="tab"
              aria-selected={filters.tab === "categories"}
              tabIndex={filters.tab === "categories" ? 0 : -1}
              onClick={() => update({ tab: "categories", search: "" })}
            >
              Categories
            </Button>
            <Button
              variant="ghost"
              role="tab"
              aria-selected={filters.tab === "concurrency"}
              tabIndex={filters.tab === "concurrency" ? 0 : -1}
              onClick={() => update({ tab: "concurrency", search: "" })}
            >
              并发分组
            </Button>
            <Button
              variant="ghost"
              role="tab"
              aria-selected={filters.tab === "extensions"}
              tabIndex={filters.tab === "extensions" ? 0 : -1}
              onClick={() => update({ tab: "extensions", search: "" })}
            >
              托管扩展
            </Button>
          </div>
          {filters.tab === "extensions" ? (
            <ExtensionsDirectory refreshVersion={concurrencyRefresh} />
          ) : filters.tab === "concurrency" ? (
            <ConcurrencyDirectory refreshVersion={concurrencyRefresh} />
          ) : filters.tab === "categories" ? (
            <>
              {toolbar}
              <div className={styles.categoryList}>
                <div className={styles.categoryHead}>
                  <span>类别</span>
                  <span>说明</span>
                  <span>状态</span>
                  <span>已登记 Agent</span>
                </div>
                {allCategories
                  .filter(
                    (c) =>
                      !query ||
                      `${c.title} ${c.category_id} ${c.description}`
                        .toLocaleLowerCase()
                        .includes(query),
                  )
                  .map((c) => (
                    <CategoryRow
                      key={c.category_id}
                      category={c}
                      entries={current}
                      onSelect={(entry) =>
                        update({
                          tab: undefined,
                          search: "",
                          category: c.category_id,
                          environment: "",
                          location: "",
                          selected: entryKey(entry),
                        })
                      }
                    />
                  ))}
                {!allCategories.length && (
                  <EmptyState>{categories.loading ? "正在读取类别…" : "暂无类别。"}</EmptyState>
                )}
                {categories.value?.more && (
                  <Button
                    disabled={categories.loading}
                    onClick={() => void live.agents.openCategories("all", categories.value?.next)}
                  >
                    加载更多类别
                  </Button>
                )}
              </div>
            </>
          ) : (
            <div className={`${styles.layout} ${selected ? styles.withDetails : ""}`}>
              <div className={styles.list}>
                {toolbar}
                <p className={styles.activityNote}>
                  运行任务优先，组内保留目录顺序。计入启动、运行及执行中等待的去重任务，
                  不含主对话和咨询轮次；待确认单列。忙碌 Agent 仍可并发。
                </p>
                <div className={styles.head}>
                  <span>Agent</span>
                  <span>类别</span>
                  <span>运行位置</span>
                  <span>运行方式</span>
                  <span>模型 / 推理</span>
                  <span>观测</span>
                </div>
                {visible.map((entry) => (
                  <AgentRow
                    key={entryKey(entry)}
                    entry={entry}
                    selected={selected === entry}
                    title={title}
                    onPick={(button) => pick(entry, button)}
                  />
                ))}
                {!!historical.length && (
                  <details
                    className={styles.history}
                    open={historyOpen || !!selected?.history}
                    onToggle={(event) => setHistoryOpen(event.currentTarget.open)}
                  >
                    <summary>历史定义（{historical.length}）</summary>
                    {historical.map((entry) => (
                      <AgentRow
                        key={entryKey(entry)}
                        entry={entry}
                        selected={selected === entry}
                        title={title}
                        onPick={(button) => pick(entry, button)}
                      />
                    ))}
                  </details>
                )}
                {hostIDs
                  .split("\n")
                  .filter((id) => id && !catalogs.get(id)?.value?.projection)
                  .map((id) => (
                    <div key={id} className={styles.emptyHost}>
                      <strong>{id}</strong>
                      <p>
                        {catalogs.get(id)?.error
                          ? "暂时无法读取登记。"
                          : catalogs.get(id)?.value
                            ? "此主机尚未登记 Agent。"
                            : "正在读取…"}
                      </p>
                    </div>
                  ))}
                {!entries.length && (
                  <EmptyState>
                    {hosted.loading || !snapshot ? "正在读取 Agent…" : "暂无已登记的 Agent。"}
                  </EmptyState>
                )}
                {!!entries.length && !selectable.length && (
                  <EmptyState>没有符合筛选条件的 Agent。</EmptyState>
                )}
              </div>
              {selected && (
                <aside
                  className={styles.inspector}
                  aria-label={`${selected.agent.display_name} 详情`}
                >
                  <AgentDetails
                    key={entryKey(selected)}
                    entry={selected}
                    categories={allCategories}
                    issues={issueAssociations}
                  />
                </aside>
              )}
            </div>
          )}
          <p className={styles.footer}>
            定义由对应环境维护，此页只读。类别用于选择，不授予执行或验收权限。
          </p>
        </div>
        {selected && !filters.tab && (
          <div className={styles.mobileDetails}>
            <Button
              variant="ghost"
              className={styles.back}
              onClick={() => update({ selected: "" })}
            >
              <Back />
              返回列表
            </Button>
            <AgentDetails
              key={entryKey(selected)}
              entry={selected}
              categories={allCategories}
              issues={issueAssociations}
            />
          </div>
        )}
      </section>
    </>
  );
}

function AgentRow({
  entry,
  selected,
  title,
  onPick,
}: {
  entry: Entry;
  selected: boolean;
  title(id: string): string;
  onPick(button: HTMLButtonElement): void;
}) {
  const { agent, group, runtime } = entry;
  const state = observation(entry);
  const activity = taskActivity(entry);
  const purpose = !group.catalog ? agent.purposes?.map(purposeLabel).join("、") : "";
  const category = agent.categories?.map(title).join("、");
  const status = (
    <span className={styles.presence}>
      {state.available && <StatusDot tone="ok" />}
      {state.label}
    </span>
  );
  return (
    <Button
      variant="ghost"
      className={`${styles.row} ${selected ? styles.selected : ""}`}
      aria-pressed={selected}
      onClick={(event) => onPick(event.currentTarget)}
    >
      <span className={styles.identity}>
        <strong>{agent.display_name}</strong>
        {!entry.history && (
          <span className={`${styles.activity} ${activity?.running ? styles.busy : ""}`}>
            {activity
              ? activity.running
                ? `${activity.running} 个任务运行中`
                : activity.uncertain
                  ? "运行状态待确认"
                  : !group.catalog
                    ? "0 个运行任务"
                    : "0 个任务 · 空闲"
              : "任务数未提供"}
            {!!activity?.uncertain && ` · ${activity.uncertain} 个待确认`}
          </span>
        )}
        <small className={entry.history ? styles.historicalIdentity : undefined}>
          {purpose && `${purpose} · `}
          {agent.identity.local_id}
          {entry.history && ` · v${agent.revision} · r${entry.history.pin.inventory_revision}`}
        </small>
      </span>
      <span className={styles.category}>{category}</span>
      <span className={styles.location}>{group.id}</span>
      <span className={styles.method}>{runtimeMethod(runtime)}</span>
      <span className={styles.model}>
        {agent.requested_model} · {agent.requested_effort || "默认"}
      </span>
      <span className={styles.status}>{status}</span>
      <span className={styles.mobileInfo}>
        {purpose ? `用途：${purpose} · 类别：${category || "未分类"}` : `${category} · ${group.id}`}
        <small>
          {purpose
            ? `${group.id} · ${cliLabel(runtime?.cli) || runtimeMethod(runtime)} · ${agent.requested_model}`
            : `${runtimeMethod(runtime)} · ${agent.requested_model} · ${agent.requested_effort || "默认"}`}
        </small>
      </span>
      <Chevron className={styles.chevron} />
    </Button>
  );
}

function CategoryRow({
  category: c,
  entries,
  onSelect,
}: {
  category: AgentCategory;
  entries: Entry[];
  onSelect(entry: Entry): void;
}) {
  return (
    <div className={styles.categoryRow}>
      <div>
        <strong>{c.title}</strong>
        <small>
          {c.category_id} · v{c.revision}
        </small>
      </div>
      <span>{c.description || "暂无说明"}</span>
      <span>{c.state === "active" ? "启用" : "已退役"}</span>
      <div>
        {entries
          .filter((e) => e.agent.categories?.includes(c.category_id))
          .map((e) => (
            <Button key={entryKey(e)} variant="link" onClick={() => onSelect(e)}>
              {e.agent.display_name} · {e.group.id}
            </Button>
          ))}
      </div>
    </div>
  );
}
