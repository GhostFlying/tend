import { CapabilityFacts } from "./CapabilityFacts.tsx";
import { AgentIssues, type AgentIssueModel } from "./Issues.tsx";
import { href } from "../app/router.ts";
import type { AgentCategory } from "../api/generated/api.ts";
import { currentObservation } from "./facts.ts";
import { observation, purposeLabel, type Entry } from "./entries.ts";
import { cliLabel, runtimeMethod } from "./runtimeLabel.ts";
import styles from "./Directory.module.css";

function registration(entry: Entry): string {
  const { group } = entry;
  if (!group.catalog) return "已声明";
  if (entry.history) return "历史登记 · 仅续接旧责任";
  return group.catalog.current ? `已登记 · v${group.catalog.current.local_revision}` : "未登记";
}

export function AgentDetails({
  entry,
  categories,
  issues,
}: {
  entry: Entry;
  categories: AgentCategory[];
  issues: AgentIssueModel | null;
}) {
  const { agent, group, runtime } = entry;
  const catalog = group.catalog;
  const worker = entry.history ? undefined : currentObservation(catalog, agent, "");
  const manager = entry.history ? undefined : currentObservation(catalog, agent, "issue_manager");
  const owner = (entry.history?.runtimes ?? group.projection.runtimes ?? []).find(
    (r) =>
      r.runtime_id === (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).runtime_id &&
      r.revision === (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).revision,
  );
  const retained =
    catalog?.retained_executions?.filter(
      (p) =>
        p.identity.local_id === agent.identity.local_id &&
        p.identity.execution_environment_id === agent.identity.execution_environment_id &&
        (!entry.history ||
          (p.snapshot_id === entry.history.pin.snapshot_id &&
            p.public_sha256 === entry.history.pin.public_sha256)),
    ) ?? [];
  const observed = (value: typeof worker) =>
    value
      ? value.available
        ? "可用"
        : value.reason || "不可用"
      : catalog
        ? "未上报"
        : "未接入运行观测";
  return (
    <div className={styles.details}>
      <h2 tabIndex={-1}>{agent.display_name}</h2>
      <p className={styles.muted}>{agent.identity.local_id}</p>
      <dl>
        <dt>用途</dt>
        <dd>{agent.purposes?.map(purposeLabel).join("、") || "未声明"}</dd>
        <dt>类别</dt>
        <dd>
          {agent.categories
            ?.map((id) => categories.find((c) => c.category_id === id)?.title || id)
            .join("、")}
        </dd>
        <dt>运行位置</dt>
        <dd>{group.id}</dd>
        <dt>运行方式</dt>
        <dd>{runtimeMethod(runtime)}</dd>
        <dt>模型</dt>
        <dd>{agent.requested_model}</dd>
        <dt>推理强度</dt>
        <dd>{agent.requested_effort || "默认"}</dd>
        <dt>并发分组</dt>
        <dd>
          {[
            ...new Set([
              agent.runtime_binding_ref.runtime_id,
              (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).runtime_id,
            ]),
          ].map((runtimeID) => {
            const binding = (
              group.catalog?.concurrency_bindings ??
              group.concurrencyBindings ??
              []
            ).find((b) => b.runtime_id === runtimeID);
            return (
              <div key={runtimeID}>
                {runtimeID} ·{" "}
                {binding ? (
                  <a href={href({ page: "agents", tab: "concurrency", group: binding.group_id })}>
                    {binding.group_id}
                  </a>
                ) : (
                  "未绑定"
                )}
              </div>
            );
          })}
        </dd>
        <CapabilityFacts catalog={catalog} agent={agent} historical={!!entry.history} />
        <dt>配置</dt>
        <dd>{agent.enabled ? "已启用" : "已停用"}</dd>
        <dt>登记版本</dt>
        <dd>
          {entry.history
            ? `r${entry.history.pin.inventory_revision}`
            : catalog?.current
              ? `r${catalog.current.server_revision}`
              : group.configurationRevision
                ? `r${group.configurationRevision}`
                : "未登记"}
        </dd>
      </dl>
      <dl>
        <dt>最近观测</dt>
        <dd>{observation(entry).label}</dd>
      </dl>
      {!catalog && (
        <p className={styles.muted}>
          服务端定义不使用 runner 心跳；本目录未提供执行健康观测，用途也不代表能力类别。
        </p>
      )}
      <details className={styles.disclosure}>
        <summary>绑定与版本</summary>
        <dl>
          <dt>定义版本</dt>
          <dd>v{agent.revision}</dd>
          {entry.history && (
            <>
              <dt>原登记</dt>
              <dd>r{entry.history.pin.inventory_revision}</dd>
              <dt>快照</dt>
              <dd>{entry.history.pin.snapshot_id}</dd>
            </>
          )}
          <dt>执行环境</dt>
          <dd>
            {runtimeMethod(runtime)} · {agent.runtime_binding_ref.runtime_id} · v
            {agent.runtime_binding_ref.revision}
          </dd>
          <dt>协调环境</dt>
          <dd>
            {runtimeMethod(owner)} ·{" "}
            {(agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).runtime_id} · v
            {(agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref).revision}
          </dd>
          <dt>CLI 声明</dt>
          <dd>{cliLabel(runtime?.cli) || "未声明"}</dd>
        </dl>
      </details>
      <details className={styles.disclosure}>
        <summary>观测与既有责任</summary>
        <dl>
          <dt>登记</dt>
          <dd>{registration(entry)}</dd>
          <dt>执行</dt>
          <dd>{entry.history ? "见下方原版本观测" : observed(worker)}</dd>
          <dt>协调</dt>
          <dd>{entry.history ? "见下方原版本观测" : observed(manager)}</dd>
          <dt>主机</dt>
          <dd>{catalog ? (catalog.online ? "在线" : "离线 · 上次观察") : "服务端"}</dd>
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
                  v{p.agent_revision} · {p.purpose === "issue_manager" ? "协调" : "执行"} · r
                  {p.inventory_revision} · {p.backend_id || "原生"} · {observed(observation)}
                </p>
              );
            })}
          </>
        )}
      </details>
      {issues && <AgentIssues model={issues} />}
    </div>
  );
}
