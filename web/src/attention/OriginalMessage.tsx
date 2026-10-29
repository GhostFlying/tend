import { useEffect, useMemo } from "react";
import type { IssueConversationItem } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { MessageAttachments } from "../conversation/Attachments.tsx";
import { MessageImages } from "../conversation/Images.tsx";
import styles from "./Inbox.module.css";

export function AttentionOriginalMessage({ id }: { id: string }) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<IssueConversationItem>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    void read.open(`/api/v1/messages/${encodeURIComponent(id)}`);
    return () => read.close();
  }, [read, id]);
  const source = state.value;
  return (
    <section className={styles.original} aria-label="原始说明">
      <h3>原始说明</h3>
      {state.error ? (
        <p>
          原消息暂时无法读取。<button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      ) : source ? (
        <>
          <p className={styles.muted}>
            原消息作者：
            {source.author === "owner"
              ? "你"
              : source.author === "main_agent"
                ? "Agent"
                : source.author}{" "}
            · {new Date(source.created_at).toLocaleString("zh-CN")}
          </p>
          {source.body ? (
            <Markdown text={source.body} />
          ) : (
            <p className={styles.muted}>原消息未包含文字。</p>
          )}
          {source.attachments?.length ? (
            <MessageAttachments attachments={source.attachments} />
          ) : (
            !!source.images?.length && <MessageImages images={source.images} />
          )}
          {source.control?.withdrawn_at && (
            <p className={styles.muted}>原消息已撤回，保留来源记录。</p>
          )}
        </>
      ) : (
        <p>正在读取原说明…</p>
      )}
    </section>
  );
}
