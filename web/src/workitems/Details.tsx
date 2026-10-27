import { useEffect, useState, type ReactNode } from "react";
import type { Connection } from "../api/client.ts";
import type {
  DeliveryObservation,
  WorkItemDelivery,
  WorkItemEvidence,
  WorkItemView,
} from "../api/generated/api.ts";
import { runOp } from "../app/ops.ts";
import { notify } from "../app/notice.ts";
import type { WorkItemContext } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { taskLook } from "../tasks/status.ts";
import { ExecutionSummary, TaskExecution } from "../tasks/Execution.tsx";
import {
  closureLabels,
  columnLabel,
  deliveryLabels,
  evidenceLabel,
  observationErrorLabel,
  factLabels,
  followOnLabels,
  parallelStages,
  refreshReconciled,
  stageProgressLabel,
  stageNeedsReview,
  stageRows,
} from "./model.ts";
import { DetailTabs } from "../shell/DetailOverlay.tsx";
import { Materials, OriginalInputs } from "./Materials.tsx";
import { uniqueTasks } from "./progress.ts";
import { isFinished } from "../tasks/status.ts";
import materialStyles from "./Materials.module.css";
import { ChoiceCard } from "./Choice.tsx";
import { Followups } from "./Followups.tsx";
import { WorkItemProgress } from "./Progress.tsx";
import controls from "../shell/controls.module.css";
import styles from "./WorkItems.module.css";

