import { useEntryState } from "../app/router.ts";
import { AssigneeName } from "./AssigneeName.tsx";
import { useMemo } from "react";
import type { IssueSummary, IssueState, IssueView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { Chevron } from "../shell/icons.tsx";
import styles from "./Issues.module.css";

export const issueStateLabel: Record<IssueState, string> = {
  active: "推进中",
  paused: "已暂停",
  cancelling: "取消中",
  cancelled: "已取消",
  closed: "已关闭",
};
export function IssueHierarchy({
  items,
  onSelect,
  current,
  includeClosed = true,
}: {
  items: readonly IssueSummary[];
  onSelect(id: string): void;
  current?: IssueView | undefined;
  includeClosed?: boolean;
}) {
  return (
    <div className={styles.tree} role="tree" aria-label="Issue层级">
      <div className={styles.treeHead}>
        <span>Issue</span>
        <span>负责人</span>
        <span>状态</span>
      </div>
      {current ? (
        <IssueNode
          item={{ ...current, title: current.plan.title, project: current.plan.project }}
          depth={0}
          onSelect={onSelect}
          defaultOpen
          includeClosed={includeClosed}
        />
      ) : (
        items.map((item) => (
          <IssueNode
            key={item.issue_id}
            item={item}
            depth={0}
            onSelect={onSelect}
            includeClosed={includeClosed}
          />
        ))
      )}
    </div>
  );
}
function IssueNode({
  item,
  depth,
  onSelect,
  defaultOpen = false,
  includeClosed,
}: {
  item: IssueSummary;
  depth: number;
  onSelect(id: string): void;
  defaultOpen?: boolean;
  includeClosed: boolean;
}) {
  const [open, setOpen] = useEntryState(`tree:${includeClosed}:${item.issue_id}`, defaultOpen);
  return (
    <>
      <div className={styles.treeRow} role="treeitem" aria-expanded={open} aria-level={depth + 1}>
        <div
          className={styles.treeTitle}
          style={{ paddingLeft: `calc(${depth} * var(--space-4))` }}
        >
          <button
            className={styles.chevron}
            aria-label={`${open ? "折叠" : "展开"} ${item.title}`}
            onClick={() => setOpen(!open)}
          >
            <Chevron
              width={16}
              height={16}
              style={{ transform: open ? "rotate(90deg)" : undefined }}
            />
          </button>
          <div className={styles.treeName}>
            <button onClick={() => onSelect(item.issue_id)}>{item.title}</button>
            {depth === 0 && item.parent_issue_id && (
              <small className={styles.muted}>上级Issue：{item.parent_issue_id}</small>
            )}
          </div>
        </div>
        <span>
          <AssigneeName assignee={item.assignee} />
        </span>
        <span>
          {item.assignment_state !== "assigned" && item.pending_handoff_id
            ? "交接待核实"
            : issueStateLabel[item.state]}
        </span>
      </div>
      {open && (
        <Children
          id={item.issue_id}
          depth={depth + 1}
          onSelect={onSelect}
          includeClosed={includeClosed}
        />
      )}
    </>
  );
}
function Children({
  id,
  depth,
  onSelect,
  includeClosed,
}: {
  id: string;
  depth: number;
  onSelect(id: string): void;
  includeClosed: boolean;
}) {
  const { live } = useServices();
  const read = useMemo(
    () => live.issues.children(id, undefined, includeClosed),
    [live, id, includeClosed],
  );
  const state = useStore(read.state);
  return (
    <div role="group">
      {state.error ? (
        <p className={styles.warning}>
          子项读取失败。<button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      ) : !state.value ? (
        <p className={styles.muted}>正在读取子项…</p>
      ) : (
        (state.value.items ?? []).map((item) => (
          <IssueNode
            key={item.issue_id}
            item={item}
            depth={depth}
            onSelect={onSelect}
            includeClosed={includeClosed}
          />
        ))
      )}
      {state.value?.more && (
        <button
          className={styles.button}
          onClick={() => live.issues.children(id, state.value?.next, includeClosed)}
        >
          下一页子项
        </button>
      )}
    </div>
  );
}
