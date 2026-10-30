import { useEffect, useRef, useState } from "react";
import type { IssueSummary, IssueWorkStatus } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { DurableOp } from "../app/durableOp.ts";
import { performOwnerCommand } from "../app/ownerCommand.ts";
import type { OpOutcome } from "../app/ops.ts";

const prefix = "issue-status:";

export function useBoardActions() {
  const { client, live } = useServices();
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const [states, setStates] = useState<Record<string, "saving" | "unknown">>({});
  const [feedback, setFeedback] = useState<{ item: IssueSummary; outcome: OpOutcome } | null>(null);
  const running = useRef(new Set<string>());
  useEffect(() => {
    let active = true;
    void DurableOp.pendingScopes(prefix).then(
      (entries) => {
        if (!active) return;
        setStates(
          Object.fromEntries(entries.map(({ scope }) => [scope.slice(prefix.length), "unknown"])),
        );
        setReady(true);
      },
      () => {
        if (active) setError("无法读取本地操作记录，尚未发送状态变更。");
      },
    );
    return () => {
      active = false;
    };
  }, []);

  const move = async (item: IssueSummary, status?: IssueWorkStatus) => {
    if (!ready || running.current.has(item.issue_id) || (states[item.issue_id] && status)) return;
    running.current.add(item.issue_id);
    setStates((old) => ({ ...old, [item.issue_id]: "saving" }));
    const journal = new DurableOp(prefix + item.issue_id);
    try {
      const outcome = await performOwnerCommand(
        client,
        journal,
        status
          ? {
              operation: "issues.set_status",
              target: { type: "issue", id: item.issue_id },
              expected_control_revision: item.revision,
              payload: { status, expected_assignment_epoch: item.assignment_epoch },
            }
          : undefined,
      );
      setFeedback({ item, outcome });
      setStates((old) => {
        const next = { ...old };
        if (outcome.kind === "unknown") next[item.issue_id] = "unknown";
        else delete next[item.issue_id];
        return next;
      });
      live.issues.refresh();
    } catch {
      const pending = await journal.pending().catch(() => true);
      setStates((old) => {
        const next = { ...old };
        if (pending) next[item.issue_id] = "unknown";
        else delete next[item.issue_id];
        return next;
      });
      setFeedback({
        item,
        outcome: pending
          ? { kind: "unknown", message: "状态结果尚未确认，请核对原操作。" }
          : { kind: "refused", message: "无法保存本地操作记录，尚未发送请求。" },
      });
    } finally {
      running.current.delete(item.issue_id);
    }
  };
  return { ready, error, states, feedback, setFeedback, move };
}
