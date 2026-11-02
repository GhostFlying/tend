import { Fragment, useEffect } from "react";
import type {
  AgentCategory,
  AgentInventoryDefinition,
  AgentInventoryProjection,
  AgentInventoryRuntime,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { navigate, useRoute, type AgentFilters } from "../app/router.ts";
import { useStore } from "../state/store.ts";
import { Button, Input, Select, EmptyState } from "../ui/index.ts";
import { runtimeLabel } from "./runtimeLabel.ts";
import { StatusDot } from "../shell/StatusDot.tsx";
import { agentKey, currentObservation } from "./facts.ts";
import styles from "./Directory.module.css";

type Group = {
  id: string;
  projection: AgentInventoryProjection;
  catalog?: RunnerAgentCatalog | undefined;
};
type Entry = {
  group: Group;
  agent: AgentInventoryDefinition;
  runtime: AgentInventoryRuntime | undefined;
};

export function AgentCatalogView() {
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
    groups.push({ id: "托管环境", projection: hosted.value.projection });
  for (const id of hostIDs.split("\n").filter(Boolean)) {
    const c = catalogs.get(id)?.value;
    if (c?.projection) groups.push({ id, projection: c.projection, catalog: c });
  }
  const entries: Entry[] = groups.flatMap((group) =>
    [
      ...(group.projection.agents ?? []),
      ...(group.catalog?.retained_definitions ?? []).filter(
        (a) => !group.projection.agents?.some((current) => agentKey(current) === agentKey(a)),
      ),
    ].map((agent) => ({
      group,
      agent,
      runtime: [
        ...(group.projection.runtimes ?? []),
        ...(group.catalog?.retained_runtimes ?? []),
      ].find(
        (r) =>
          r.runtime_id === agent.runtime_binding_ref.runtime_id &&
          r.revision === agent.runtime_binding_ref.revision,
      ),
    })),
  );
  const query = (filters.search ?? "").trim().toLocaleLowerCase();
  const visible = entries.filter(
    ({ agent, runtime, group }) =>
      (!query ||
        `${agent.display_name} ${agent.identity.local_id} ${group.id}`
          .toLocaleLowerCase()
          .includes(query)) &&
      (!filters.category || agent.categories?.includes(filters.category)) &&
      (!filters.environment || runtimeLabel(runtime) === filters.environment),
  );
  const selected = visible.find((e) => agentKey(e.agent) === filters.selected);
  const allCategories = categories.value?.items ?? [];
  const environments = [...new Set(entries.map((e) => runtimeLabel(e.runtime)))].sort();
  const errors = hosted.error || categories.error || [...catalogs.values()].some((c) => c.error);
  const title = (id: string) => allCategories.find((c) => c.category_id === id)?.title || id;
  const pick = (entry: Entry) =>
    update({ selected: filters.selected === agentKey(entry.agent) ? "" : agentKey(entry.agent) });
  return (
    <section className={styles.root}>
      <div
        className={styles.tabs}
        role="tablist"
        aria-label="目录内容"
        onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
          event.preventDefault();
          const categories = event.key === "End" || (event.key !== "Home" && !filters.tab);
          update({ tab: categories ? "categories" : undefined, search: "" });
          const tabs = event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]');
          tabs.item(categories ? 1 : 0)?.focus();
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
      </div>
      <div className={styles.toolbar}>
        <Input
          aria-label={filters.tab ? "搜索类别" : "搜索名称或 slug"}
          placeholder={filters.tab ? "搜索类别" : "搜索名称或 slug"}
          value={filters.search ?? ""}
          onChange={(e) => update({ search: e.target.value })}
        />
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
              aria-label="运行环境筛选"
              value={filters.environment ?? ""}
              onChange={(e) => update({ environment: e.target.value })}
            >
              <option value="">全部运行环境</option>
              {environments.map((e) => (
                <option key={e}>{e}</option>
              ))}
            </Select>
          </>
        )}
        <Button variant="secondary" onClick={() => live.agents.refresh()}>
          重新读取
        </Button>
      </div>
      {hosted.value?.state === "unconfigured" && <p className={styles.muted}>尚未配置托管 Agent</p>}
      {errors && (
        <p role="status" className={styles.warning}>
          部分目录暂时无法读取，保留上次结果。
        </p>
      )}
      {filters.tab === "categories" ? (
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
                `${c.title} ${c.category_id} ${c.description}`.toLocaleLowerCase().includes(query),
            )
            .map((c) => (
              <CategoryRow
                key={c.category_id}
                category={c}
                entries={entries.filter((e) =>
                  e.group.projection.agents?.some((a) => agentKey(a) === agentKey(e.agent)),
                )}
                onSelect={(entry) =>
                  update({
                    tab: undefined,
                    search: "",
                    category: c.category_id,
                    environment: "",
                    selected: agentKey(entry.agent),
                  })
                }
              />
            ))}
          {!allCategories.length && (
            <EmptyState>{categories.loading ? "正在读取类别…" : "暂无类别。"}</EmptyState>
          )}
          {categories.value?.more && (
            <Button onClick={() => void live.agents.openCategories("all", categories.value?.next)}>
              加载更多类别
            </Button>
          )}
        </div>
      ) : (
        <div className={`${styles.layout} ${selected ? styles.withDetails : ""}`}>
          <div className={styles.list}>
            {groups.map((group) => {
              const rows = visible.filter((e) => e.group.id === group.id);
              if (!rows.length && (query || filters.category || filters.environment)) return null;
              const environments = [...new Set(rows.map((e) => runtimeLabel(e.runtime)))].sort();
              return (
                <section key={group.id} className={styles.group} aria-label={group.id}>
                  <h2>
                    {group.id}
                    <span className={styles.presence}>
                      {group.catalog && <StatusDot tone={group.catalog.online ? "ok" : "idle"} />}
                      {group.catalog
                        ? group.catalog.online
                          ? "在线"
                          : "离线 · 上次观察"
                        : "托管定义"}
                    </span>
                  </h2>
                  <div className={styles.head}>
                    <span>名称</span>
                    <span>类别</span>
                    <span>执行配置</span>
                    <span>最近上报</span>
                  </div>
                  {environments.map((environment) => (
                    <Fragment key={environment}>
                      <div className={styles.environment}>{environment}</div>
                      {rows
                        .filter((e) => runtimeLabel(e.runtime) === environment)
                        .map((entry) => {
                          const { agent } = entry;
                          const worker = currentObservation(group.catalog, agent, "");
                          const manager = currentObservation(group.catalog, agent, "issue_manager");
                          const observation = worker ?? manager;
                          const retainedOnly = !group.projection.agents?.some(
                            (a) => agentKey(a) === agentKey(agent),
                          );
                          const selectedRow = selected === entry;
                          return (
                            <Fragment key={agentKey(agent)}>
                              <Button
                                variant="ghost"
                                className={`${styles.row} ${selectedRow ? styles.selected : ""}`}
                                aria-expanded={selectedRow}
                                onClick={() => pick(entry)}
                              >
                                <span className={styles.identity}>
                                  <strong>{agent.display_name}</strong>
                                  <small>{agent.identity.local_id}</small>
                                </span>
                                <span>{agent.categories?.map(title).join("、")}</span>
                                <span>
                                  {environment} · {agent.requested_model} ·{" "}
                                  {agent.requested_effort || "默认"}
                                </span>
                                <span
                                  className={
                                    observation?.available && group.catalog?.online
                                      ? styles.available
                                      : styles.muted
                                  }
                                >
                                  {retainedOnly
                                    ? "仅续接旧责任"
                                    : !agent.enabled
                                      ? "已停用"
                                      : group.catalog && !group.catalog.online
                                        ? "离线"
                                        : observation
                                          ? observation.available
                                            ? "最近上报可用"
                                            : "最近上报不可用"
                                          : "等待本版观测"}
                                </span>
                              </Button>
                              {selectedRow && (
                                <div className={styles.inlineDetails}>
                                  <CompactDetails entry={entry} />
                                </div>
                              )}
                            </Fragment>
                          );
                        })}
                    </Fragment>
                  ))}
                  {!rows.length && <EmptyState>此环境暂无 Agent 定义。</EmptyState>}
                </section>
              );
            })}
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
            {!entries.length && !hosted.loading && snapshot && !hostIDs && (
              <EmptyState>暂无已登记的 Agent。</EmptyState>
            )}
            {!!entries.length && !visible.length && (
              <EmptyState>没有符合筛选条件的 Agent。</EmptyState>
            )}
          </div>
          {selected && (
            <aside className={styles.inspector} aria-label={`${selected.agent.display_name} 详情`}>
              <AgentDetails entry={selected} />
            </aside>
          )}
        </div>
      )}
      <p className={styles.footer}>
        定义由对应环境维护，此页只读。类别用于选择，不授予执行或验收权限。
      </p>
    </section>
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
            <Button key={agentKey(e.agent)} variant="link" onClick={() => onSelect(e)}>
              {e.agent.display_name} · {e.group.id}
            </Button>
          ))}
      </div>
    </div>
  );
}
function AgentDetails({ entry: { agent, group, runtime } }: { entry: Entry }) {
  const catalog = group.catalog;
  const worker = currentObservation(catalog, agent, "");
  const manager = currentObservation(catalog, agent, "issue_manager");
  const owner = [...(group.projection.runtimes ?? []), ...(catalog?.retained_runtimes ?? [])].find(
    (r) =>
      r.runtime_id === (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).runtime_id &&
      r.revision === (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).revision,
  );
  const retained =
    catalog?.retained_executions?.filter(
      (p) =>
        p.identity.local_id === agent.identity.local_id &&
        p.identity.execution_environment_id === agent.identity.execution_environment_id,
    ) ?? [];
  const observed = (value: typeof worker) =>
    value ? (value.available ? "可用" : value.reason || "不可用") : "未观测";
  return (
    <div className={styles.details}>
      <h2>{agent.display_name}</h2>
      <p className={styles.muted}>slug：{agent.identity.local_id}</p>
      <h3>定义</h3>
      <dl>
        <dt>类别</dt>
        <dd>{agent.categories?.join("、")}</dd>
        <dt>用途</dt>
        <dd>
          {agent.purposes
            ?.map((p) =>
              p === "worker_run"
                ? "执行"
                : p === "issue_manager"
                  ? "Issue 负责人"
                  : p === "consultation"
                    ? "咨询"
                    : p,
            )
            .join("、")}
        </dd>
        <dt>定义版本</dt>
        <dd>v{agent.revision}</dd>
        <dt>执行环境</dt>
        <dd>{runtimeLabel(runtime)}</dd>
        <dt>协调环境</dt>
        <dd>{runtimeLabel(owner)}</dd>
        <dt>模型</dt>
        <dd>{agent.requested_model}</dd>
        <dt>推理强度</dt>
        <dd>{agent.requested_effort || "默认"}</dd>
        <dt>配置</dt>
        <dd>{agent.enabled ? "已启用" : "已停用"}</dd>
      </dl>
      <h3>最近观测</h3>
      <dl>
        <dt>登记</dt>
        <dd>
          {catalog?.current
            ? `已登记 · v${catalog.current.local_revision}`
            : catalog
              ? "未登记"
              : "已声明"}
        </dd>
        <dt>执行</dt>
        <dd>{observed(worker)}</dd>
        <dt>协调</dt>
        <dd>{observed(manager)}</dd>
        <dt>主机</dt>
        <dd>{catalog ? (catalog.online ? "在线" : "离线 · 上次观察") : "托管环境"}</dd>
        <dt>更新时间</dt>
        <dd>
          {catalog?.last_heartbeat_at_ms
            ? new Date(catalog.last_heartbeat_at_ms).toLocaleString("zh-CN")
            : "未记录"}
        </dd>
      </dl>
      {catalog && !catalog.continuation_supported && (
        <p className={styles.muted}>此 runner 尚未支持旧版本续接观测。</p>
      )}
      {!!retained.length && (
        <>
          <h3>既有责任</h3>
          <p className={styles.muted}>既有责任继续使用原版本，切换版本需显式交接。</p>
          {retained.map((p) => {
            const observation = catalog?.continuations?.find(
              (e) => JSON.stringify(e.pin) === JSON.stringify(p),
            );
            return (
              <p key={JSON.stringify(p)}>
                v{p.agent_revision} · {p.purpose === "issue_manager" ? "协调" : "执行"} ·{" "}
                {observed(observation)}
              </p>
            );
          })}
        </>
      )}
    </div>
  );
}

function CompactDetails({ entry }: { entry: Entry }) {
  const { agent, group, runtime } = entry;
  const worker = currentObservation(group.catalog, agent, "");
  const manager = currentObservation(group.catalog, agent, "issue_manager");
  const available = (value: typeof worker) =>
    value ? (value.available ? "可用" : "不可用") : "未观测";
  return (
    <div className={styles.compact}>
      <dl>
        <dt>定义</dt>
        <dd>
          v{agent.revision} · {agent.enabled ? "已启用" : "已停用"}
        </dd>
        <dt>运行环境</dt>
        <dd>{runtimeLabel(runtime)}</dd>
        <dt>登记</dt>
        <dd>{group.catalog?.current ? "已登记" : "已声明"}</dd>
        <dt>最近上报</dt>
        <dd>
          执行 {available(worker)} · 协调 {available(manager)}
        </dd>
      </dl>
      <details>
        <summary>完整定义与观测</summary>
        <AgentDetails entry={entry} />
      </details>
    </div>
  );
}