export function WorkItemDetails({ connection }: { connection: Connection }) {
  const { live, client } = useServices();

  const current = useStore(live.workItems.detail.state);
  const history = useStore(live.workItems.history.state);
  const [historical, setHistorical] = useState(false);
  const [revision, setRevision] = useState("1");
  const state = historical ? history : current;
  const item = state.value;
  const [context, setContext] = useState<WorkItemContext | null>(null);
  useEffect(() => {
    if (!item) return;
    const abort = new AbortController();
    void client
      .get<WorkItemContext>(
        `/api/v1/work-items/${encodeURIComponent(item.work_item_id)}/context`,
        abort.signal,
      )
      .then(setContext)
      .catch(() => {
        if (!abort.signal.aborted) setContext(null);
      });
    return () => abort.abort();
  }, [client, item]);
  const [chosenTab, setTab] = useState<string | null>(null);
  const [reviewVisited, setReviewVisited] = useState(false);
  const tab =
    chosenTab ??
    (item && uniqueTasks(item).some((task) => !isFinished(task)) ? "stages" : "review");
  return (
    <>
      <div className={`${styles.detailHead} ${styles.ledgerHead}`}>
        <div>
          <span className={styles.meta}>
            工作项 · {item ? columnLabel(item.column) : "正在读取"}
          </span>
          <h2>{item?.title ?? "工作项详情"}</h2>
          {context && (
            <details>
              <summary>
                上下文 ·{" "}
                {(
                  {
                    initializing: "初始化中",
                    synced: "已同步",
                    needs_review: "待核对",
                    recovering: "正在恢复",
                  } as Record<string, string>
                )[context.sync_status] ?? context.sync_status}
              </summary>
              <p>
                版本 {context.revision} · 消息水位 {context.watermark}
              </p>
              <p>检查范围：{context.coverage || "尚未登记"}</p>
              <p>已知缺口：{context.gaps || "尚未登记"}</p>
            </details>
          )}
          {item && (
            <button
              className={controls.link}
              onClick={() => {
                void runOp(client, {
                  operation: "conversation.open_background",
                  target: { type: "work_item", id: item.work_item_id },
                  payload: {},
                })
                  .then((r) => {
                    if (r.kind !== "done") {
                      notify(r.message, "warn");
                      return;
                    }
                    const result = r.result as { conversation_id: string };
                    window.dispatchEvent(
                      new CustomEvent("tend:open-agent", { detail: result.conversation_id }),
                    );
                  })
                  .catch((e) => notify(String(e), "warn"));
              }}
            >
              打开后台 Agent ↗
            </button>
          )}
        </div>
      </div>
      <DetailTabs
        tabs={[
          { key: "review", label: "评审材料" },
          { key: "stages", label: "阶段与任务" },
          { key: "background", label: "背景与输入" },
          { key: "followups", label: "跟进与交付" },
          { key: "history", label: "历史" },
        ]}
        value={tab}
        onChange={(next) => {
          if (tab === "review" || next === "review") setReviewVisited(true);
          setTab(next);
        }}
      />
      <div className={styles.detailBody}>
        {connection.status !== "online" && (
          <p className={styles.notice} role="status">
            等待连接恢复 · 显示最后读取的事实
          </p>
        )}
        {historical && (
          <p className={styles.notice}>
            历史计划与留存依据；不包含当前执行和观察状态。{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => {
                setHistorical(false);
                live.workItems.history.close();
              }}
            >
              回到当前计划
            </button>
          </p>
        )}
        {state.error && (
          <p role="alert">
            读取失败：{state.error}{" "}
            <button
              className={controls.link}
              onClick={() =>
                void (historical ? live.workItems.history : live.workItems.detail).refresh()
              }
            >
              重试详情
            </button>
          </p>
        )}
        {!item && !state.error && <p className={styles.empty}>正在读取工作项…</p>}
        {item && (
          <>
            <div hidden={tab !== "review"}>
              <div className={materialStyles.review}>
                {(tab === "review" || reviewVisited) &&
                  (historical ? <OriginalInputs item={item} /> : <Materials item={item} />)}
                <aside className={materialStyles.decisions} aria-label="下一步安排">
                  {!historical &&
                    item.choices
                      ?.filter((choice) => choice.status === "open")
                      .map((choice) => <ChoiceCard key={choice.choice_id} choice={choice} />)}
                  {!historical && <DraftActions item={item} />}
                </aside>
              </div>
            </div>
            <div hidden={tab !== "stages"}>
              {!historical && (
                <WorkItemProgress key={item.work_item_id} item={item}></WorkItemProgress>
              )}
              {!historical &&
                !!item.follow_ons?.some(
                  (next) => next.kind === "problem" && next.status === "open",
                ) && (
                  <Section title="当前阻碍">
                    {item.follow_ons
                      .filter((next) => next.kind === "problem" && next.status === "open")
                      .map((next) => (
                        <div key={next.follow_on_id} className={styles.notice}>
                          <Markdown text={next.text} />
                        </div>
                      ))}
                  </Section>
                )}
              <div>
                <Section title="阶段与执行">
                  {item.stages?.length ? (
                    <StageList item={item} />
                  ) : (
                    <p className={styles.empty}>阶段尚未安排，仍保持跟进。</p>
                  )}
                </Section>
              </div>
            </div>
            <div hidden={tab !== "background"}>
              <div>
                {tab === "background" && <OriginalInputs item={item} />}
                <Section title="目标">
                  <Markdown text={item.intent} />
                </Section>
                {!!item.background?.length && (
                  <Section title="共享背景">
                    {item.background.map((fact, index) => (
                      <div key={index} className={styles.fact}>
                        <span className={styles.meta}>{factLabels[fact.kind] ?? fact.kind}</span>
                        <Markdown text={fact.text} />
                        <p className={styles.source}>来源：{fact.source}</p>
                      </div>
                    ))}
                  </Section>
                )}
              </div>
            </div>
            <div hidden={tab !== "followups"}>
              {!historical && <DraftActions item={item} />}
              {!historical && (
                <Followups key={item.work_item_id} item={item} connection={connection} />
              )}
              <Section title="后续事项">
                {item.follow_ons?.length ? (
                  item.follow_ons.map((next) => (
                    <div className={styles.fact} key={next.follow_on_id}>
                      <p className={styles.meta}>
                        {followOnLabels[next.kind] ?? next.kind} ·{" "}
                        {followOnLabels[next.status] ?? next.status}
                      </p>
                      <Markdown text={next.text} />
                      <p className={styles.source}>来源：{next.source}</p>
                      {next.transferred_to && (
                        <p className={styles.source}>
                          承接：
                          <Markdown text={next.transferred_to} />
                        </p>
                      )}
                      <Evidence items={next.evidence} />
                    </div>
                  ))
                ) : (
                  <p className={styles.empty}>暂无已记录的后续事项；本轮完成不会自动结束跟进。</p>
                )}
              </Section>
              <Section title="外部交付">
                {item.deliveries?.length ? (
                  <Deliveries item={item} readOnly={historical || connection.status !== "online"} />
                ) : (
                  <p className={styles.empty}>尚未关联交付对象。</p>
                )}
              </Section>
              {item.default_workspace_task_id && (
                <Section title="默认工作区来源">
                  <TaskButton item={item} id={item.default_workspace_task_id} />
                </Section>
              )}
              {item.closure && (
                <Section title={`收尾 · ${closureLabels[item.closure.kind] ?? item.closure.kind}`}>
                  <Markdown text={item.closure.reason} />
                  <Evidence items={item.closure.evidence} />
                </Section>
              )}
            </div>
            <div hidden={tab !== "history"}>
              {!!item.choices?.some((choice) => choice.status !== "open") && (
                <Section title="历史安排">
                  {item.choices
                    .filter((choice) => choice.status !== "open")
                    .map((choice) => (
                      <ChoiceCard key={choice.choice_id} choice={choice} readOnly />
                    ))}
                </Section>
              )}
              <details className={styles.history}>
                <summary>工作项标识与修订</summary>
                <p className={styles.meta}>
                  {item.project || "未分组"} · 修订 {item.revision} ·{" "}
                  <code>{item.work_item_id}</code>
                </p>
              </details>
              {!historical && (
                <details className={styles.history}>
                  <summary>查看计划历史</summary>
                  <label>
                    修订{" "}
                    <input
                      aria-label="工作项历史修订"
                      type="number"
                      min={1}
                      max={current.value?.revision ?? 1}
                      value={revision}
                      onChange={(event) => setRevision(event.target.value)}
                    />
                  </label>
                  <button
                    type="button"
                    className={controls.chipBtn}
                    disabled={
                      !Number.isInteger(Number(revision)) ||
                      Number(revision) < 1 ||
                      Number(revision) > (current.value?.revision ?? 1)
                    }
                    onClick={() => {
                      setHistorical(true);
                      setTab("background");
                      void live.workItems.history.open(
                        `/api/v1/work-items/${encodeURIComponent(item.work_item_id)}?revision=${Number(revision)}`,
                      );
                    }}
                  >
                    读取历史计划
                  </button>
                </details>
              )}
            </div>
          </>
        )}
      </div>
    </>
  );
}
function DraftActions({ item }: { item: WorkItemView }) {
  const { discussWorkItem } = useServices();
  return (
    <div className={styles.drafts}>
      <div className={styles.actions}>
        <button
          type="button"
          className={controls.chipBtn}
          onClick={() => discussWorkItem(item, "discuss")}
        >
          继续讨论
        </button>
        {item.status === "closed" ? (
          <button
            type="button"
            className={controls.chipBtn}
            onClick={() => discussWorkItem(item, "reopen")}
          >
            提出重新跟进
          </button>
        ) : (
          <>
            <button
              type="button"
              className={controls.chipBtn}
              onClick={() => discussWorkItem(item, item.status === "snoozed" ? "resume" : "snooze")}
            >
              {item.status === "snoozed" ? "提出恢复安排" : "提出暂缓安排"}
            </button>
            <button
              type="button"
              className={controls.chipBtn}
              onClick={() => discussWorkItem(item, "close")}
            >
              讨论结束跟进
            </button>
          </>
        )}
      </div>
      <p className={styles.meta}>仅填入聊天草稿，由你补充并发送。暂缓新领取不会停止已运行任务。</p>
    </div>
  );
}
export function StageList({ item, compact = false }: { item: WorkItemView; compact?: boolean }) {
  const stages = item.stages ?? [];
  const parallel = parallelStages(stages);
  const titles = new Map(stages.map((stage) => [stage.stage_id, stage.title]));
  return (
    <>
      {!!item.pending_reviews?.length && (
        <p className={styles.meta}>
          有 {item.pending_reviews.length} 项已完成执行等待阶段核对。任务完成不代表工作项结束。
        </p>
      )}
      <ol className={styles.stages}>
        {stageRows(stages).map(({ stage, depth }) => (
          <li
            key={stage.stage_id}
            className={styles.stage}
            style={{ marginInlineStart: `${Math.min(depth, 4) * 12}px` }}
          >
            <div className={styles.stageHeading}>
              <strong>{stage.title}</strong>
              <span>
                计划 ·{" "}
                {stageProgressLabel(
                  stage,
                  item.stage_execution?.find((entry) => entry.stage_id === stage.stage_id)
                    ?.execution,
                  stageNeedsReview(item, stage.stage_id),
                )}
              </span>
            </div>
            <ExecutionSummary
              counts={
                item.stage_execution?.find((entry) => entry.stage_id === stage.stage_id)?.execution
              }
            />
            {stage.parent_id && (
              <p className={styles.meta}>属于 {titles.get(stage.parent_id) ?? stage.parent_id}</p>
            )}
            {!!stage.depends_on?.length && (
              <p className={styles.meta}>
                依赖：{stage.depends_on.map((id) => titles.get(id) ?? id).join("、")}
              </p>
            )}
            {parallel.has(stage.stage_id) && (
              <p className={styles.parallel} title="阶段依赖允许并行；执行仍由调度器分配资源">
                计划可并行
              </p>
            )}
            {stage.reason && <p>{stage.reason}</p>}
            {!!stage.task_ids?.length && (
              <div className={styles.taskLinks}>
                {stage.task_ids.map((id) => (
                  <TaskButton key={id} id={id} item={item} />
                ))}
              </div>
            )}
            {!compact && <Evidence items={stage.evidence} />}
          </li>
        ))}
      </ol>
    </>
  );
}
function TaskButton({ item, id }: { item: WorkItemView; id: string }) {
  const { openTask } = useServices();
  const task = item.tasks?.find((task) => task.task_id === id);
  return (
    <div className={styles.linkedTask}>
      <button type="button" className={styles.taskLink} onClick={() => openTask(id)}>
        {task?.title ?? id}
        {task && <span>{taskLook(task).label}</span>}
      </button>
      {task && <TaskExecution task={task} showState={false} />}
    </div>
  );
}
function Evidence({ items }: { items: readonly WorkItemEvidence[] | null | undefined }) {
  if (!items?.length) return null;
  return (
    <details className={styles.evidence}>
      <summary>依据 · {items.length}</summary>
      {items.map((item, index) => (
        <div key={index}>
          <Markdown text={item.summary} />
          {item.upgrade && (
            <div className={styles.source}>
              <p>
                升级批次 {item.upgrade.upgrade_id} · 修订 {item.upgrade.revision}
              </p>
              <p>
                发布 {item.upgrade.release_id} · 核验于{" "}
                {new Date(item.upgrade.checked_at).toLocaleString()}
              </p>
              <p>
                Manifest <code>{item.upgrade.manifest_sha256}</code>
              </p>
              <p>
                Server <code>{item.upgrade.server_sha256}</code>
              </p>
              <ul>
                {item.upgrade.hosts?.map((host) => (
                  <li key={host.host_id}>
                    {host.host_id} ·{" "}
                    {host.status === "completed"
                      ? "已更新"
                      : host.status === "skipped"
                        ? "已符合目标版本"
                        : host.status}{" "}
                    · <code>{host.artifact_sha256}</code>
                  </li>
                ))}
              </ul>
              <p>此依据仅证明上述发布范围完成，不代表产品功能已通过验收。</p>
            </div>
          )}
          {item.observation && (
            <p className={styles.source}>
              留存观察：{deliveryLabels[item.observation.state] ?? item.observation.state}
              {item.observation.head_sha ? ` · Head ${item.observation.head_sha}` : ""}
              {item.observation.merge_sha ? ` · Merge ${item.observation.merge_sha}` : ""}
              {item.observation.checked_at
                ? ` · ${new Date(item.observation.checked_at).toLocaleString()}`
                : ""}
            </p>
          )}
          <p className={styles.source}>
            {evidenceLabel(item.kind)} · {item.reference}
          </p>
        </div>
      ))}
    </details>
  );
}
function externalURL(value: string | undefined): string | undefined {
  try {
    const url = new URL(value ?? "");
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : undefined;
  } catch {
    return undefined;
  }
}
function Deliveries({ item, readOnly }: { item: WorkItemView; readOnly: boolean }) {
  return (
    <>
      {item.deliveries?.map((delivery) => {
        const observed = item.observations?.find(
          (observation) => observation.delivery_id === delivery.delivery_id,
        );
        const url = externalURL(observed?.url ?? delivery.url);
        return (
          <div className={styles.fact} key={delivery.delivery_id}>
            <div className={styles.deliveryHeading}>
              {url ? (
                <a href={url} target="_blank" rel="noopener noreferrer">
                  {delivery.repository} #{delivery.number} ↗
                </a>
              ) : (
                <strong>
                  {delivery.repository} #{delivery.number}
                </strong>
              )}
              <span>{deliveryLabels[observed?.state ?? "unknown"] ?? observed?.state}</span>
            </div>
            <p className={styles.source}>
              {delivery.provider}
              {delivery.host_id ? ` · ${delivery.host_id}` : ""}
            </p>
            {observed?.head_sha && (
              <p className={styles.source}>
                Head <code>{observed.head_sha}</code>
              </p>
            )}
            {observed?.merge_sha && (
              <p className={styles.source}>
                Merge <code>{observed.merge_sha}</code>
              </p>
            )}
            <p className={styles.meta}>
              {observed?.checked_at
                ? `最近核验 ${new Date(observed.checked_at).toLocaleString()}`
                : "尚无成功观察"}
            </p>
            {observed?.error_kind && (
              <p className={styles.warning}>
                本次观察失败（{observationErrorLabel(observed.error_kind)}），保留上次事实。
                {observed.attempted_at
                  ? ` 尝试于 ${new Date(observed.attempted_at).toLocaleString()}`
                  : ""}
              </p>
            )}
            {observed?.refresh_pending && <p className={styles.meta}>等待重新观察</p>}
            <DeliveryRefreshControl
              item={item}
              delivery={delivery}
              observed={observed}
              readOnly={readOnly}
            />
          </div>
        );
      })}
    </>
  );
}
function DeliveryRefreshControl({
  item,
  delivery,
  observed,
  readOnly,
}: {
  item: WorkItemView;
  delivery: WorkItemDelivery;
  observed: DeliveryObservation | undefined;
  readOnly: boolean;
}) {
  const { client, live } = useServices();
  const action = live.workItems.deliveryRefresh(item.work_item_id, delivery.delivery_id, client);
  const state = useStore(action.state);
  const reconciled = state.unknown && refreshReconciled(state.unknown.baseline, delivery, observed);
  const unresolved = state.unknown && !reconciled;
  return (
    <>
      <button
        type="button"
        className={controls.chipBtn}
        disabled={readOnly || state.pending || observed?.refresh_pending || !!unresolved}
        onClick={() => void action.start(item, delivery, observed)}
      >
        刷新交付状态
      </button>
      {unresolved && (
        <div className={styles.actions}>
          <button
            type="button"
            className={controls.chipBtn}
            disabled={readOnly || state.pending}
            onClick={() => void action.check()}
          >
            核对刷新请求
          </button>
          {state.unknown?.missing && (
            <button
              type="button"
              className={controls.chipBtn}
              disabled={readOnly || state.pending}
              onClick={() => void action.retry()}
            >
              重试原刷新请求
            </button>
          )}
        </div>
      )}
      {state.message && (
        <p role="status" className={styles.notice}>
          {reconciled
            ? "交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。"
            : state.message}
        </p>
      )}
    </>
  );
}
function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h3>{title}</h3>
      {children}
    </section>
  );
}
