import { useEffect, useMemo, useState } from "react";
import type { IssueEventNotificationPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";

const batchStatus: Record<string, string> = {
  pending: "待投递",
  frozen: "已冻结来源",
  completed: "已结算",
  interrupted: "已中断",
  failed: "失败",
  cancelled: "已取消",
  unknown: "未知",
};

const settlementStatus: Record<string, string> = {
  processing: "未结算",
  pending_repair: "待补核",
  awaiting_clarification: "等待澄清",
  completed: "已结算",
  stopped: "已停止",
  unknown: "未知",
};

function when(ms: number) {
  return new Date(ms).toLocaleString("zh-CN");
}

export function EventNotification({ issueID, position }: { issueID: string; position: number }) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<IssueEventNotificationPage>(client), [client]);
  const { value, error, loading } = useStore(read.state);
  const [open, setOpen] = useState(false);
  const [cursors, setCursors] = useState([0]);
  const cursor = cursors.at(-1)!;
  useEffect(() => {
    if (!open) return;
    const path = `/api/v1/issues/${encodeURIComponent(issueID)}/activity/${position}/notification`;
    void read.open(cursor ? `${path}?after=${cursor}` : path);
    return () => read.close();
  }, [read, issueID, position, cursor, open]);
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>查看这条事件的通知诊断</summary>
      {open && (
        <>
          {error && <p role="status">通知诊断暂时无法读取。</p>}
          {!value && !error && <p role="status">正在读取通知诊断…</p>}
          {value?.coalescing_until ? (
            <p>等待合并通知的时间窗口 · {when(value.coalescing_until)}</p>
          ) : null}
          {value && !value.notifications?.length && !value.coalescing_until && (
            <p>缺少这条事件与通知的关联记录。送达、开始处理及结算状态未知。</p>
          )}
          {value?.notifications?.map((notice) => (
            <div key={notice.batch_id}>
              <p>通知批次 · {batchStatus[notice.batch_status] ?? notice.batch_status}</p>
              <p>送达 · {notice.delivery === "received" ? "Agent 已接收通知" : "未知"}</p>
              <p>
                开始处理 · {notice.started_at ? when(notice.started_at) : "未知"}
                {notice.source === "read" ? " · 已读取这条来源" : ""}
                {notice.source === "cancelled" ? " · 来源已取消" : ""}
              </p>
              <p>等待原因 · {notice.reason || "暂无明确记录"}</p>
              <p>结算 · {settlementStatus[notice.settlement] ?? notice.settlement}</p>
            </div>
          ))}
          {(value || error) && (
            <button type="button" disabled={loading} onClick={() => void read.refresh()}>
              刷新通知诊断
            </button>
          )}
          {cursors.length > 1 && (
            <button type="button" onClick={() => setCursors((old) => old.slice(0, -1))}>
              上一页通知
            </button>
          )}
          {value?.next && (
            <button type="button" onClick={() => setCursors((old) => [...old, value.next!])}>
              下一页通知
            </button>
          )}
        </>
      )}
    </details>
  );
}
