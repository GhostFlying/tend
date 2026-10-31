import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
// A tool the main agent used: one line with the name and a summary, as
// dsh's DisclosureRow; expanded, the input and the output.
import { useState } from "react";
import type { ToolStatus, ToolUseView } from "../api/generated/api.ts";
import { Chevron } from "../shell/icons.tsx";
import { StatusDot, type Tone } from "../shell/StatusDot.tsx";
import { readable, toolSummary } from "./model.ts";
import { toolLabel } from "./toolLabels.ts";
import styles from "./ToolRow.module.css";

const statusLook: Record<ToolStatus, { tone: Tone; label: string } | null> = {
  running: { tone: "run", label: "进行中" },
  completed: null,
  failed: { tone: "err", label: "失败" },
  interrupted: { tone: "warn", label: "中断" },
};

export function ToolRow({
  tool,
  onOpenChange,
}: {
  tool: ToolUseView;
  onOpenChange?(open: boolean): void;
}) {
  const [open, setOpen] = useState(false);
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const lookups = useStore(live.lookups);
  const issues = useStore(live.issues.list.state);
  const detail = useStore(live.issues.detail.state);
  const summary = toolSummary(tool.input, (kind, id) =>
    kind === "task"
      ? (snapshot?.tasks?.find((task) => task.task_id === id)?.title ?? lookups.get(id)?.title)
      : ((detail.value?.issue_id === id ? detail.value.plan.title : undefined) ??
        issues.value?.items?.find((issue) => issue.issue_id === id)?.title),
  );
  const status = statusLook[tool.status];
  const input = readable(tool.input);
  const output = readable(tool.output);
  return (
    <div className={styles.tool} data-open={open || undefined}>
      <button
        type="button"
        className={styles.row}
        onClick={() => {
          setOpen(!open);
          onOpenChange?.(!open);
        }}
        aria-expanded={open}
      >
        <Chevron className={styles.chevron} />
        <span className={styles.name}>{toolLabel(tool.name)}</span>
        {summary && <span className={styles.sum}>{summary}</span>}
        {status && (
          <span className={styles.status} data-status={tool.status}>
            <StatusDot tone={status.tone} />
            {status.label}
          </span>
        )}
      </button>
      {open && (
        <div className={styles.body}>
          {input !== "" && (
            <>
              <div className={styles.label}>输入</div>
              <div className={styles.text}>{input}</div>
            </>
          )}
          <div className={styles.label}>输出</div>
          <div className={styles.text}>
            {output !== "" ? output : tool.status === "running" ? "（还在运行）" : "（没有输出）"}
          </div>
        </div>
      )}
    </div>
  );
}
