import type { IssueReadingState } from "./reading.ts";
import { Button, Badge, Section, EmptyState } from "../ui/index.ts";
import { navigate } from "../app/router.ts";
import { PageHeader } from "../shell/PageHeader.tsx";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { IssueAgentConversation, IssueView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Sheet } from "../ui/Modal.tsx";
import { AttentionDetails } from "../attention/Inbox.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { MessageAttachments } from "../conversation/Attachments.tsx";
import { MessageImages } from "../conversation/Images.tsx";
import { ConversationSessionProvider } from "../conversation/session.tsx";
import { Note } from "../shell/Chat.tsx";
import { Composer } from "../conversation/Composer.tsx";
import { PendingQueue } from "../conversation/PendingQueue.tsx";
import { Issues } from "./data.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { AssigneeName } from "./AssigneeName.tsx";
import { issueStateLabel } from "./Hierarchy.tsx";
import { workStatus, workStatusLabel } from "./boardModel.ts";
import { IssuePlanRead } from "./PlanRead.tsx";
import { IssueDefinition } from "./Definition.tsx";
import { IssueHandoff } from "./Handoff.tsx";
import { ManagerStatus } from "./ManagerStatus.tsx";
import { IssueHistory } from "./History.tsx";
import { IssueFollowupsDelivery, externalDeliveryStatus } from "./FollowupsDelivery.tsx";
import { CreateIssueTask } from "./CreateTask.tsx";
import { IssueActivity } from "./Activity.tsx";
import { ProgressEditor, IssueControlEditor } from "./Progress.tsx";
import { IssueResultsList, resultStatus } from "./Results.tsx";
import { TaskRunStatus } from "../rollout/RunRollout.tsx";
import { taskLook } from "../tasks/status.ts";
import styles from "./DetailPage.module.css";

type Panel =
  | "definition"
  | "child"
  | "task"
  | "handoff"
  | "sources"
  | "followups"
  | "history"
  | "progress"
  | "results"
  | "reopen"
  | "pause"
  | "resume";
