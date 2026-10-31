import { useState } from "react";
import type { InjectSourcePayload } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useSourceTransfer } from "./useSourceTransfer.ts";
import { LocalAttachment, MessageAttachments, OutgoingAttachments } from "./Attachments.tsx";
import styles from "../agents/Catalog.module.css";
import sourceStyles from "./ManualSource.module.css";

export function ManualSource({ onBack }: { onBack(): void }) {
  const { conversation } = useServices();
  const [target] = useState(conversation);
  const source = useSourceTransfer(`manual:${target?.id ?? "main"}`, target?.id);
  const [author, setAuthor] = useState("");
  const [time, setTime] = useState("");
  const [url, setUrl] = useState("");
  const [error, setError] = useState("");
  const original = source.current?.envelope.payload as InjectSourcePayload | undefined;
  const targetID = original?.conversation_id ?? target?.id ?? "main";
  const targetMatches = targetID === (target?.id ?? "main");
  const locked = source.busy || !!source.current || !source.content.loaded || !source.queue;
  const save = async () => {
    try {
      const record = await source.submit({
        ...(author ? { author } : {}),
        ...(url ? { url } : {}),
        ...(time ? { at_ms: new Date(time).getTime() } : {}),
      });
      if (record?.state === "failed") setError(record.error ?? "来源未保存。");
    } catch {
      setError("保存结果未确认，请保留原内容后核实。");
    }
  };
  return (
    <section
      className={`${styles.root} ${styles.form} ${sourceStyles.root}`}
      aria-label="手动补充来源"
    >
      <h2>补充来源</h2>
      <p className={styles.muted}>保留原文、原文件和来源信息；这不会冒充你的执行指令。</p>
      <section className={sourceStyles.target} aria-label="注入目标">
        <strong>{source.current?.state === "sent" ? "已送达" : "注入目标"}</strong>
        <p>
          会话：{targetMatches ? target?.title || "主 Agent" : targetID} · {targetID}
        </p>
        {targetMatches && target?.agentRef && <p>接收Agent：{target.agentRef}</p>}
        {targetMatches && target?.workItemID && <p>所属Issue：{target.workItemID}</p>}
      </section>
      <div className={sourceStyles.columns}>
        <div>
          <label>
            原文
            <textarea
              value={original?.text ?? source.content.text}
              disabled={locked}
              onChange={(e) => source.draft.write(e.target.value)}
            />
          </label>
          <label>
            来源链接（可选）
            <input
              type="url"
              value={original?.metadata.url ?? url}
              disabled={locked}
              onChange={(e) => setUrl(e.target.value)}
            />
          </label>
          <div className={sourceStyles.metadata}>
            <label>
              作者
              <input
                placeholder="未知"
                value={original?.metadata.author ?? author}
                disabled={locked}
                onChange={(e) => setAuthor(e.target.value)}
              />
            </label>
            <label>
              原始时间（可选，未知时留空）
              <input
                type="datetime-local"
                value={time}
                disabled={locked}
                onChange={(e) => setTime(e.target.value)}
              />
            </label>
          </div>
          {original?.metadata.at_ms !== undefined && (
            <p>原始时间：{new Date(original.metadata.at_ms).toLocaleString("zh-CN")}</p>
          )}
        </div>
        <aside className={sourceStyles.files}>
          <h2>原文件</h2>
          <p className={styles.muted}>图片可点开查看；保存保留原始文件。</p>
          <label className={sourceStyles.filePicker}>
            选择原文件
            <input
              type="file"
              multiple
              disabled={locked}
              onChange={(e) => {
                source.draft.addFiles(Array.from(e.target.files ?? []));
                e.target.value = "";
              }}
            />
          </label>
          {source.content.files.map((f, i) => (
            <div key={i}>
              <LocalAttachment blob={f} name={f.name} />
              <button disabled={locked} onClick={() => source.draft.removeFile(i)}>
                移除
              </button>
            </div>
          ))}
          {!!source.content.attachments.length && (
            <MessageAttachments attachments={source.content.attachments} />
          )}
          {!!source.current?.uploads?.length && (
            <OutgoingAttachments uploads={source.current.uploads} />
          )}
          {!!source.current?.references?.length && (
            <MessageAttachments attachments={source.current.references} />
          )}
          {!source.content.files.length && !source.current?.uploads?.length && (
            <p className={styles.muted}>尚未选择文件，也可以仅保存原文或链接。</p>
          )}
        </aside>
      </div>
      {(error || source.error || source.content.error) && (
        <p className={styles.warning} role="alert">
          {error || source.error || source.content.error}
        </p>
      )}
      {source.current?.state === "failed" && (
        <>
          <p className={styles.warning}>
            {source.current.error || "来源提交已被拒绝，尚未保存来源。"}
          </p>
          <button
            className={styles.button}
            onClick={() => {
              setAuthor(original?.metadata.author ?? "");
              setUrl(original?.metadata.url ?? "");
              void source.editRefused();
            }}
          >
            取回原输入后修改
          </button>
        </>
      )}
      {source.current?.state === "sent" ? (
        <>
          <p role="status">来源已保存。原件与来源信息会保留供核对。</p>
          <p>
            送达会话：{targetMatches ? target?.title || "主 Agent" : targetID} · {targetID}
          </p>
          {targetMatches && target?.agentRef && <p>接收Agent：{target.agentRef}</p>}
          {targetMatches && target?.workItemID && <p>所属Issue：{target.workItemID}</p>}
          <p className={styles.muted}>来源记录：{source.current.messageId || "记录ID尚未取得"}</p>
          <button className={styles.primary} onClick={onBack}>
            返回会话
          </button>
        </>
      ) : (
        <div className={styles.actions}>
          <button
            className={styles.primary}
            disabled={
              source.current?.state === "failed" ||
              source.busy ||
              !source.queue ||
              (!source.current &&
                !source.content.text.trim() &&
                !source.content.files.length &&
                !source.content.attachments.length &&
                !url.trim())
            }
            onClick={() => void save()}
          >
            {source.current ? "核实原来源提交" : "保存来源"}
          </button>
          <button className={styles.button} onClick={onBack}>
            稍后处理
          </button>
        </div>
      )}
    </section>
  );
}
