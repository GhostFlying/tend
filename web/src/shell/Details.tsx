import { RunConversation } from "../tasks/RunConversation.tsx";
import { useDurableOp } from "../app/useDurableOp.ts";
import type { ControlMode } from "../api/generated/api.ts";
import { RunRollout } from "../rollout/RunRollout.tsx";
// A task's ledger facts and revision-checked owner controls (D70).
import { useState, type ReactNode } from "react";
import type { TaskDetail, ArtifactView, AcceptanceView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { DecisionCard } from "../decisions/DecisionCard.tsx";
import { currentWork } from "../tasks/facts.ts";
import { TaskExecution } from "../tasks/Execution.tsx";
import { WorkspaceFacts } from "../tasks/WorkspaceFacts.tsx";
import { ArtifactDelivery } from "../tasks/ArtifactDelivery.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { useStore } from "../state/store.ts";
import {
  blockLabels,
  controlLabels,
  isFinished,
  lifecycleLabels,
  ratingLabels,
  taskLook,
} from "../tasks/status.ts";
import controls from "./controls.module.css";
import styles from "./Details.module.css";
import { DetailTabs } from "./DetailOverlay.tsx";
import { TaskInputs } from "../workitems/Materials.tsx";
import { Note } from "./Chat.tsx";
import { StatusDot } from "./StatusDot.tsx";

export function Details() {
  const { live } = useServices();
  const state = useStore(live.detail);
  const task = state?.task;
  return (
    <>
      <div className={styles.head}>
        {task && <StatusDot tone={taskLook(task).tone} />}
        <span className={styles.title}>{task?.title ?? "任务详情"}</span>
      </div>
      <div className={styles.body}>
        <Note />
        {state?.error && (
          <div role="alert">
            读取失败：{state.error}{" "}
            <button className={controls.link} onClick={() => void live.refreshDetail()}>
              重试
            </button>
          </div>
        )}
        {!task && !state?.error && (
          <p className={controls.empty}>
            {state ? "正在读取任务…" : "点任务行或回复里的任务标签，在这里查看任务。"}
          </p>
        )}
        {task && <TaskContent key={task.task_id} task={task} />}
      </div>
    </>
  );
}

function TaskContent({ task: t }: { task: TaskDetail }) {
  return t.kind === "issue_manager" ? (
    <ManagerTaskContent task={t} />
  ) : (
    <ExecutionTaskContent task={t} />
  );
}

function ManagerTaskContent({ task }: { task: TaskDetail }) {
  const { openIssue } = useServices();
  const contract =
    task.spec && typeof task.spec === "object" ? (task.spec as Record<string, unknown>) : {};
  return (
    <>
      <div className={styles.summary}>
        <strong>
          Issue 协调责任 ·{" "}
          {task.lifecycle === "running" ? "协调中" : lifecycleLabels[task.lifecycle]}
        </strong>
        <span>{task.manager?.category_id ?? "issue-manager"}</span>
      </div>
      <p>每轮协调结束后释放执行槽，等待新输入。责任随 Issue 收尾或交接结束。</p>
      <TaskExecution task={task} />
      <TaskControls task={task} />
      {task.manager && (
        <Section title="管理范围与会话">
          <button className={controls.chipBtn} onClick={() => openIssue(task.manager!.issue_id)}>
            打开 Issue 与聊天
          </button>
          {task.manager.conversation_id && <p>会话：{task.manager.conversation_id}</p>}
        </Section>
      )}
      {typeof contract.reason === "string" && (
        <Section title="责任要求">
          <Markdown text={contract.reason} />
        </Section>
      )}
      <Section title="协调回合">
        {!task.runs?.length && <p>等待 runner 领取。</p>}
        {task.runs?.map((run) => (
          <div key={run.run_id} className={styles.progress}>
            <b>协调 · 第 {run.generation} 轮</b>
            <span>{statusLabels[run.observed] ?? run.observed}</span>
            <small>{run.host}</small>
          </div>
        ))}
      </Section>
      <LogSection task={task} />
    </>
  );
}

function ExecutionTaskContent({ task: t }: { task: TaskDetail }) {
  const { followUp, openTask, openIssue } = useServices();
  const current = currentWork(t);
  const [tab, setTab] = useState(t.lifecycle === "completed" ? "delivery" : "execution");
  const spec = t.spec && typeof t.spec === "object" ? (t.spec as Record<string, unknown>) : null;
  const decisions = (t.decisions ?? []).filter((d) => d.status === "open");
  return (
    <>
      <div className={styles.summary}>
        <strong>任务阶段 · {lifecycleLabels[t.lifecycle]}</strong>
        <span>
          {t.project || "未分组"} · {ratingLabels[t.rating]} · spec {t.spec_version}
        </span>
        {t.control_mode !== "managed" && (
          <span className={styles.badge}>{controlLabels[t.control_mode]}</span>
        )}
      </div>
      <DetailTabs
        tabs={[
          { key: "delivery", label: "交付与验收" },
          { key: "inputs", label: "目标与输入" },
          { key: "execution", label: "执行与日志" },
          { key: "supplement", label: "补充" },
          { key: "workspace", label: "工作区" },
          { key: "history", label: "历史" },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "supplement" && <RunConversation task={t} controls={<TaskControls task={t} />} />}
      <div hidden={tab !== "execution"}>
        <TaskExecution task={t} />
      </div>
      {!!t.issue_ids?.length && (
        <Section title="所属Issue">
          {t.issue_ids.map((id) => (
            <button key={id} className={controls.chipBtn} onClick={() => openIssue(id)}>
              {id === t.primary_issue_id ? t.primary_issue_title || id : id}
            </button>
          ))}
        </Section>
      )}
      {!!t.work_item_ids?.some((id) => !t.issue_ids?.includes(id)) && (
        <Section title="历史关联">
          {t.work_item_ids
            .filter((id) => !t.issue_ids?.includes(id))
            .map((id) => (
              <code key={id}>{id} </code>
            ))}
        </Section>
      )}
      {t.block_reason !== "none" && (
        <Section title="当前阻塞">
          <p className={styles.problem}>{blockLabels[t.block_reason]}</p>
        </Section>
      )}
      {current.issues.length > 0 && (
        <Section title="当前验收问题">
          {current.issues.map((v) => (
            <div className={styles.problem} key={v.artifact_id}>
              <b>{verdictLabels[v.verdict]}</b>
              <IssueList value={v.issues} />
            </div>
          ))}
        </Section>
      )}
      {decisions.map((decision) => (
        <DecisionCard
          key={`${decision.decision_id}@${decision.version}`}
          decision={decision}
          task={t}
          more={<span>待你选择</span>}
        />
      ))}
      {tab !== "supplement" && <TaskControls task={t} />}
      {t.lifecycle === "completed" && (
        <div className={styles.followUp}>
          <button type="button" className={controls.chipBtn} onClick={() => followUp(t)}>
            跟进 / 修改
          </button>
          <span>回到聊天补充意向，确认后手动发送</span>
        </div>
      )}
      <div hidden={tab !== "delivery"}>
        {t.lifecycle === "completed" && (
          <Section title={`本版交付与验收 · spec ${t.spec_version}`}>
            <Artifacts artifacts={current.artifacts} verdicts={current.verdicts} />
          </Section>
        )}
      </div>

      <div hidden={tab !== "execution"}>
        {!!t.relations?.length && (
          <Section title="任务关系">
            <div className={styles.relations}>
              {t.relations.map((relation) => (
                <button
                  type="button"
                  className={styles.relation}
                  key={`${relation.kind}:${relation.direction}:${relation.task.task_id}`}
                  onClick={() => openTask(relation.task.task_id)}
                >
                  <span>
                    {relation.kind === "follow_up"
                      ? relation.direction === "outgoing"
                        ? "跟进自"
                        : "后续跟进"
                      : relation.direction === "outgoing"
                        ? "关联 · 本任务声明"
                        : "关联 · 对方声明"}
                  </span>
                  <strong>{relation.task.title}</strong>
                  <small>
                    {lifecycleLabels[relation.task.lifecycle]} · {relation.task.task_id}
                  </small>
                </button>
              ))}
            </div>
          </Section>
        )}
      </div>
      <div hidden={tab !== "inputs"}>
        <TaskInputs spec={t.spec} />
        {typeof spec?.goal === "string" && (
          <Section title="任务目标">
            <Markdown text={spec.goal} />
          </Section>
        )}
      </div>
      <div hidden={tab !== "delivery"}>
        {t.lifecycle !== "completed" && current.artifacts.length > 0 && (
          <Section title={`本版产物 · spec ${t.spec_version}`}>
            <Artifacts artifacts={current.artifacts} verdicts={current.verdicts} />
          </Section>
        )}
      </div>
      <div hidden={tab !== "workspace"}>
        <WorkspaceFacts task={t} />
      </div>

      <div hidden={tab !== "execution"}>
        <Section title="本版执行进展">
          {!t.runs?.some((r) => r.spec_version === t.spec_version) && (
            <p className={controls.empty}>暂无本版执行记录。</p>
          )}
          {t.runs
            ?.filter((r) => r.spec_version === t.spec_version)
            .slice(-3)
            .reverse()
            .map((r) => (
              <div className={styles.progress} key={r.run_id}>
                <b>
                  {r.role === "implement" ? "实现" : "验收"} · 第 {r.generation} 代
                </b>
                <span>{statusLabels[r.observed] ?? r.observed}</span>
                <small>{r.host}</small>
              </div>
            ))}
        </Section>
        <LogSection task={t} />
      </div>
      <div hidden={tab !== "inputs"}>
        <Fold title={`完整目标与验收条件 · spec ${t.spec_version}`}>
          {spec ? (
            Object.entries(spec).map(([key, value]) => (
              <div key={key}>
                <div className={styles.caption}>
                  {key === "goal" ? "目标" : (specLabels[key] ?? key)}
                </div>
                <SpecValue value={value} field={key} />
              </div>
            ))
          ) : (
            <pre className={styles.fact}>{text(t.spec)}</pre>
          )}
        </Fold>
      </div>
      <div hidden={tab !== "workspace"}>
        <Fold title="工作区与分支">
          <p>
            {t.workspace || "尚未分配"}
            {t.workspace_state &&
              ` · ${t.workspace_state.host} · ${workspaceLabels[t.workspace_state.status] ?? t.workspace_state.status}`}
          </p>
          {t.workspace_state?.mounts?.map((m) => (
            <div className={styles.mount} key={`${m.repo}:${m.path}`}>
              <dl className={styles.kv}>
                {m.repo === m.path ? (
                  <>
                    <dt>仓库 / 路径</dt>
                    <dd>
                      <code>{m.repo}</code>
                    </dd>
                  </>
                ) : (
                  <>
                    <dt>仓库</dt>
                    <dd>{m.repo}</dd>
                    <dt>相对路径</dt>
                    <dd>
                      <code>{m.path}</code>
                    </dd>
                  </>
                )}
                <dt>分支</dt>
                <dd>
                  <code>{m.branch}</code>
                </dd>
              </dl>
            </div>
          ))}
        </Fold>
      </div>
      <div hidden={tab !== "history"}>
        <Fold title="标识与原始记录">
          <dl className={styles.kv}>
            <dt>任务 ID</dt>
            <dd>
              <code>{t.task_id}</code>
            </dd>
            <dt>控制版本</dt>
            <dd>{t.control_revision}</dd>
            <dt>实现代次</dt>
            <dd>{t.next_implement_generation}</dd>
          </dl>
          <pre className={styles.fact}>
            {text({
              relations: t.relations,
              actions: t.actions,
              runs: t.runs,
              artifacts: t.artifacts,
              acceptances: t.acceptances,
              decisions: t.decisions,
            })}
          </pre>
        </Fold>
        <Fold title="历史产物与验收">
          <Artifacts artifacts={t.artifacts ?? []} verdicts={t.acceptances ?? []} historical />
        </Fold>
      </div>
    </>
  );
}

function TaskControls({ task: t }: { task: TaskDetail }) {
  const s = useServices();
  const operation = useDurableOp(s.client, `task-control:${t.task_id}`);
  const busy = operation.busy || operation.pending;
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const execute = async (action: ControlMode | "cancel" | "reconcile") => {
    try {
      const outcome = await operation.execute(
        action === "reconcile"
          ? undefined
          : {
              operation: action === "cancel" ? "tasks.cancel" : "control.set_mode",
              target: { type: "task", id: t.task_id },
              expected_control_revision: t.control_revision,
              payload: action === "cancel" ? {} : { mode: action },
            },
      );
      if (outcome.kind === "done" || outcome.kind === "stale") {
        void s.live.refreshDetail();
        void s.live.refreshSnapshot();
      }
      setResult(
        outcome.kind === "done"
          ? "操作已执行"
          : outcome.kind === "stale"
            ? "任务已被修改，已刷新。请确认最新状态后再操作。"
            : outcome.kind === "unknown"
              ? outcome.message
              : `操作未执行：${outcome.message}`,
      );
    } finally {
      setConfirmCancel(false);
    }
  };
  return (
    <>
      {operation.storageError && <p role="alert">{operation.storageError}</p>}
      {operation.pending && (
        <div className={styles.problem}>
          原管理操作待核实。
          <button
            className={controls.chipBtn}
            disabled={operation.busy}
            onClick={() => void execute("reconcile")}
          >
            核实原管理操作
          </button>
        </div>
      )}
      {!isFinished(t) && !operation.pending && (
        <div className={styles.actions}>
          {t.control_mode === "managed" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute("paused")}
            >
              暂停
            </button>
          )}
          {t.control_mode !== "managed" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute("managed")}
            >
              {t.control_mode === "human_control" ? "交还给 tend" : "恢复"}
            </button>
          )}
          {t.control_mode !== "human_control" && (
            <button
              disabled={busy}
              className={controls.chipBtn}
              onClick={() => void execute("human_control")}
            >
              接管
            </button>
          )}
          <button
            disabled={busy}
            className={`${controls.chipBtn} ${styles.danger}`}
            onClick={() => setConfirmCancel(true)}
          >
            取消任务
          </button>
          {confirmCancel && (
            <div className={styles.confirm}>
              <span>确认取消『{t.title}』？</span>
              <button
                disabled={busy}
                className={controls.chipBtn}
                onClick={() => void execute("cancel")}
              >
                确认取消
              </button>
              <button
                disabled={busy}
                className={controls.chipBtn}
                onClick={() => setConfirmCancel(false)}
              >
                继续任务
              </button>
            </div>
          )}
        </div>
      )}
      {result && !operation.pending && (
        <p role="status" className={styles.caption}>
          {result}
        </p>
      )}
    </>
  );
}

