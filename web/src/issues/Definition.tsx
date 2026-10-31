import { useEffect, useState } from "react";
import type {
  IssuePlan,
  Envelope,
  IssueView,
  UpdateIssuePayload,
  CreateIssuePayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import { createIssueCommand, issueCommand } from "./actions.ts";
import type { OpOutcome } from "../app/ops.ts";
import styles from "../agents/Catalog.module.css";
import layout from "./Definition.module.css";
import { AssigneeName } from "./AssigneeName.tsx";

export function IssueDefinition({
  issue,
  parent,
  sourceMessageID,
  sourceRevision,
  sourceDescription,
  onBack,
  onSaved,
}: {
  issue?: IssueView;
  parent?: IssueView;
  sourceMessageID?: string;
  sourceRevision?: number;
  sourceDescription?: string;
  onBack(): void;
  onSaved(id: string): void;
}) {
  const { client, live } = useServices();
  const [baseline, setBaseline] = useState(issue);
  const [restored, setRestored] = useState<Readonly<Envelope> | null>(null);
  const categories = useStore(live.agents.categories.state);
  const activeCategories = (categories.value?.items ?? []).filter(
    (category) => category.state === "active",
  );
  const connection = useStore(client.connection);
  const [title, setTitle] = useState(baseline?.plan.title ?? "");
  const [project, setProject] = useState(
    baseline?.plan.project ?? parent?.plan.project ?? "default",
  );
  const [intent, setIntent] = useState(baseline?.plan.intent ?? "");
  const [scope, setScope] = useState(baseline?.plan.scope ?? "");
  const [criteria, setCriteria] = useState(baseline?.plan.completion_criteria ?? [""]);
  const [constraints, setConstraints] = useState(
    baseline?.plan.background
      ?.filter((f) => f.kind === "user_constraint")
      .map((f) => f.text)
      .join("\n") ?? "",
  );
  const [assignee, setAssignee] = useState("owner");
  const [result, setResult] = useState<OpOutcome | null>(null);
  const operation = useDurableOp(
    client,
    issue
      ? `issue-definition:${issue.issue_id}`
      : `issue-create:${parent?.issue_id ?? sourceMessageID ?? "root"}`,
  );
  const original = operation.original?.payload as
    UpdateIssuePayload | CreateIssuePayload | undefined;
  const originalCreate = !issue ? (original as CreateIssuePayload | undefined) : undefined;
  const shownAssignee = originalCreate?.manager
    ? `category:${originalCreate.manager.category_id ?? "issue-manager"}`
    : originalCreate?.assignee
      ? originalCreate.assignee.kind === "owner"
        ? "owner"
        : originalCreate.assignee.ref
      : assignee;
  const shown = original?.plan;
  const restoredPayload = restored?.payload as UpdateIssuePayload | undefined;
  const editPlan = restoredPayload?.plan ?? baseline?.plan;
  const request = operation.original;
  if (
    request &&
    request.operation === "issues.update" &&
    request.request_id !== restored?.request_id
  ) {
    const payload = request.payload as UpdateIssuePayload;
    // Capture a newly restored request once, before a later receipt can clear it.
    setRestored(request);
    setTitle(payload.plan.title);
    setProject(payload.plan.project);
    setIntent(payload.plan.intent);
    setScope(payload.plan.scope ?? "");
    setCriteria(payload.plan.completion_criteria ?? [""]);
    setConstraints(
      payload.plan.background
        ?.filter((fact) => fact.kind === "user_constraint")
        .map((fact) => fact.text)
        .join("\n") ?? "",
    );
  }
  useEffect(() => {
    if (!issue) void live.agents.openCategories();
  }, [live, issue]);
  const save = async (reconcile = false) => {
    const old = editPlan;
    const plan: IssuePlan = {
      ...(old ?? { stages: [] }),
      title,
      project,
      intent,
      scope,
      completion_criteria: criteria.filter((x) => x.trim()),
      background: [
        ...(old?.background?.filter((f) => f.kind !== "user_constraint") ?? []),
        ...constraints
          .split("\n")
          .filter((x) => x.trim())
          .map(
            (text) =>
              old?.background?.find((f) => f.kind === "user_constraint" && f.text === text) ?? {
                kind: "user_constraint" as const,
                source: "owner",
                text,
              },
          ),
      ],
    };
    try {
      const out = await operation.execute(
        reconcile
          ? undefined
          : restored && restoredPayload
            ? {
                operation: restored.operation,
                target: restored.target,
                ...(restored.expected_control_revision === undefined
                  ? {}
                  : { expected_control_revision: restored.expected_control_revision }),
                payload: { ...restoredPayload, plan },
              }
            : issue
              ? issueCommand(baseline!, "update", {
                  plan,
                  task_dispositions: (baseline?.tasks ?? [])
                    ?.filter((t) => !["completed", "cancelled"].includes(t.lifecycle))
                    .map((t) => ({
                      task_id: t.task_id,
                      spec_version: t.spec_version,
                      action: "continue_existing",
                      reason: "更新Issue定义，保留当前任务原约定",
                    })),
                })
              : createIssueCommand({
                  plan,
                  ...(parent
                    ? {
                        parent_issue_id: parent.issue_id,
                        expected_parent_revision: parent.revision,
                      }
                    : {}),
                  ...(sourceMessageID
                    ? {
                        source_message_id: sourceMessageID,
                        expected_source_revision: sourceRevision,
                      }
                    : {}),
                  ...(assignee.startsWith("category:")
                    ? {
                        manager: {
                          category_id: assignee.slice("category:".length),
                          expected_assignment_epoch: 1,
                          reason: "按类别委派 Issue 持续协调责任",
                        },
                      }
                    : {
                        assignee: {
                          kind: "owner",
                          ref: connection.status === "online" ? connection.login : "",
                        },
                      }),
                }),
      );
      setResult(out);
      if (out.kind === "done") {
        live.issues.refresh();
        const value = out.result as { issue_id?: string };
        onSaved(value?.issue_id ?? issue?.issue_id ?? "");
      }
      if (out.kind === "stale") live.issues.refresh();
    } catch {
      setResult({ kind: "unknown", message: "保存结果待核实，请保留原输入。" });
    }
  };
  const blocked = operation.busy || operation.pending;
  const displayedCriteria = shown?.completion_criteria ?? criteria;
  const previousConstraints =
    editPlan?.background
      ?.filter((fact) => fact.kind === "user_constraint")
      .map((fact) => fact.text)
      .join("\n") ?? "";
  const changes: { label: string; before: string; after: string }[] = [];
  for (const [label, before, after] of [
    ["标题", editPlan?.title ?? "", title],
    ["目标", editPlan?.intent ?? "", intent],
    ["范围", editPlan?.scope ?? "", scope],
    ["约束", previousConstraints, constraints],
  ]) {
    if (before !== after) changes.push({ label: label!, before: before!, after: after! });
  }
  const oldCriteria = editPlan?.completion_criteria ?? [];
  const newCriteria = criteria.filter((value) => value.trim());
  for (let index = 0; index < Math.max(oldCriteria.length, newCriteria.length); index++) {
    const before = oldCriteria[index] ?? "";
    const after = newCriteria[index] ?? "";
    if (before !== after) changes.push({ label: `完成条件 ${index + 1}`, before, after });
  }
  const editRevision = restored?.expected_control_revision ?? baseline?.revision;
  const updated = !!issue && editRevision !== undefined && issue.revision !== editRevision;
  const reloadDefinition = () => {
    if (!issue) return;
    setRestored(null);
    setBaseline(issue);
    setTitle(issue.plan.title);
    setProject(issue.plan.project);
    setIntent(issue.plan.intent);
    setScope(issue.plan.scope ?? "");
    setCriteria(issue.plan.completion_criteria ?? [""]);
    setConstraints(
      issue.plan.background
        ?.filter((fact) => fact.kind === "user_constraint")
        .map((fact) => fact.text)
        .join("\n") ?? "",
    );
    setResult(null);
  };
  return (
    <section className={layout.root} aria-label={issue ? "编辑Issue定义" : "新建Issue"}>
      <h2>{issue ? "编辑Issue定义" : parent ? "新增子Issue" : "新建Issue"}</h2>
      <p className={styles.muted}>
        {baseline?.plan.title ??
          (parent ? `上级Issue：${parent.plan.title}` : "定义目标与完成条件，由负责人持续推进。")}
      </p>
      {sourceDescription && <p className={layout.source}>{sourceDescription}</p>}
      {updated && !operation.pending && (
        <div className={layout.conflict} role="status">
          <p>Issue已有新版本。你的草稿仍保留，保存将按编辑起始版本核对。</p>
          <button className={styles.button} disabled={operation.busy} onClick={reloadDefinition}>
            载入最新定义并替换草稿
          </button>
        </div>
      )}
      <div className={layout.columns}>
        <div className={layout.fields}>
          <label>
            标题
            <input
              value={shown?.title ?? title}
              disabled={blocked}
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          <label>
            项目
            <input
              value={shown?.project ?? project}
              disabled={blocked || !!issue}
              onChange={(e) => setProject(e.target.value)}
            />
          </label>
          <label>
            目标
            <textarea
              value={shown?.intent ?? intent}
              disabled={blocked}
              onChange={(e) => setIntent(e.target.value)}
            />
          </label>
          <label>
            范围
            <textarea
              value={shown?.scope ?? scope}
              disabled={blocked}
              onChange={(e) => setScope(e.target.value)}
            />
          </label>
          <div className={layout.field}>
            <span>完成条件</span>
            <div>
              {displayedCriteria.map((value, index) => (
                <div className={layout.criterion} key={index}>
                  <input
                    aria-label={`完成条件 ${index + 1}`}
                    value={value}
                    disabled={blocked}
                    onChange={(e) =>
                      setCriteria((old) => old.map((v, i) => (i === index ? e.target.value : v)))
                    }
                  />
                  <button
                    aria-label={`移除完成条件 ${index + 1}`}
                    disabled={blocked}
                    onClick={() => setCriteria((old) => old.filter((_, i) => i !== index))}
                  >
                    ×
                  </button>
                </div>
              ))}
              <button
                className={layout.add}
                disabled={blocked}
                onClick={() => setCriteria((old) => [...old, ""])}
              >
                ＋ 添加完成条件
              </button>
            </div>
          </div>
          <label>
            约束（每行一项）
            <textarea
              value={
                shown?.background
                  ?.filter((f) => f.kind === "user_constraint")
                  .map((f) => f.text)
                  .join("\n") ?? constraints
              }
              disabled={blocked}
              onChange={(e) => setConstraints(e.target.value)}
            />
          </label>
          {!issue && (
            <>
              <label>
                负责人
                <select
                  disabled={blocked}
                  value={shownAssignee}
                  onChange={(e) => setAssignee(e.target.value)}
                >
                  <option value="owner">你</option>
                  {shownAssignee !== "owner" &&
                    !activeCategories.some(
                      (category) => `category:${category.category_id}` === shownAssignee,
                    ) && <option value={shownAssignee}>原请求选择：{shownAssignee}</option>}
                  {activeCategories.map((category) => (
                    <option key={category.category_id} value={`category:${category.category_id}`}>
                      {category.title} · {category.category_id}（runner 领取）
                    </option>
                  ))}
                </select>
              </label>
              {categories.value?.more && (
                <button
                  type="button"
                  onClick={() => void live.agents.openCategories("active", categories.value?.next)}
                >
                  下一页类别
                </button>
              )}
              {categories.error && (
                <p role="status">
                  类别读取失败，保留已读取结果。
                  <button type="button" onClick={() => void live.agents.categories.refresh()}>
                    重新读取
                  </button>
                </p>
              )}
            </>
          )}
          {issue && (
            <div className={layout.field}>
              <span>负责人</span>
              <div>
                <AssigneeName assignee={issue.assignee} />
                <p className={styles.muted}>调整责任请返回使用“交接负责人”。</p>
              </div>
            </div>
          )}
        </div>
        <aside className={layout.changes} aria-label="本次变更">
          <h2>本次变更</h2>
          {operation.pending ? (
            <p>原保存请求待核实，按已提交内容保留。</p>
          ) : changes.length ? (
            <ul>
              {changes.map((change) => (
                <li key={change.label}>
                  <strong>
                    {change.before ? (change.after ? "修改" : "删除") : "新增"}
                    {change.label}
                  </strong>
                  {change.before && <p>原：{change.before}</p>}
                  {change.after && <p>新：{change.after}</p>}
                </li>
              ))}
            </ul>
          ) : (
            <p className={styles.muted}>尚未修改定义。</p>
          )}
          <p className={styles.muted}>保存后保留定义变更记录。</p>
          <p className={styles.muted}>Issue完成条件用于责任收尾；任务仍独立实施与验收。</p>
        </aside>
      </div>
      {(operation.storageError || (result && result.kind !== "done")) && (
        <p role="status" className={styles.warning}>
          {operation.storageError || (result && result.kind !== "done" && result.message)}
        </p>
      )}
      <footer className={layout.footer}>
        <p>更新Issue定义；已开始的任务仍按原约定执行。</p>
        <div className={styles.actions}>
          {operation.pending ? (
            <button
              className={styles.primary}
              disabled={operation.busy}
              onClick={() => void save(true)}
            >
              核实原保存请求
            </button>
          ) : (
            <button
              className={styles.primary}
              disabled={blocked || !title.trim() || !project.trim() || !intent.trim()}
              onClick={() => void save()}
            >
              {issue ? "保存定义" : "创建Issue"}
            </button>
          )}
          <button className={styles.button} onClick={onBack}>
            返回
          </button>
        </div>
      </footer>
    </section>
  );
}
