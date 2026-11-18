import { useEffect, useMemo, useState } from "react";
import type { IssueConversationItem, MessagingReceipts } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { DeliveryReceipt } from "../messaging/Messaging.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";

export function MessageReceipts({ item }: { item: IssueConversationItem }) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<MessagingReceipts>(client), [client]);
  const { value, error } = useStore(read.state);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (open)
      void read.open(`/api/v1/messaging/messages/${encodeURIComponent(item.message_id)}/receipts`);
    return () => read.close();
  }, [open, read, item.message_id, item.state]);
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>接收与投递记录</summary>
      {open && (
        <>
          {error && (
            <p role="status">
              回执读取失败。<button onClick={() => void read.refresh()}>重新读取回执</button>
            </p>
          )}
          {!value && !error && <p>正在读取回执…</p>}
          {value && (
            <>
              {(value.reads ?? []).map((receipt) => (
                <p
                  key={JSON.stringify([receipt.reader, receipt.content_revision, receipt.read_at])}
                >
                  {receipt.endpoint === "agent" && receipt.evidence.startsWith("native_input:")
                    ? "Agent 已接收原生输入"
                    : "已记录阅读"}
                  {" · "}
                  {receipt.reader} · 内容版本 {receipt.content_revision} ·{" "}
                  {new Date(receipt.read_at).toLocaleString("zh-CN")}
                </p>
              ))}
              {!value.reads?.length && <p>暂无阅读回执。</p>}
              {value.deliveries?.map((receipt) => (
                <DeliveryReceipt key={receipt.delivery_id} receipt={receipt} />
              ))}
              {!value.deliveries?.length && <p>暂无外部渠道投递记录。</p>}
            </>
          )}
        </>
      )}
    </details>
  );
}
