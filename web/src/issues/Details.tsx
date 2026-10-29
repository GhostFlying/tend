import { IssuePlanRead } from "./PlanRead.tsx";
import type { IssueView } from "../api/generated/api.ts";
import { MessageImages } from "../conversation/Images.tsx";
import { IssueTaskPeek } from "./TaskPeek.tsx";
import { IssueFollowupsDelivery } from "./FollowupsDelivery.tsx";
import { AssigneeName } from "./AssigneeName.tsx";
import { useEffect, useState } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { DetailTabs } from "../shell/DetailOverlay.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { MessageAttachments } from "../conversation/Attachments.tsx";
import { IssueHierarchy, issueStateLabel } from "./Hierarchy.tsx";
import { CreateIssueTask } from "./CreateTask.tsx";
import { IssueDefinition } from "./Definition.tsx";
import { IssueHandoff } from "./Handoff.tsx";
import styles from "./Issues.module.css";

type Tab = "overview" | "hierarchy" | "sources" | "followups" | "history";
export function IssueDetails({
  id,
  onSelect,
  onTask,
  onInbox,
}: {
  id: string;
  onSelect(id: string): void;
  onTask(id: string): void;
  onInbox(id?: string): void;
}) {
  const { live } = useServices();
  const state = useStore(live.issues.detail.state);
  const [tab, setTab] = useState<Tab>("hierarchy");
  const [handoff, setHandoff] = useState(false);
  const [creatingTask, setCreatingTask] = useState(false);
  const [editing, setEditing] = useState(false);
  const [child, setChild] = useState(false);
  useEffect(() => {
    void live.issues.open(id);
  }, [live, id]);
  const context = live.issues.context;
  const sources = useStore(context.state);
  useEffect(() => {
    if (tab === "sources" || tab === "hierarchy") void live.issues.openContext(id);
    return () => context.close();
  }, [context, live, id, tab, state.value?.context_revision]);
  const issue = state.value;
  if (!issue)
    return (
      <div className={styles.body}>
        <p role="status">{state.error ? "Issue读取失败，尚未取得最新记录。" : "正在读取…"}</p>
        <button onClick={() => void live.issues.detail.refresh()}>重新读取</button>
      </div>
    );
  if (editing || child)
    return (
      <IssueDefinition
        {...(editing ? { issue } : { parent: issue })}
        onBack={() => {
          setEditing(false);
          setChild(false);
        }}
        onSaved={(newID) => {
          setEditing(false);
          setChild(false);
          if (newID) onSelect(newID);
        }}
      />
    );
  if (creatingTask)
    return <CreateIssueTask issue={issue} onBack={() => setCreatingTask(false)} onTask={onTask} />;
  if (handoff) return <IssueHandoff issue={issue} onBack={() => setHandoff(false)} />;
  return (
    <section className={styles.root} aria-label="Issue详情">
      <header className={styles.header}>
        <div>
          <p className={styles.muted}>Issue · {issueStateLabel[issue.state]}</p>
          <h1>{issue.plan.title}</h1>
          <p>
            负责人：
            <AssigneeName assignee={issue.assignee} />{" "}
            {issue.assignee.kind === "agent" && (
              <button
                className={styles.button}
                onClick={() => {
                  window.dispatchEvent(
                    new CustomEvent("tend:open-agent", { detail: `work-item:${id}` }),
                  );
                }}
              >
                与负责人对话
              </button>
            )}
          </p>
        </div>
        <span className={styles.spacer} />
        <button
          className={styles.button}
          disabled={!["active", "paused"].includes(issue.state)}
          onClick={() => setEditing(true)}
        >
          编辑定义
        </button>
        <button className={styles.button} onClick={() => setHandoff(true)}>
          交接负责人
        </button>
      </header>
      <DetailTabs<Tab>
        tabs={[
          { key: "overview", label: "概览" },
          { key: "hierarchy", label: "层级与任务" },
          { key: "sources", label: "背景与输入" },
          { key: "followups", label: "跟进与交付" },
          { key: "history", label: "历史" },
        ]}
        value={tab}
        onChange={setTab}
      />
      <div className={styles.body}>
        {state.error && <p className={styles.warning}>当前显示上次读取，暂时无法取得最新状态。</p>}
        <div className={styles.goal}>目标：{issue.plan.intent}</div>
        {(issue.attentions?.items ?? [])
          .filter((a) => a.status === "open")
          .map((a) => (
            <div className={styles.warning} key={a.attention_id}>
              {a.kind === "decision" ? "需要你决定" : "需要你处理"}：{a.title}{" "}
              <button onClick={() => onInbox(a.attention_id)}>查看待处理</button>
            </div>
          ))}
        {tab === "hierarchy" && (
          <>
            <h3>Issue层级</h3>
            <button
              className={styles.button}
              disabled={!["active", "paused"].includes(issue.state)}
              onClick={() => setChild(true)}
            >
              新增子Issue
            </button>
            <IssueHierarchy items={[]} current={issue} onSelect={onSelect} />
            <h3>任务与执行</h3>
            <button
              className={styles.button}
              disabled={
                !["active", "paused"].includes(issue.state) || issue.assignment_state !== "active"
              }
              onClick={() => setCreatingTask(true)}
            >
              创建任务
            </button>
            <div className={styles.tasks}>
              {(issue.tasks ?? []).map((task, i) => (
                <IssueTaskPeek key={task.task_id} task={task} initial={i === 0} onTask={onTask} />
              ))}
              {!issue.tasks?.length && <p className={styles.muted}>暂无任务。</p>}
            </div>
          </>
        )}
        {tab === "followups" && <IssueFollowupsDelivery issue={issue} />}
        {tab === "hierarchy" &&
          sources.value?.sources?.some(
            (item) => item.attachments?.length || item.images?.length,
          ) && (
            <section>
              <h3>相关来源</h3>
              {sources.value.sources
                .filter((item) => item.attachments?.length || item.images?.length)
                .slice(0, 3)
                .map((item) => (
                  <div key={item.message_id}>
                    {item.attachments?.length ? (
                      <MessageAttachments attachments={item.attachments} />
                    ) : (
                      !!item.images?.length && <MessageImages images={item.images} />
                    )}
                  </div>
                ))}
            </section>
          )}
        {tab === "overview" && (
          <>
            <IssuePlanRead issue={issue} />
            <p className={styles.muted}>关闭前核对完成条件、子项责任与相关任务验收。</p>
          </>
        )}
        {tab === "sources" && (
          <>
            {sources.error ? (
              <p className={styles.warning}>
                背景与输入读取失败。<button onClick={() => void context.refresh()}>重新读取</button>
              </p>
            ) : !sources.value ? (
              <p>正在读取…</p>
            ) : (
              <>
                {sources.value.sources?.length === 0 && (
                  <p className={styles.muted}>暂无已记录来源。</p>
                )}
                {(sources.value.sources ?? []).map((item) => (
                  <article key={item.message_id}>
                    <p className={styles.muted}>
                      {item.author} · {item.created_at}
                    </p>
                    <Markdown text={item.body ?? ""} />
                    {item.attachments?.length ? (
                      <MessageAttachments attachments={item.attachments} />
                    ) : (
                      !!item.images?.length && <MessageImages images={item.images} />
                    )}
                  </article>
                ))}
                {sources.value.more && (
                  <button
                    className={styles.button}
                    onClick={() => void live.issues.openContext(id, sources.value?.next)}
                  >
                    更早来源
                  </button>
                )}
              </>
            )}
          </>
        )}
        {tab === "history" && <IssueHistory key={id} issue={issue} />}
      </div>
    </section>
  );
}
function IssueHistory({ issue }: { issue: IssueView }) {
  const { live } = useServices();
  const read = live.issues.history;
  const state = useStore(read.state);
  const [selection, setSelection] = useState(String(issue.revision));
  const revision = Number(selection);
  const valid = Number.isInteger(revision) && revision >= 1 && revision <= issue.revision;
  useEffect(() => {
    if (valid) void live.issues.openHistory(issue.issue_id, revision);
    return () => read.close();
  }, [live, read, issue.issue_id, revision, valid]);
  const value = state.value;
  const matching = value?.issue_id === issue.issue_id && value.revision === revision;
  return (
    <>
      <h3>定义历史</h3>
      <label>
        版本
        <input
          type="number"
          min={1}
          max={issue.revision}
          value={selection}
          onChange={(event) => setSelection(event.target.value)}
        />
      </label>
      {matching ? (
        <article>
          <h3>{value.plan.title}</h3>
          <p>版本 {value.revision}</p>
          <IssuePlanRead issue={value} showIntent />
        </article>
      ) : state.error ? (
        <p role="status">
          历史读取失败。<button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      ) : (
        <p className={styles.muted}>{valid ? "正在读取定义历史…" : "选择有效版本查看。"}</p>
      )}
    </>
  );
}
