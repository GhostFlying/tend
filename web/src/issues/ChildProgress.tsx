import { useEffect, useId } from "react";
import type { ExecutionCounts, IssueChildProgress, IssuePage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Store, useStore } from "../state/store.ts";
import { Button } from "../ui/index.ts";
import { WorkItemRead, type ReadState } from "../workitems/live.ts";
import { issuePagePath } from "./data.ts";
import { workStatus, workStatusLabel } from "./boardModel.ts";
import styles from "./Board.module.css";

export type ExpandedChildView = {
  read: WorkItemRead<IssuePage>;
  cursors: string[];
};

const collapsedChildPage = new Store<ReadState<IssuePage>>({ value: null, loading: false });

export function childExecutionLabel(counts?: ExecutionCounts): string | undefined {
  if (!counts) return undefined;
  if (counts.unknown) return "待核对";
  if (counts.waiting_owner) return "等你处理";
  if (counts.waiting_supervisor) return "等待负责人";
  if (counts.accepting) return "验收中";
  if (counts.running) return "执行中";
  if (counts.recovering) return "恢复中";
  if (counts.starting) return "准备中";
  if (counts.queued) return "排队中";
  if (counts.paused) return "已暂停";
  return undefined;
}

export function ChildProgress({
  id,
  summary,
  view,
  onViewChange,
  onSelect,
}: {
  id: string;
  summary: IssueChildProgress;
  view: ExpandedChildView | undefined;
  onViewChange(view: ExpandedChildView | undefined): void;
  onSelect(id: string): void;
}) {
  const { client, live } = useServices();
  const read = view?.read;
  const state = useStore(read?.state ?? collapsedChildPage);
  const after = view?.cursors.at(-1) ?? "";
  const listID = useId();
  useEffect(() => {
    if (!read) return;
    void read.open(issuePagePath({ parentID: id, includeClosed: true, after }));
    const stop = live.subscribeEvents(() => void read.refresh());
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      read.pause();
    };
  }, [read, live, id, after]);
  const items = view
    ? (state.value?.items ?? []).map((item) => ({
        issue_id: item.issue_id,
        title: item.title,
        status: workStatus(item),
        execution: item.execution,
      }))
    : (summary.items ?? []);
  return (
    <section
      className={styles.children}
      aria-label="子 Issue 进展"
      onDragStart={(e) => e.stopPropagation()}
    >
      <div className={styles.childrenHead}>
        <span>
          子 Issue · {summary.done}/{summary.total} 已完成
          {summary.cancelled > 0 && ` · ${summary.cancelled} 已取消`}
        </span>
        {summary.running > 0 && <span>{summary.running} 项执行中</span>}
      </div>
      <div id={listID}>
        {items.map((item) => {
          const activity = childExecutionLabel(item.execution);
          return (
            <Button
              key={item.issue_id}
              variant="ghost"
              className={styles.child}
              data-active={!!activity}
              data-board-focus={`${id}:child:${item.issue_id}`}
              onClick={() => onSelect(item.issue_id)}
            >
              <span aria-hidden>{item.status === "done" ? "✓" : activity ? "◉" : "○"}</span>
              <span>{item.title}</span>
              <small>{activity ?? workStatusLabel(item.status)}</small>
            </Button>
          );
        })}
        {view && state.loading && <p role="status">正在读取子 Issue…</p>}
        {view && state.error && (
          <Button
            variant="link"
            data-board-focus={`${id}:children-retry`}
            onClick={() => void view.read.refresh()}
          >
            子 Issue 读取失败 · 重试
          </Button>
        )}
        {view && view.cursors.length > 1 && (
          <Button
            variant="link"
            aria-disabled={state.loading}
            data-board-focus={`${id}:children-previous`}
            onClick={() => {
              if (!view.read.state.get().loading)
                onViewChange({ ...view, cursors: view.cursors.slice(0, -1) });
            }}
          >
            上一页
          </Button>
        )}
        {view && state.value?.more && (
          <Button
            variant="link"
            aria-disabled={state.loading}
            data-board-focus={`${id}:children-next`}
            onClick={() => {
              const current = view.read.state.get();
              if (!current.loading && current.value?.next)
                onViewChange({ ...view, cursors: [...view.cursors, current.value.next] });
            }}
          >
            下一页
          </Button>
        )}
      </div>
      {summary.total > (summary.items?.length ?? 0) && (
        <Button
          variant="link"
          className={styles.childMore}
          data-board-focus={`${id}:children`}
          aria-expanded={!!view}
          aria-controls={listID}
          onClick={() =>
            onViewChange(view ? undefined : { read: new WorkItemRead(client), cursors: [""] })
          }
        >
          {view ? "收起子 Issue ↑" : `查看全部 ${summary.total} 个子 Issue ↓`}
        </Button>
      )}
    </section>
  );
}
