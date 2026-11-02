import { useEffect, useId, useMemo, useState } from "react";
import type { ExecutionCounts, IssueChildProgress, IssuePage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Button } from "../ui/index.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { issuePagePath } from "./data.ts";
import { workStatus, workStatusLabel } from "./boardModel.ts";
import styles from "./Board.module.css";

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
  onSelect,
}: {
  id: string;
  summary: IssueChildProgress;
  onSelect(id: string): void;
}) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<IssuePage>(client), [client]);
  const state = useStore(read.state);
  const [expanded, setExpanded] = useState(false);
  const [cursors, setCursors] = useState<string[]>([""]);
  const after = cursors.at(-1)!;
  const listID = useId();
  useEffect(() => {
    if (!expanded) return;
    void read.open(issuePagePath({ parentID: id, includeClosed: true, after }));
    const stop = live.subscribeEvents(() => void read.refresh());
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      read.close();
    };
  }, [read, live, id, expanded, after]);
  const items = expanded
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
              onClick={() => onSelect(item.issue_id)}
            >
              <span aria-hidden>{item.status === "done" ? "✓" : activity ? "◉" : "○"}</span>
              <span>{item.title}</span>
              <small>{activity ?? workStatusLabel(item.status)}</small>
            </Button>
          );
        })}
        {expanded && state.loading && <p role="status">正在读取子 Issue…</p>}
        {expanded && state.error && (
          <Button variant="link" onClick={() => void read.refresh()}>
            子 Issue 读取失败 · 重试
          </Button>
        )}
        {expanded && cursors.length > 1 && (
          <Button
            variant="link"
            disabled={state.loading}
            onClick={() => setCursors(cursors.slice(0, -1))}
          >
            上一页
          </Button>
        )}
        {expanded && state.value?.more && (
          <Button
            variant="link"
            disabled={state.loading}
            onClick={() => setCursors([...cursors, state.value!.next!])}
          >
            下一页
          </Button>
        )}
      </div>
      {summary.total > (summary.items?.length ?? 0) && (
        <Button
          variant="link"
          className={styles.childMore}
          aria-expanded={expanded}
          aria-controls={listID}
          onClick={() => {
            setExpanded(!expanded);
            setCursors([""]);
          }}
        >
          {expanded ? "收起子 Issue ↑" : `查看全部 ${summary.total} 个子 Issue ↓`}
        </Button>
      )}
    </section>
  );
}
