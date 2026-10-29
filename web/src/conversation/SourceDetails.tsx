import { useEffect, useMemo, useState } from "react";
import type {
  ManualSource,
  IssueConversationItem as ConversationItem,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { WorkItemRead } from "../workitems/live.ts";
import { useStore } from "../state/store.ts";
import { Markdown } from "./Markdown.tsx";
import { MessageAttachments } from "./Attachments.tsx";
import styles from "../agents/Catalog.module.css";
import sourceStyles from "./SourceDetails.module.css";

export function SourceDetails({ id, original = true }: { id: string; original?: boolean }) {
  const { client } = useServices();
  const [open, setOpen] = useState(false);
  // Independently expanded source disclosures cannot retarget each other's read.
  const read = useMemo(() => new WorkItemRead<ManualSource>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    if (open) void read.open(`/api/v1/sources/${encodeURIComponent(id)}`);
    return () => read.close();
  }, [read, id, open]);
  const source = state.value;
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>查看原件与来源信息</summary>
      {state.error ? (
        <p className={styles.muted}>
          暂时无法读取手动来源记录；原消息标识仍保留。
          <button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      ) : source ? (
        <>
          <p className={styles.muted}>
            标注作者：{source.metadata.author || "未知"} · 原始时间：
            {source.metadata.at_ms !== undefined
              ? new Date(source.metadata.at_ms).toLocaleString("zh-CN")
              : "未知"}
          </p>
          {source.metadata.url && (
            <p>
              来源：
              {/^https?:\/\//i.test(source.metadata.url) ? (
                <a href={source.metadata.url} target="_blank" rel="noreferrer">
                  {source.metadata.url}
                </a>
              ) : (
                source.metadata.url
              )}
            </p>
          )}
          {source.metadata.correction_of && (
            <p className={styles.muted}>更正关联来源：{source.metadata.correction_of}</p>
          )}
          {original && (
            <>
              <Markdown text={source.text} />
              {!!source.attachments?.length && (
                <MessageAttachments attachments={source.attachments} />
              )}
            </>
          )}
          <p className={styles.muted}>
            补充于{new Date(source.created_at).toLocaleString("zh-CN")} · 来源记录：
            {source.message_id}
          </p>
        </>
      ) : (
        <p>正在读取来源…</p>
      )}
    </details>
  );
}
export function ExternalSourceMessage({ item }: { item: ConversationItem }) {
  return (
    <section className={sourceStyles.source} aria-label="外部来源">
      <strong>补充的原始来源</strong>
      <p className={styles.muted}>保留来源原文，不代表你的执行指令或Agent结论。</p>
      <Markdown text={item.body ?? ""} />
      {!!item.attachments?.length && <MessageAttachments attachments={item.attachments} />}
      <SourceDetails id={item.message_id} original={false} />
    </section>
  );
}
