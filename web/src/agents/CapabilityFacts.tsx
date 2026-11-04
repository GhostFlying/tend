import type {
  AgentCapabilityView,
  AgentInventoryDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";

export function capabilityFacts(
  catalog: RunnerAgentCatalog | undefined,
  agent: AgentInventoryDefinition,
  historical = false,
  now = Date.now(),
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
  if (!facts || facts.current.state !== "eligible") return facts;
  const expired = facts.current.reasons?.some(
    (reason) => !reason.expires_at_ms || reason.expires_at_ms <= now,
  );
  if (!expired) return facts;
  return {
    ...facts,
    current: {
      ...facts.current,
      state: "pending",
      reasons:
        facts.current.reasons?.map((reason) => ({
          ...reason,
          state: "pending",
          reason: "evidence_expired",
          recovery: "重新读取并验证同一入口；先对账原未知请求",
        })) ?? [],
    },
  };
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
      <dt>工具实证</dt>
      <dd>
        {facts ? (
          <>
            {facts.current.state === "eligible"
              ? "工具已确认；调用结果另行记录"
              : facts.current.state === "unavailable"
                ? "当前不满足能力要求"
                : "尚未验证或证据已失效"}
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
          "未验证；离线或缺少当前入口证据"
        )}
      </dd>
      <dt>最近真实调用</dt>
      <dd>
        {execution
          ? `${execution.capability.capability_id} · ${execution.reason} · ${new Date(execution.observed_at_ms).toLocaleString()} · 费用${execution.cost_state === "observed" ? "已观测" : "未知"}`
          : "尚无真实调用证据；工具暴露不等于调用成功"}
      </dd>
    </>
  );
}