function LogSection({ task }: { task: TaskDetail }) {
  return <RunRollout runs={task.runs ?? []} />;
}

function Artifacts({
  artifacts,
  verdicts,
  historical = false,
}: {
  artifacts: readonly ArtifactView[];
  verdicts: readonly AcceptanceView[];
  historical?: boolean;
}) {
  return (
    <>
      {!artifacts.length && <p className={controls.empty}>暂无可关联到本版的产物。</p>}
      {artifacts.map((artifact) => (
        <div className={styles.artifact} key={artifact.artifact_id}>
          <ArtifactDelivery value={artifact.ref} />
          <ArtifactRef value={artifact.ref} />
          {verdicts
            .filter((v) => v.artifact_id === artifact.artifact_id)
            .map((v, i) => (
              <div className={styles.verdict} key={i}>
                <b>{verdictLabels[v.verdict]}</b>
                <span className={styles.caption}>
                  {" "}
                  · spec {v.spec_version}
                  {historical ? " · 历史记录" : ""}
                </span>
                {historical && <IssueList value={v.issues} />}
              </div>
            ))}
          <details>
            <summary>原始产物与证据</summary>
            <small>{artifact.artifact_id}</small>
            <pre className={styles.fact}>{text(artifact.ref)}</pre>
            {verdicts
              .filter((v) => v.artifact_id === artifact.artifact_id && v.evidence !== undefined)
              .map((v, i) => (
                <pre key={i} className={styles.fact}>
                  {text(v.evidence)}
                </pre>
              ))}
          </details>
        </div>
      ))}
    </>
  );
}
function ArtifactRef({ value }: { value: unknown }) {
  if (value && typeof value === "object") {
    const object = value as Record<string, unknown>;
    if (Array.isArray(object.mounts)) {
      const mounts = object.mounts.filter(
        (mount): mount is Record<string, unknown> =>
          !!mount &&
          typeof mount === "object" &&
          typeof (mount as Record<string, unknown>).commit_sha === "string",
      );
      if (mounts.length)
        return (
          <dl className={styles.commits}>
            {mounts.map((mount, index) => (
              <div key={index}>
                <dt>
                  {text(mount.repo)}
                  {mount.path !== mount.repo && ` · ${text(mount.path)}`}
                </dt>
                <dd>
                  <code>{text(mount.commit_sha)}</code>
                </dd>
                {typeof mount.snapshot_sha === "string" && mount.snapshot_sha && (
                  <dd>
                    未提交变更快照 <code>{mount.snapshot_sha}</code>
                  </dd>
                )}
              </div>
            ))}
          </dl>
        );
    }
    const commits = object.commits;
    if (commits && typeof commits === "object" && !Array.isArray(commits))
      return (
        <dl className={styles.commits}>
          {Object.entries(commits).map(([repo, sha]) => (
            <div key={repo}>
              <dt>{repo}</dt>
              <dd>
                <code>{text(sha)}</code>
              </dd>
            </div>
          ))}
        </dl>
      );
  }
  if (value && typeof value === "object" && ("report" in value || "files" in value)) return null;
  return <pre className={styles.fact}>{text(value)}</pre>;
}
function IssueList({ value }: { value: unknown }) {
  if (Array.isArray(value))
    return value.length ? (
      <ul className={styles.checks}>
        {value.map((issue, i) => (
          <li key={i}>
            <IssueList value={issue} />
          </li>
        ))}
      </ul>
    ) : (
      <p className={styles.caption}>无问题。</p>
    );
  if (typeof value === "string") return <Markdown text={value} />;
  if (value && typeof value === "object")
    return (
      <dl className={styles.kv}>
        {Object.entries(value).map(([key, body]) => (
          <div className={styles.issueField} key={key}>
            <dt>{issueLabels[key] ?? key}</dt>
            <dd>{text(body)}</dd>
          </div>
        ))}
      </dl>
    );
  return value === null || value === undefined ? null : <span>{text(value)}</span>;
}
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h2>{title}</h2>
      {children}
    </section>
  );
}
function Fold({ title, children }: { title: string; children: ReactNode }) {
  return (
    <details className={styles.fold}>
      <summary>{title}</summary>
      <div>{children}</div>
    </details>
  );
}
function SpecValue({ value, field }: { value: unknown; field?: string }) {
  const deliverableLabels: Record<string, string> = {
    commit: "提交代码",
    report: "报告",
    files: "文件",
  };
  if (field === "deliverables" && Array.isArray(value))
    return (
      <p>
        {value
          .map((v) => (typeof v === "string" ? (deliverableLabels[v] ?? v) : text(v)))
          .join("、")}
      </p>
    );
  if (Array.isArray(value) && value.every((item) => typeof item === "string"))
    return (
      <ul className={styles.checks}>
        {value.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </ul>
    );
  return <pre className={styles.fact}>{value === "commit" ? "提交代码" : text(value)}</pre>;
}
function text(value: unknown): string {
  return typeof value === "string" ? value : (JSON.stringify(value, null, 2) ?? "—");
}
const workspaceLabels: Record<string, string> = {
  ready: "就绪",
  creating: "创建中",
  unavailable: "不可用",
  unknown: "未知",
};
const specLabels: Record<string, string> = {
  delivery: "交付方式",
  deliverables: "交付要求",
  acceptance: "验收条件",
  constraints: "约束",
  context: "上下文",
  non_goals: "范围以外",
};
const statusLabels: Record<string, string> = {
  ready: "就绪",
  dispatched: "已派发",
  done: "已完成",
  superseded: "已替代",
  waiting: "等待中",
  interrupted: "已中断",
  pending: "待执行",
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  queued: "排队中",
  succeeded: "成功",
  accepted: "已接受",
  unknown: "状态未知",
  stopped: "已停止",
  created: "已创建",
  starting: "启动中",
  working: "工作中",
  disconnected: "连接已断开",
  finished: "已结束",
};
const verdictLabels: Record<string, string> = {
  passed: "验收通过",
  changes_required: "需要修改",
  blocked: "验收受阻",
  inconclusive: "验收无结论",
};

const issueLabels: Record<string, string> = {
  severity: "严重程度",
  message: "问题",
  description: "说明",
  file: "文件",
  line: "行",
  suggestion: "建议",
  code: "编号",
  title: "标题",
};
