import { useEffect, useMemo, useState } from "react";
import type { AgentIssuePage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { navigate } from "../app/router.ts";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { Button, Input, Select } from "../ui/index.ts";
import type { Entry } from "./entries.ts";
import styles from "./Issues.module.css";

const states: Record<string, string> = {
  starting: "正在启动",
  running: "运行中",
  waiting: "等待中",
  interrupted: "已中断",
  unknown: "结果未知，待对账",
  finished: "已结束",
  failed: "已失败",
};
const roles: Record<string, string> = {
  implement: "实施",
  accept: "验收",
  issue_manager: "Issue owner",
};
const sources: Record<string, string> = {
  run_responsibility: "运行时责任",
  manager_contract: "协调任务合同",
  task_creation_owner: "任务创建时的主 Issue",
};
const issueStates: Record<string, string> = {
  active: "进行中",
  paused: "已暂停",
  cancelling: "正在取消",
  cancelled: "已取消",
  closed: "已关闭",
};
export function AgentIssues({ entry }: { entry: Entry }) {
  const { client, openIssue } = useServices();
  const read = useMemo(() => new WorkItemRead<AgentIssuePage>(client), [client]);
  const result = useStore(read.state);
  const [scope, setScope] = useState("current");
  const [state, setState] = useState("");
  const [search, setSearch] = useState("");
  const [cursors, setCursors] = useState<string[]>([]);
  const identity = entry.agent.identity;
  const query = new URLSearchParams({
    environment_id: identity.execution_environment_id,
    local_id: identity.local_id,
    scope,
    state,
    search,
    after: cursors.at(-1) ?? "",
    limit: "20",
    ...(entry.history ? { snapshot_id: entry.history.pin.snapshot_id } : {}),
  }).toString();
  useEffect(() => {
    void read.open(`/api/v1/agent-issues?${query}`);
    return () => read.close();
  }, [read, query]);
  const reset = (change: () => void) => {
    change();
    setCursors([]);
  };
  return (
    <section className={styles.root} aria-label="关联 Issue">
      <h3>关联 Issue</h3>
      <p className={styles.note}>
        依据执行时冻结的 Agent 身份与原版本，只显示有明确 Run 关联的 Issue。
        主会话、咨询或旧记录缺少关联时，不推测已处理历史。
      </p>
      <div className={styles.filters}>
        <Select
          aria-label="Issue 关联范围"
          value={scope}
          onChange={(e) => reset(() => setScope(e.target.value))}
        >
          <option value="current">当前未结执行 / 待决策</option>
          <option value="history">历史执行</option>
          <option value="all">全部已记录关联</option>
        </Select>
        <Select
          aria-label="关联 Issue 状态"
          value={state}
          onChange={(e) => reset(() => setState(e.target.value))}
        >
          <option value="">全部 Issue 状态</option>
          {Object.entries(issueStates).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </Select>
        <Input
          aria-label="搜索关联 Issue"
          placeholder="搜索 Issue 标题或 ID"
          value={search}
          onChange={(e) => reset(() => setSearch(e.target.value))}
        />
        <Button disabled={result.loading} onClick={() => void read.refresh()}>
          刷新关联
        </Button>
      </div>
      {result.error && <p role="alert">无法读取 Issue 关联：{result.error}</p>}
      {result.loading && <p role="status">正在读取 Issue 关联…</p>}
      {!result.loading && !result.error && !result.value?.items?.length && (
        <p>此范围暂无已记录关联，不代表从未处理过 Issue。</p>
      )}
      {result.value?.items?.map((item) => (
        <article key={item.issue_id} className={styles.issue}>
          <Button variant="link" onClick={() => openIssue(item.issue_id)}>
            {item.title || item.issue_id}
          </Button>
          <p>
            {issueStates[item.state] || item.state} ·{" "}
            {item.current ? "仍有未结执行或任务待决策" : "历史执行"}
          </p>
          <details>
            <summary>实际执行（{item.runs?.length ?? 0}）</summary>
            {item.runs?.map((execution) => (
              <div key={execution.run.run_id} className={styles.run}>
                <Button
                  variant="link"
                  onClick={() =>
                    navigate({ page: "rollout", id: execution.task_id, run: execution.run.run_id })
                  }
                >
                  {roles[execution.run.role] || execution.run.role} ·{" "}
                  {states[execution.run.observed] || execution.run.observed}
                </Button>
                {execution.pending_decision && <p>该任务有待处理决策</p>}
                <p>
                  Agent v{execution.pin.agent_revision} · 登记 r{execution.pin.inventory_revision} ·{" "}
                  {execution.pin.profile.model} · {execution.pin.profile.effort || "默认强度"}
                </p>
                <p>
                  {execution.pin.runtime.runtime_id} v{execution.pin.runtime.revision} · 快照{" "}
                  {execution.pin.snapshot_id}
                </p>
                <p>
                  关联证据：{sources[execution.source] || execution.source} · Run{" "}
                  {execution.run.run_id}
                </p>
              </div>
            ))}
          </details>
        </article>
      ))}
      <div className={styles.filters}>
        {!!cursors.length && (
          <Button disabled={result.loading} onClick={() => setCursors((v) => v.slice(0, -1))}>
            上一页关联
          </Button>
        )}
        {result.value?.more && (
          <Button
            disabled={result.loading}
            onClick={() => setCursors((v) => [...v, result.value!.next!])}
          >
            下一页关联
          </Button>
        )}
      </div>
    </section>
  );
}