export function IssueDetails({
  id,
  connection,
  reading,
  onBack,
  onSelect,
  onTask,
}: {
  id: string;
  connection: Connection;
  reading: IssueReadingState;
  onBack(): void;
  onSelect(id: string): void;
  onTask(id: string): void;
}) {
  const { client, live } = useServices();
  const reads = useMemo(() => new Issues(client), [client]);
  const state = useStore(reads.detail.state);
  const results = useStore(reads.results.state);
  const deliveries = useStore(reads.deliveries.state);
  const activityState = useStore(reads.activity.state);
  const childRead = useMemo(() => reads.childReader(id, true), [reads, id]);
  const childState = useStore(childRead.state);
  const restored = useRef(false);
  const initialTop = useRef(reading.top);
  const [panel, setPanel] = useState<Panel | null>(null);
  const [attention, setAttention] = useState("");
  const [side, setSide] = useState(false);
  const scroll = useRef<HTMLDivElement>(null);
  const conversationRead = useMemo(
    () => new WorkItemRead<IssueAgentConversation>(client),
    [client],
  );
  const conversationState = useStore(conversationRead.state);
  useEffect(() => {
    void reads.open(id);
    reads.children(id, reading.childCursor(), true);
    void reads.openResults(id);
    void reads.openDeliveries(id);
    const stop = live.subscribeEvents((event) => {
      if (event.object_type === "message") {
        const bound = reads.detail.state.get().value?.conversation?.conversation_id;
        if (!event.item || event.item.conversation_id === bound) void reads.activity.refresh();
      } else reads.refresh();
    });
    const reset = live.snapshot.subscribe(() => reads.refresh());
    return () => {
      stop();
      reset();
      reads.stop();
    };
  }, [reads, live, id, reading]);
  const issue = state.value;
  const conversationID = issue?.conversation?.conversation_id;
  const conversation =
    conversationState.value?.conversation_id === conversationID ? conversationState.value : null;
  const conversationError = conversationState.error ? "负责人对话暂时无法读取" : "";
  useEffect(() => {
    if (conversationID)
      void conversationRead.open(
        `/api/v1/agent-conversations/${encodeURIComponent(conversationID)}?projection=issues`,
      );
    return () => conversationRead.close();
  }, [conversationRead, conversationID]);
  const loaded =
    !!issue &&
    !!(activityState.value || activityState.error) &&
    !!(childState.value || childState.error) &&
    !!(results.value || results.error);
  useLayoutEffect(() => {
    if (loaded && scroll.current && !restored.current) {
      scroll.current.scrollTo({ top: initialTop.current });
      restored.current = true;
    }
  }, [loaded, reading]);
  const editable =
    !!issue && ["active", "paused"].includes(issue.state) && issue.assignment_state === "active";
  const close = () => {
    setPanel(null);
    void reads.open(id);
  };
  const saved = (newID?: string) => {
    close();
    if (newID && newID !== id) onSelect(newID);
    else void reads.open(id);
  };
  const jump = () => {
    reading.followLatest();
    scroll.current
      ?.querySelector("[data-activity-end]")
      ?.scrollIntoView({ block: "end", behavior: "smooth" });
  };
  const actionable =
    issue?.attentions?.items?.filter((a) => a.status === "open" && a.kind !== "information") ?? [];
  const execution = executionSummary(issue);
  const ended = !!issue && ["closed", "cancelled"].includes(issue.state);
  const statusLabel =
    issue &&
    (issue.state !== "active" ? issueStateLabel[issue.state] : workStatusLabel(workStatus(issue)));
  const deliveryCount =
    (results.value?.items?.length ?? 0) + (deliveries.value?.items?.length ?? 0);
  const recentDeliveries = [
    ...(results.value?.items ?? []).map((result) => ({
      id: `artifact:${result.artifact.artifact_id}`,
      title: result.title,
      status: resultStatus(result),
      at: result.created_at,
      panel: "results" as const,
    })),
    ...(deliveries.value?.items ?? []).map((item) => ({
      id: `external:${item.delivery.delivery_id}`,
      title: `${item.delivery.repository} · #${item.delivery.number}`,
      status: externalDeliveryStatus(item),
      at: item.observation.checked_at ?? item.observation.attempted_at ?? 0,
      panel: "followups" as const,
    })),
  ]
    .sort((a, b) => b.at - a.at)
    .slice(0, 3);
  const sideContent = issue && (
    <>
      {issue.manager && (
        <ManagerStatus
          manager={issue.manager}
          onTask={(task) => {
            setSide(false);
            onTask(task);
          }}
        />
      )}
      <section>
        <h2>属性</h2>
        <dl>
          <dt>负责人</dt>
          <dd>
            <AssigneeName assignee={issue.assignee} />
          </dd>
          <dt>状态</dt>
          <dd data-issue-status>{statusLabel}</dd>
          <dt>项目</dt>
          <dd>{issue.plan.project || "未分组"}</dd>
        </dl>
      </section>
      {!!issue.ancestors?.length && (
        <section>
          <h2>上级 Issue</h2>
          <Button variant="link" onClick={() => onSelect(issue.ancestors!.at(-1)!.issue_id)}>
            {issue.ancestors.at(-1)!.title} →
          </Button>
        </section>
      )}
      <section>
        <h2>交付物</h2>
        {results.error && (
          <ReadError label="交付读取失败" retry={() => void reads.results.refresh()} />
        )}
        {deliveries.error && (
          <ReadError label="外部交付读取失败" retry={() => void reads.deliveries.refresh()} />
        )}
        {!!deliveries.value?.items?.length && (
          <Button
            variant="link"
            onClick={() => {
              setSide(false);
              setPanel("followups");
            }}
          >
            外部交付 · {deliveries.value.items.length} 项 →
          </Button>
        )}
        <Button
          variant="link"
          onClick={() => {
            setSide(false);
            setPanel("results");
          }}
        >
          查看全部交付 ·{" "}
          {results.value && deliveries.value
            ? `${results.value.items?.length ?? 0}${results.value.more ? "+" : ""} 项`
            : "正在读取"}{" "}
          →
        </Button>
      </section>
      <section>
        <h2>执行记录</h2>
        {[...(issue.tasks ?? [])]
          .sort((a, b) => taskPriority(a) - taskPriority(b))
          .slice(0, 3)
          .map((task) => (
            <div key={task.task_id}>
              <Button
                variant="ghost"
                className={styles.resultLink}
                onClick={() => {
                  setSide(false);
                  onTask(task.task_id);
                }}
              >
                <span>{task.title}</span>
                <small>
                  {task.execution?.role === "accept"
                    ? "独立验收 · "
                    : task.execution?.role === "implement"
                      ? "实施 · "
                      : ""}
                  {taskLook(task).label}
                </small>
              </Button>
              <TaskRunStatus id={task.task_id} />
            </div>
          ))}
        {!issue.tasks?.length && <p className={styles.muted}>暂无任务</p>}
        {!!issue.tasks?.length && (
          <details>
            <summary>全部执行（{issue.tasks.length}）</summary>
            {issue.tasks.map((task) => (
              <Button
                variant="ghost"
                className={styles.resultLink}
                key={task.task_id}
                onClick={() => {
                  setSide(false);
                  onTask(task.task_id);
                }}
              >
                {task.title} · {taskLook(task).label}
              </Button>
            ))}
          </details>
        )}
      </section>
      <Button
        variant="ghost"
        className={styles.sideEntry}
        onClick={() => {
          setSide(false);
          setPanel("sources");
        }}
      >
        背景与原始输入 <span>›</span>
      </Button>
      <Button
        variant="ghost"
        className={styles.sideEntry}
        onClick={() => {
          setSide(false);
          setPanel("followups");
        }}
      >
        跟进与外部交付 <span>›</span>
      </Button>
      <Button
        variant="ghost"
        className={styles.sideEntry}
        onClick={() => {
          setSide(false);
          setPanel("history");
        }}
      >
        变更历史 <span>›</span>
      </Button>
    </>
  );
  return (
    <section className={styles.page} aria-label="Issue 详情">
      <Button
        variant="link"
        onClick={() => navigate({ page: "diagnostics", issue: id, tab: "events" })}
      >
        查看调度与事件诊断 →
      </Button>
      <header className={styles.toolbar}>
        <Button
          variant="ghost"
          className={styles.back}
          onClick={onBack}
          aria-label="返回 Issue 列表或上一项"
        >
          ← 返回
        </Button>
        <nav aria-label="Issue 路径">
          <Button variant="link" onClick={() => navigate({ page: "issues" })}>
            Issues
          </Button>
          {issue?.ancestors?.map((parent) => (
            <Button key={parent.issue_id} onClick={() => onSelect(parent.issue_id)}>
              / {parent.title}
            </Button>
          ))}
          <span>/ {issue?.plan.title ?? "正在读取"}</span>
        </nav>
        <span className={styles.spacer} />
        <Button className={styles.sideToggle} onClick={() => setSide(true)}>
          详情
        </Button>
        {issue && (
          <details className={styles.menu}>
            <summary aria-label="更多 Issue 操作">•••</summary>
            <div onClick={(event) => event.currentTarget.parentElement?.removeAttribute("open")}>
              <Button disabled={!editable} onClick={() => setPanel("progress")}>
                编辑当前进展
              </Button>
              <Button disabled={!editable} onClick={() => setPanel("definition")}>
                编辑定义
              </Button>
              <Button disabled={!editable} onClick={() => setPanel("child")}>
                新增子 Issue
              </Button>
              <Button
                disabled={!editable || issue.assignment_state !== "active"}
                onClick={() => setPanel("task")}
              >
                创建任务
              </Button>
              <Button onClick={() => setPanel("handoff")}>交接负责人</Button>
              {issue.state === "active" && (
                <Button onClick={() => setPanel("pause")}>暂停 Issue</Button>
              )}
              {issue.state === "paused" && (
                <Button onClick={() => setPanel("resume")}>恢复 Issue</Button>
              )}
              {["closed", "cancelled"].includes(issue.state) && (
                <Button onClick={() => setPanel("reopen")}>重新打开 Issue</Button>
              )}
            </div>
          </details>
        )}
      </header>
      {!issue ? (
        <div className={styles.loading} role="status">
          <PageHeader title="Issue 详情" />
          {state.error ? (
            <ReadError label="Issue 读取失败" retry={() => void reads.open(id)} />
          ) : (
            "正在读取 Issue…"
          )}
        </div>
      ) : (
        <div className={styles.columns}>
          <div className={styles.main}>
            <div
              ref={scroll}
              data-issue-scroll
              className={styles.scroll}
              onScroll={() => {
                if (scroll.current && restored.current) reading.saveTop(scroll.current.scrollTop);
              }}
            >
              <div className={styles.lane} data-issue-lane>
                {state.error && (
                  <ReadError
                    label="当前显示上次读取，尚未取得最新状态"
                    retry={() => void reads.open(id)}
                  />
                )}
                <PageHeader className={styles.titleHeader} title={issue.plan.title} />
                <p className={styles.metadata}>
                  <span className={styles.dot} />
                  {statusLabel}
                  <span>·</span>
                  <AssigneeName assignee={issue.assignee} />
                  负责<span>·</span>
                  <time>{new Date(issue.updated_at).toLocaleString("zh-CN")}</time>
                </p>
                <p className={styles.intent}>{issue.plan.intent}</p>
                {actionable[0] && (
                  <section className={styles.attention}>
                    <div>
                      <small>
                        {actionable[0].kind === "decision" ? "需要你决定" : "需要你处理"}
                      </small>
                      <h2>{actionable[0].title}</h2>
                      {actionable.length > 1 && (
                        <details>
                          <summary>另有 {actionable.length - 1} 项</summary>
                          {actionable.slice(1).map((item) => (
                            <Button
                              variant="link"
                              key={item.attention_id}
                              onClick={() => setAttention(item.attention_id)}
                            >
                              {item.title}
                            </Button>
                          ))}
                        </details>
                      )}
                    </div>
                    <Button
                      variant="primary"
                      onClick={() => setAttention(actionable[0]!.attention_id)}
                    >
                      {actionable[0].kind === "decision" ? "查看并决定" : "查看并处理"}
                    </Button>
                  </section>
                )}
                <section className={styles.progress}>
                  <div className={styles.sectionHead}>
                    <h2>当前进展</h2>
                    <Button
                      variant="link"
                      disabled={!editable}
                      onClick={() => setPanel("progress")}
                    >
                      编辑
                    </Button>
                  </div>
                  {issue.progress?.summary ? (
                    <Expandable text={issue.progress.summary} />
                  ) : (
                    <p className={styles.muted}>负责人尚未更新进展</p>
                  )}
                  {issue.progress?.next_step && <p>下一步：{issue.progress.next_step}</p>}
                  <p className={styles.muted}>
                    {execution}
                    {issue.progress && (
                      <>
                        {" "}
                        · 由 <AssigneeName assignee={issue.progress.updated_by} /> 更新 ·{" "}
                        {new Date(issue.progress.updated_at).toLocaleString("zh-CN")}
                      </>
                    )}
                  </p>
                  {issue.assignment_state !== "active" && (
                    <p role="status">正在交接负责人，当前执行事实仍予保留。</p>
                  )}
                </section>
                <details className={styles.definition}>
                  <summary>目标与完成条件</summary>
                  <IssuePlanRead issue={issue} />
                </details>
                <ChildIssues
                  issue={issue}
                  reads={reads}
                  reading={reading}
                  onSelect={onSelect}
                  onCreate={() => setPanel("child")}
                  editable={editable}
                />
                <Section title="交付物" className={styles.deliverySummary}>
                  {results.error && (
                    <ReadError label="交付读取失败" retry={() => void reads.results.refresh()} />
                  )}
                  {deliveries.error && (
                    <ReadError
                      label="外部交付读取失败"
                      retry={() => void reads.deliveries.refresh()}
                    />
                  )}
                  {recentDeliveries.map((item) => (
                    <Button
                      key={item.id}
                      variant="ghost"
                      className={styles.resultLink}
                      onClick={() => setPanel(item.panel)}
                    >
                      <span>{item.title}</span>
                      <Badge>{item.status}</Badge>
                    </Button>
                  ))}
                  {results.value && deliveries.value && !recentDeliveries.length && (
                    <EmptyState>暂无已登记交付</EmptyState>
                  )}
                  {!!deliveryCount && (
                    <Button variant="link" onClick={() => setPanel("results")}>
                      全部执行交付 →
                    </Button>
                  )}
                </Section>
                <div className={styles.sectionHead}>
                  <h2>讨论与进展</h2>
                  <Button variant="link" onClick={jump}>
                    到最新 ↓
                  </Button>
                </div>
                {conversation ? (
                  <ConversationSessionProvider conversation={conversation} connection={connection}>
                    <IssueActivity
                      issue={issue}
                      reads={reads}
                      reading={reading}
                      results={results.value?.items ?? []}
                      onIssue={onSelect}
                      onTask={onTask}
                      scroll={scroll}
                    />
                  </ConversationSessionProvider>
                ) : (
                  <IssueActivity
                    issue={issue}
                    reads={reads}
                    reading={reading}
                    results={results.value?.items ?? []}
                    onIssue={onSelect}
                    onTask={onTask}
                    scroll={scroll}
                  />
                )}
              </div>
            </div>
            <div className={styles.composer}>
              <div className={styles.composerLane} data-issue-footer>
                {ended && (
                  <div className={styles.ended} role="status">
                    <p>
                      {issue.state === "cancelled"
                        ? "Issue 已取消，对话记录仍可阅读。"
                        : "Issue 已结束，对话记录仍可阅读。"}
                    </p>
                    <Button variant="link" onClick={() => setPanel("reopen")}>
                      重新打开 Issue →
                    </Button>
                  </div>
                )}
                {!ended && (
                  <>
                    <Note />
                    {issue.conversation?.can_send && (
                      <div className={styles.recipient}>
                        发给 <AssigneeName assignee={issue.assignee} /> · 本 Issue 负责人
                      </div>
                    )}
                  </>
                )}
                {conversation ? (
                  <ConversationSessionProvider conversation={conversation} connection={connection}>
                    <PendingQueue connection={connection} />
                    {!ended && issue.conversation?.reason !== "owner" && (
                      <Composer
                        connection={connection}
                        disabled={!issue.conversation?.can_send}
                        presentation="issue"
                      />
                    )}
                  </ConversationSessionProvider>
                ) : !ended && issue.conversation?.can_send ? (
                  <p className={styles.muted} role="status">
                    {conversationError || "正在读取负责人对话…"}
                  </p>
                ) : null}
                {!ended && !issue.conversation?.can_send && (
                  <div className={styles.conversationReason}>
                    <p className={styles.muted}>
                      {issue.conversation?.reason === "owner"
                        ? "这个 Issue 由你负责，没有负责人 Agent 对话。"
                        : conversationError || conversationReason(issue)}
                    </p>
                    <Button variant="secondary" onClick={() => setPanel("handoff")}>
                      {issue.conversation?.reason === "owner" ? "交给 Agent 负责" : "交接负责人 →"}
                    </Button>
                  </div>
                )}
              </div>
            </div>
          </div>
          <aside className={styles.sidebar} aria-label="Issue 辅助信息">
            {sideContent}
          </aside>
        </div>
      )}
      {side && (
        <Sheet open title="Issue 辅助信息" onClose={() => setSide(false)}>
          <aside className={styles.drawer}>{sideContent}</aside>
        </Sheet>
      )}
      {attention && (
        <Sheet open title="Issue 待处理事项" onClose={() => setAttention("")}>
          <AttentionDetails id={attention} onIssue={onSelect} onBack={() => setAttention("")} />
        </Sheet>
      )}
      {issue && panel && (
        <Sheet open title={panelLabels[panel]} onClose={close}>
          <div className={styles.panel}>
            {panel === "definition" && (
              <IssueDefinition issue={issue} onBack={close} onSaved={saved} />
            )}
            {panel === "child" && <IssueDefinition parent={issue} onBack={close} onSaved={saved} />}
            {panel === "task" && (
              <CreateIssueTask
                issue={issue}
                onBack={close}
                onTask={(task) => {
                  close();
                  onTask(task);
                }}
              />
            )}
            {panel === "handoff" && (
              <IssueHandoff issue={issue} onBack={close} onChanged={() => void reads.open(id)} />
            )}
            {panel === "progress" && <ProgressEditor issue={issue} onSaved={() => saved()} />}
            {(panel === "reopen" || panel === "pause" || panel === "resume") && (
              <IssueControlEditor issue={issue} action={panel} onSaved={() => saved()} />
            )}
            {panel === "history" && <IssueHistory issue={issue} />}
            {panel === "followups" && <IssueFollowupsDelivery issue={issue} />}
            {panel === "sources" && <IssueSources id={id} reads={reads} />}
            {panel === "results" && (
              <>
                <IssueResultsList id={id} onTask={onTask} />
                <Button variant="link" onClick={() => setPanel("followups")}>
                  外部交付与观察 →
                </Button>
              </>
            )}
          </div>
        </Sheet>
      )}
    </section>
  );
}

