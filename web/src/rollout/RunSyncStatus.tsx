import { useEffect, useState } from "react";
import type { RunSyncObservation } from "../api/generated/api.ts";

function since(at: number, now: number) {
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes === 0) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时前`;
  return `${Math.floor(minutes / 1440)} 天前`;
}

export function RunSyncStatus({ facts }: { facts: RunSyncObservation | undefined }) {
  const hasFacts = facts !== undefined;
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!hasFacts) return;
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [hasFacts]);
  if (!facts) return null;
  const missing = (facts.announced_sequence ?? 0) - (facts.received_sequence ?? 0);
  return (
    <small>
      {facts.stop_requested_at ? (
        <strong>{facts.stopped_at ? "runner 已确认停妥" : "停止已请求，runner 尚未确认"} · </strong>
      ) : null}
      {facts.last_contact_at
        ? `runner 最后联系于${since(facts.last_contact_at, now)}`
        : "尚无 runner 联系记录"}
      {facts.last_progress_at
        ? ` · 最后进展于${since(facts.last_progress_at, now)}（${facts.last_progress}）`
        : ""}
      {missing > 0 ? ` · 已知还有 ${missing} 条事件待同步` : ""}
    </small>
  );
}
