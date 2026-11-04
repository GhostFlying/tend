import { useEffect, useMemo, useState } from "react";
import type { TaskSummary, TaskCapabilityExplanation } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";

export function TaskCapabilityFacts({ task }: { task: TaskSummary }) {
  const { client } = useServices();
  const [open, setOpen] = useState(false);
  const [role, setRole] = useState<"implement" | "accept">("implement");
  const read = useMemo(() => new WorkItemRead<TaskCapabilityExplanation>(client), [client]);
  const state = useStore(read.state);
  const path = `/api/v1/tasks/${encodeURIComponent(task.task_id)}/capability-match?${new URLSearchParams({ role, spec_version: String(task.spec_version) })}`;
  useEffect(() => {
    if (!open) return;
    void read.open(path);
    return () => read.close();
  }, [read, open, path]);
  const selection = task.required_capabilities;
  if (!selection) return null;
  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>所需能力与匹配原因</summary>
      <p>
        实施：
        {selection.implementation.required_capabilities
          ?.map((r) => `${r.capability_id}@${r.definition_revision}`)
          .join("、") || "无能力约束"}
        ；独立验收：
        {selection.acceptance?.required_capabilities
          ?.map((r) => `${r.capability_id}@${r.definition_revision}`)
          .join("、") || "无能力约束"}
      </p>
      <label>
        检查角色{" "}
        <select
          value={role}
          onChange={(e) => setRole(e.currentTarget.value as "implement" | "accept")}
        >
          <option value="implement">实施</option>
          <option value="accept">独立验收</option>
        </select>
      </label>{" "}
      <button type="button" onClick={() => void read.open(path)}>
        重新读取匹配
      </button>
      {state.error ? (
        <p>能力匹配读取失败，保留未确认状态。</p>
      ) : state.value ? (
        <>
          <p>
            {state.value.state} · {new Date(state.value.checked_at_ms).toLocaleString()}
          </p>
          <ul>
            {state.value.reasons?.map((reason) => (
              <li key={reason}>{reason}</li>
            ))}
          </ul>
          {state.value.candidates?.map((candidate) => (
            <div
              key={`${candidate.identity.execution_environment_id}/${candidate.identity.local_id}/${candidate.pin?.backend_id}`}
            >
              <strong>
                {candidate.identity.execution_environment_id}/{candidate.identity.local_id}
              </strong>
              <ul>
                {candidate.current.reasons?.map((reason) => (
                  <li key={`${reason.capability.capability_id}:${reason.reason}`}>
                    {reason.capability.capability_id} · {reason.state} · {reason.reason}
                    <br />
                    {reason.recovery}
                  </li>
                ))}
              </ul>
            </div>
          ))}
          {state.value.next_after ? (
            <button
              type="button"
              onClick={() =>
                void read.open(`${path}&after=${encodeURIComponent(state.value!.next_after!)}`)
              }
            >
              更多候选
            </button>
          ) : null}
          <p>{state.value.recovery}</p>
        </>
      ) : (
        <p>正在读取匹配事实…</p>
      )}
    </details>
  );
}