const panelLabels: Record<Panel, string> = {
  definition: "编辑 Issue 定义",
  child: "新增子 Issue",
  task: "创建任务",
  handoff: "交接负责人",
  sources: "背景与原始输入",
  followups: "跟进与外部交付",
  history: "变更历史",
  progress: "编辑当前进展",
  results: "全部执行交付",
  reopen: "重新打开 Issue",
  pause: "暂停 Issue",
  resume: "恢复 Issue",
};
function conversationReason(issue: IssueView): string {
  return (
    (
      {
        owner: "当前由你负责；可交接给 Agent 后继续负责人对话。",
        handoff: "负责人交接中，草稿保留，暂不发送新消息。",
        ended: "Issue 已结束，对话记录仍可阅读。",
        unavailable: "负责人对话尚未建立。",
        manager_pending: "负责人任务正在等待领取；领取后可在此继续讨论。",
      } as Record<string, string>
    )[issue.conversation?.reason ?? "unavailable"] || "正在读取负责人对话…"
  );
}
function executionSummary(issue: IssueView | null): string {
  const counts = new Map<string, number>();
  for (const task of issue?.tasks ?? []) {
    const state = task.execution?.state;
    if (!state || ["idle", "completed", "cancelled"].includes(state)) continue;
    const label =
      (
        {
          unknown: "结果待核实",
          queued: "等待执行",
          starting: "正在启动",
          recovering: "恢复中",
          accepting: "独立验收中",
          waiting_owner: "等待你处理",
          paused: "已暂停执行",
        } as Record<string, string>
      )[state] ?? (task.execution?.role === "accept" ? "独立验收中" : "实施中");
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return [...counts].map(([label, count]) => `${count} 项${label}`).join(" · ") || "暂无活动执行";
}
function taskPriority(task: NonNullable<IssueView["tasks"]>[number]): number {
  return ["completed", "cancelled"].includes(task.lifecycle) ? 1 : 0;
}
export function ReadError({ label, retry }: { label: string; retry(): void }) {
  return (
    <p role="status" className={styles.error}>
      {label}。<Button onClick={retry}>重新读取</Button>
    </p>
  );
}
function Expandable({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const [clipped, setClipped] = useState(false);
  const paragraph = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    const node = paragraph.current;
    if (!node || open) return;
    const measure = () => setClipped(node.scrollHeight > node.clientHeight + 1);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [text, open]);
  return (
    <>
      <p ref={paragraph} className={open ? undefined : styles.clamp}>
        {text}
      </p>
      {(open || clipped) && (
        <Button variant="link" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "收起" : "展开全文"}
        </Button>
      )}
    </>
  );
}
function ChildIssues({
  issue,
  reads,
  reading,
  onSelect,
  onCreate,
  editable,
}: {
  issue: IssueView;
  reads: Issues;
  reading: IssueReadingState;
  onSelect(id: string): void;
  onCreate(): void;
  editable: boolean;
}) {
  const read = useMemo(() => reads.childReader(issue.issue_id, true), [reads, issue.issue_id]);
  useEffect(() => {
    reads.children(issue.issue_id, reading.childCursor(), true);
  }, [reads, issue.issue_id, reading]);
  const state = useStore(read.state);
  const [expanded, setExpanded] = useState(reading.children);
  const [cursors, setCursors] = useState(reading.childCursors);
  if (!state.value)
    return state.error ? (
      <ReadError label="子 Issue 读取失败" retry={() => void read.refresh()} />
    ) : null;
  if (!state.value.items?.length && cursors.length === 1)
    return state.error ? (
      <ReadError label="子 Issue 读取失败" retry={() => void read.refresh()} />
    ) : null;
  return (
    <section className={styles.children}>
      {state.error && (
        <ReadError label="子 Issue 尚未取得最新状态" retry={() => void read.refresh()} />
      )}
      <div className={styles.sectionHead}>
        <h2>子 Issue</h2>
        <Button variant="link" disabled={!editable} onClick={onCreate}>
          ＋ 新建
        </Button>
      </div>
      <div>
        {(state.value.items ?? []).slice(0, expanded ? undefined : 5).map((child) => (
          <Button
            variant="ghost"
            className={styles.child}
            key={child.issue_id}
            onClick={() => onSelect(child.issue_id)}
          >
            <span>{child.title}</span>
            <span className={styles.muted}>
              <AssigneeName assignee={child.assignee} />
            </span>
            <Badge>{issueStateLabel[child.state]}</Badge>
          </Button>
        ))}
      </div>
      {(state.value.items?.length ?? 0) > 5 && !expanded && (
        <Button
          variant="link"
          onClick={() => {
            setExpanded(true);
            reading.expandChildren();
          }}
        >
          查看全部子 Issue
        </Button>
      )}
      {expanded && cursors.length > 1 && (
        <Button
          onClick={() => {
            const previous = cursors.slice(0, -1);
            setCursors(previous);
            reading.saveChildCursors(previous);
            reads.children(issue.issue_id, previous.at(-1), true);
          }}
        >
          上一页子 Issue
        </Button>
      )}
      {expanded && state.value.more && (
        <Button
          onClick={() => {
            const next = [...cursors, state.value!.next!];
            setCursors(next);
            reading.saveChildCursors(next);
            reads.children(issue.issue_id, state.value?.next, true);
          }}
        >
          下一页子 Issue
        </Button>
      )}
    </section>
  );
}
function IssueSources({ id, reads }: { id: string; reads: Issues }) {
  const state = useStore(reads.context.state);
  useEffect(() => {
    void reads.openContext(id);
  }, [reads, id]);
  return (
    <>
      <h2>背景与原始输入</h2>
      {state.error && <ReadError label="来源读取失败" retry={() => void reads.context.refresh()} />}
      {!state.value && !state.error && <p>正在读取…</p>}
      {state.value?.sources?.map((item) => (
        <article className={styles.source} key={item.message_id}>
          <p className={styles.muted}>
            {item.author} · {new Date(item.created_at).toLocaleString("zh-CN")}
          </p>
          <Markdown text={item.body ?? ""} />
          {item.attachments?.length ? (
            <MessageAttachments attachments={item.attachments} />
          ) : (
            !!item.images?.length && <MessageImages images={item.images} />
          )}
        </article>
      ))}
      {state.value && !state.value.sources?.length && <p>暂无已记录来源。</p>}
      {state.value?.more && (
        <Button onClick={() => void reads.openContext(id, state.value?.next)}>更早来源</Button>
      )}
    </>
  );
}
