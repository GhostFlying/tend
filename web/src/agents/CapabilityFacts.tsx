import type {
  AgentCapabilityView,
  AgentInventoryDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";

export function capabilityFacts(
  catalog: RunnerAgentCatalog | undefined,
  agent: AgentInventoryDefinition,
  historical = false,
): AgentCapabilityView | undefined {
  if (historical || !catalog?.online) return undefined;
  const facts = catalog.capabilities?.find(
    (v) =>
      v.identity.execution_environment_id === agent.identity.execution_environment_id &&
      v.identity.local_id === agent.identity.local_id &&
      v.agent_revision === agent.revision &&
      v.pin?.snapshot_id === catalog.current?.snapshot_id &&
      v.pin?.public_sha256 === catalog.current?.public_sha256 &&
      v.pin?.runtime.runtime_id === agent.runtime_binding_ref.runtime_id &&
      v.pin?.runtime.revision === agent.runtime_binding_ref.revision &&
      v.pin?.profile.model === agent.requested_model &&
      (v.pin?.profile.effort ?? "") === (agent.requested_effort ?? ""),
  );
  return facts;
}

export function CapabilityFacts({
  catalog,
  agent,
  historical,
}: {
  catalog: RunnerAgentCatalog | undefined;
  agent: AgentInventoryDefinition;
  historical: boolean;
}) {
  const facts = capabilityFacts(catalog, agent, historical);
  const execution = facts?.evidence?.filter((e) => e.kind === "execution").at(-1);
  return (
    <>
      <dt>能力声明</dt>
      <dd>
        {agent.capabilities?.map((r) => `${r.capability_id}@${r.definition_revision}`).join("、") ||
          "尚无声明"}
      </dd>
      <dt>声明匹配</dt>
      <dd>
        {facts ? (
          <>
            {facts.current.state === "eligible"
              ? "配置声明满足要求；实际执行未验证"
              : facts.current.state === "unavailable"
                ? "当前不满足能力要求"
                : "等待原执行入口可用"}
            <ul>
              {facts.current.reasons?.map((r) => (
                <li key={`${r.capability.capability_id}:${r.reason}`}>
                  {r.capability.capability_id} · {r.reason}
                  {r.expires_at_ms
                    ? ` · 有效期至 ${new Date(r.expires_at_ms).toLocaleString()}`
                    : ""}
                  <br />
                  {r.recovery}
                </li>
              ))}
            </ul>
          </>
        ) : historical ? (
          "历史声明，不代表当前入口可用"
        ) : (
          "离线或缺少当前执行入口观测"
        )}
      </dd>
      <dt>最近真实调用</dt>
      <dd>
        {execution
          ? `${execution.capability.capability_id} · ${execution.reason} · ${new Date(execution.observed_at_ms).toLocaleString()} · 费用${execution.cost_state === "observed" ? "已观测" : "未知"}`
          : "尚无真实调用证据；配置声明不等于调用成功"}
      </dd>
    </>
  );
}
