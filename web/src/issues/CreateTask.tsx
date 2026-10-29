import { runtimeLabel } from "../agents/runtimeLabel.ts";
import { useEffect, useMemo, useState } from "react";
import type {
  AgentRequirement,
  CreateIssueTaskPayload,
  IssueView,
  TaskDetail,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import { WorkItemRead } from "../workitems/live.ts";
import type { OpOutcome } from "../app/ops.ts";
import { issueCommand } from "./actions.ts";
import styles from "../agents/Catalog.module.css";
import layout from "./CreateTask.module.css";

export function CreateIssueTask({
  issue,
  onBack,
  onTask,
}: {
  issue: IssueView;
  onBack(): void;
  onTask(id: string): void;
}) {
  const { client, live } = useServices();
  const snapshot = useStore(live.snapshot);
  const categories = useStore(live.agents.categories.state);
  const runner = useStore(live.agents.runner.state);
  const repos = useStore(live.repos.state);
  const operation = useDurableOp(client, `issue-create-task:${issue.issue_id}`);
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [acceptance, setAcceptance] = useState("");
  const [delivery, setDelivery] = useState("report");
  const [reuse, setReuse] = useState(!!issue.plan.default_workspace_task_id);
  const [repo, setRepo] = useState("");
  const [host, setHost] = useState("");
  const [implementation, setImplementation] = useState<AgentRequirement>({
    category_id: "implementer",
  });
  const [review, setReview] = useState<AgentRequirement>({ category_id: "reviewer" });
  const [result, setResult] = useState<OpOutcome | null>(null);
  const [created, setCreated] = useState("");
  const [submitted, setSubmitted] = useState<CreateIssueTaskPayload | undefined>();
  const source = useMemo(() => new WorkItemRead<TaskDetail>(client), [client]);
  const workspace = useStore(source.state);
  useEffect(() => {
    void live.agents.openCategories();
    void live.repos.refresh();
  }, [live]);
  useEffect(() => {
    if (reuse && issue.plan.default_workspace_task_id)
      void source.open(`/api/v1/tasks/${encodeURIComponent(issue.plan.default_workspace_task_id)}`);
    return () => source.close();
  }, [source, reuse, issue.plan.default_workspace_task_id]);
  const fixedHost = reuse ? workspace.value?.workspace_state?.host : undefined;
  const effectiveHost = fixedHost ?? host;
  useEffect(() => {
    if (effectiveHost) void live.agents.openRunner(effectiveHost);
  }, [effectiveHost, live]);
  const original = operation.original?.payload as CreateIssueTaskPayload | undefined;
  const submit = async (reconcile = false) => {
    try {
      const payload: Omit<CreateIssueTaskPayload, "expected_assignment_epoch"> = {
        title,
        spec: {
          goal,
          deliverables: [delivery],
          acceptance: acceptance.split("\n").filter((line) => line.trim()),
          agent_selection: { implementation, acceptance: review },
          workspace: reuse
            ? { mode: "reuse", source_task_id: issue.plan.default_workspace_task_id!, mounts: [] }
            : { mounts: [{ repo }] },
          requirements: effectiveHost ? { host_id: effectiveHost } : {},
        },
        ...(reuse ? { use_default_workspace: true } : {}),
      };
      setSubmitted(
        reconcile ? original : { ...payload, expected_assignment_epoch: issue.assignment_epoch },
      );
      const out = await operation.execute(
        reconcile ? undefined : issueCommand(issue, "create_task", payload),
      );
      setResult(out);
      if (out.kind === "done") {
        live.issues.refresh();
        if (out.result && typeof out.result === "object" && "task_id" in out.result)
          setCreated(String(out.result.task_id));
      }
    } catch {
      setResult({ kind: "unknown", message: "创建结果待核实，请核实原请求。" });
    }
  };
  const blocked = operation.busy || operation.pending;
  if (created)
    return (
      <section className={styles.root}>
        <h1>任务已创建</h1>
        <h2>{submitted?.title ?? title}</h2>
        <p>目标：{submitted?.spec.goal ?? goal}</p>
        <section className={layout.receipt}>
          <h3>已保存的任务要求</h3>
          <p>完成条件：</p>
          <ul>
            {submitted?.spec.acceptance?.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
          <div className={layout.roles}>
            {(["implementation", "acceptance"] as const).map((role) => {
              const requirement = submitted?.spec.agent_selection?.[role];
              return (
                <div key={role}>
                  <h3>{role === "implementation" ? "实施" : "独立验收"}</h3>
                  <p>类别：{requirement?.category_id}</p>
                  <p>运行时：{requirement?.runtime_id || "自动选择"}</p>
                  <p>
                    模型：{requirement?.model || "不限"} · effort：{requirement?.effort || "不限"}
                  </p>
                </div>
              );
            })}
          </div>
          <p>
            工作区：
            {submitted?.spec.workspace?.mode === "reuse"
              ? `复用任务 ${submitted.spec.workspace.source_task_id}`
              : `新建 · ${submitted?.spec.workspace?.mounts?.map((m) => m.repo).join("、") || "未记录"}`}
          </p>
          <p>执行主机：{submitted?.spec.requirements?.host_id || "自动选择"}</p>
          <p className={styles.muted}>实际分配的工作区、主机与Agent见任务详情。</p>
        </section>
        <div className={styles.actions}>
          <button className={styles.primary} onClick={() => onTask(created)}>
            查看任务
          </button>
          <button className={styles.button} onClick={onBack}>
            返回Issue
          </button>
        </div>
      </section>
    );
  return (
    <section className={`${styles.root} ${styles.form} ${layout.root}`} aria-label="创建任务">
      <h1>创建任务</h1>
      <p>{issue.plan.title}</p>
      <div className={layout.content}>
        <div className={layout.intro}>
          <label>
            标题
            <input
              value={original?.title ?? title}
              disabled={blocked}
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          <label>
            目标
            <textarea
              value={original?.spec.goal ?? goal}
              disabled={blocked}
              onChange={(e) => setGoal(e.target.value)}
            />
          </label>
          <label>
            完成条件（每行一项）
            <textarea
              value={original?.spec.acceptance?.join("\n") ?? acceptance}
              disabled={blocked}
              onChange={(e) => setAcceptance(e.target.value)}
            />
          </label>
          <label>
            交付方式
            <select
              value={original?.spec.deliverables?.[0] ?? delivery}
              disabled={blocked}
              onChange={(e) => setDelivery(e.target.value)}
            >
              <option value="report">报告</option>
              <option value="commit">代码提交</option>
              <option value="files">文件</option>
            </select>
          </label>
        </div>
        <h2>执行要求</h2>
        <div className={layout.roles}>
          <RoleRequirements
            label="实施"
            value={original?.spec.agent_selection?.implementation ?? implementation}
            onChange={setImplementation}
            blocked={blocked}
            categories={(categories.value?.items ?? [])
              .filter((c) => c.state === "active")
              .map((c) => ({ id: c.category_id, title: c.title }))}
            runtimes={
              effectiveHost && runner.value?.host_id === effectiveHost
                ? (runner.value?.projection?.runtimes ?? []).map((r) => ({
                    id: r.runtime_id,
                    title: `${runtimeLabel(r)} · ${r.runtime_id}`,
                  }))
                : []
            }
          />
          <RoleRequirements
            label="独立验收"
            value={original?.spec.agent_selection?.acceptance ?? review}
            onChange={setReview}
            blocked={blocked}
            categories={(categories.value?.items ?? [])
              .filter((c) => c.state === "active")
              .map((c) => ({ id: c.category_id, title: c.title }))}
            runtimes={
              effectiveHost && runner.value?.host_id === effectiveHost
                ? (runner.value?.projection?.runtimes ?? []).map((r) => ({
                    id: r.runtime_id,
                    title: `${runtimeLabel(r)} · ${r.runtime_id}`,
                  }))
                : []
            }
          />
        </div>
        <div className={layout.workspace}>
          <label>
            工作区
            <select
              value={reuse ? "default" : "new"}
              disabled={blocked}
              onChange={(e) => setReuse(e.target.value === "default")}
            >
              <option value="new">新建工作区</option>
              {issue.plan.default_workspace_task_id && (
                <option value="default">复用Issue默认工作区</option>
              )}
            </select>
          </label>
          {reuse ? (
            <p>
              工作区：
              {workspace.value?.workspace_state?.name ?? workspace.value?.workspace ?? "正在读取…"}
            </p>
          ) : (
            <label>
              仓库
              <select value={repo} disabled={blocked} onChange={(e) => setRepo(e.target.value)}>
                <option value="">选择已登记仓库</option>
                {repos.view?.repos
                  ?.filter((r) => !r.retired)
                  .map((r) => (
                    <option value={r.repo} key={r.repo}>
                      {r.repo}
                    </option>
                  ))}
              </select>
            </label>
          )}
          <label>
            执行主机
            <select
              value={fixedHost ?? host}
              disabled={blocked || !!fixedHost || reuse}
              onChange={(e) => setHost(e.target.value)}
            >
              <option value="">
                {reuse
                  ? workspace.value
                    ? "默认工作区尚未绑定主机"
                    : "读取固定主机…"
                  : "自动选择"}
              </option>
              {fixedHost && <option value={fixedHost}>{fixedHost}</option>}
              {snapshot?.hosts
                ?.filter((h) => h.host_id !== fixedHost)
                .map((h) => (
                  <option key={h.host_id} value={h.host_id}>
                    {h.host_id}
                    {h.online ? "" : "（离线）"}
                  </option>
                ))}
            </select>
          </label>
        </div>
        <p className={styles.muted}>
          {reuse ? "复用工作区固定主机。" : "选择具体主机后仅在该主机运行。"}{" "}
          实施与独立验收安排在同一主机和工作区，分别使用独立run；要求仅筛选Agent，不修改本机配置。
        </p>
        {reuse && workspace.value && !fixedHost && (
          <p className={styles.warning}>
            默认任务尚无已绑定工作区。可以改选新建工作区，或稍后重新读取。
            <button onClick={() => void source.refresh()}>重新读取</button>
          </p>
        )}
        {workspace.error && reuse && (
          <p className={styles.warning}>
            工作区信息读取失败，请重新读取。
            <button onClick={() => void source.refresh()}>重新读取</button>
          </p>
        )}
        {operation.storageError && <p role="alert">{operation.storageError}</p>}
        {result && (
          <p role="status" className={result.kind === "done" ? styles.muted : styles.warning}>
            {result.kind === "done" ? "任务已创建。" : result.message}
          </p>
        )}
      </div>
      <div className={`${styles.actions} ${layout.footer}`}>
        {operation.pending ? (
          <button
            className={styles.primary}
            disabled={operation.busy}
            onClick={() => void submit(true)}
          >
            核实原创建请求
          </button>
        ) : (
          <button
            className={styles.primary}
            disabled={
              blocked ||
              !title.trim() ||
              !goal.trim() ||
              !acceptance.trim() ||
              (reuse ? !fixedHost : !repo)
            }
            onClick={() => void submit()}
          >
            创建任务
          </button>
        )}
        <button className={styles.button} onClick={onBack}>
          返回Issue
        </button>
      </div>
    </section>
  );
}
function RoleRequirements({
  label,
  value,
  onChange,
  blocked,
  categories,
  runtimes,
}: {
  label: string;
  value: AgentRequirement;
  onChange(value: AgentRequirement): void;
  blocked: boolean;
  categories: { id: string; title: string }[];
  runtimes: { id: string; title: string }[];
}) {
  return (
    <fieldset disabled={blocked}>
      <legend>{label}（执行此角色任务的Agent）</legend>
      <label>
        类别
        <select
          value={value.category_id}
          onChange={(e) => onChange({ ...value, category_id: e.target.value })}
        >
          {!categories.some((c) => c.id === value.category_id) && (
            <option value={value.category_id}>{value.category_id}</option>
          )}
          {categories.map((c) => (
            <option value={c.id} key={c.id}>
              {c.title} · {c.id}
            </option>
          ))}
        </select>
      </label>
      <label>
        运行时
        <select
          value={value.runtime_id ?? ""}
          onChange={(e) => onChange({ ...value, runtime_id: e.target.value })}
        >
          <option value="">自动选择运行时</option>
          {value.runtime_id && !runtimes.some((r) => r.id === value.runtime_id) && (
            <option value={value.runtime_id}>
              {value.runtime_id}（当前主机目录未列出，仍保留原要求）
            </option>
          )}
          {runtimes.map((r) => (
            <option value={r.id} key={r.id}>
              {r.title}
            </option>
          ))}
        </select>
      </label>
      <label>
        模型要求（可选）
        <input
          value={value.model ?? ""}
          placeholder="不限"
          onChange={(e) => onChange({ ...value, model: e.target.value })}
        />
      </label>
      <label>
        effort要求（可选）
        <input
          value={value.effort ?? ""}
          placeholder="不限"
          onChange={(e) => onChange({ ...value, effort: e.target.value })}
        />
      </label>
    </fieldset>
  );
}
