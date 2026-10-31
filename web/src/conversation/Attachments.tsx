import { useEffect, useRef, useState } from "react";
import type { ConversationAttachment, DownloadURLResponse } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import type { OutgoingAttachment } from "../queue/outbox.ts";
import { FileDownload } from "../tasks/FileDownload.tsx";
import { ImageTile } from "./Images.tsx";
import styles from "./Attachments.module.css";

export function fileSize(size: number): string {
  if (size < 1024) return `${size} B`;
  const power = Math.min(Math.floor(Math.log(size) / Math.log(1024)), 4);
  return `${(size / 1024 ** power).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${["B", "KiB", "MiB", "GiB", "TiB"][power]}`;
}

/** Blob URLs reference browser storage without reading file bytes into JavaScript. */
export function LocalAttachment({ blob, name }: { blob: Blob; name: string }) {
  const [preview, setPreview] = useState<{ blob: Blob; url: string } | null>(null);
  const source = preview?.blob === blob ? preview.url : "";
  const [failedBlob, setFailedBlob] = useState<Blob | null>(null);
  const failed = failedBlob === blob;
  useEffect(() => {
    if (!blob.type.startsWith("image/")) return;
    const url = URL.createObjectURL(blob);
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) setPreview({ blob, url });
    });
    return () => {
      cancelled = true;
      URL.revokeObjectURL(url);
    };
  }, [blob]);
  return (
    <div className={styles.card}>
      {source && !failed && (
        <ImageTile source={source} name={name} onError={() => setFailedBlob(blob)} />
      )}
      <span className={styles.name} title={name}>
        {name}
      </span>
      <span className={styles.meta}>
        {fileSize(blob.size)}
        {failed ? " · 无法预览，原文件仍可发送" : ""}
      </span>
    </div>
  );
}

export function OutgoingAttachments({ uploads }: { uploads: OutgoingAttachment[] }) {
  return (
    <div className={styles.list} aria-label="待发送消息附件">
      {uploads.map((upload) => (
        <LocalAttachment key={upload.requestId} blob={upload.blob} name={upload.name} />
      ))}
    </div>
  );
}

export function MessageAttachments({ attachments }: { attachments: ConversationAttachment[] }) {
  return (
    <div className={styles.list} aria-label="消息附件">
      {attachments.map((file) => (
        <StoredAttachment key={file.file_id} file={file} />
      ))}
    </div>
  );
}

function StoredAttachment({ file }: { file: ConversationAttachment }) {
  const { client } = useServices();
  const element = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  const [source, setSource] = useState("");
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const image = file.media_type.startsWith("image/");
  useEffect(() => {
    if (!image) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin: "300px" },
    );
    if (element.current) observer.observe(element.current);
    return () => observer.disconnect();
  }, [image]);
  useEffect(() => {
    if (!visible || !image) return;
    const controller = new AbortController();
    void client
      .get<DownloadURLResponse>(
        `/api/v1/conversation/attachments/${encodeURIComponent(file.file_id)}/download-url?inline=1`,
        controller.signal,
      )
      .then(({ url }) => {
        if (!controller.signal.aborted) setSource(url);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [client, visible, image, file.file_id, attempt]);
  return (
    <div ref={element} className={styles.card}>
      {image && (
        <div className={styles.thumbnail}>
          {source && !failed && (
            <ImageTile
              name={file.name}
              source={source}
              previewPath={`/api/v1/conversation/attachments/${encodeURIComponent(file.file_id)}/download-url?inline=1`}
              onError={() => setFailed(true)}
            />
          )}
        </div>
      )}
      <span className={styles.name} title={file.name}>
        {file.name}
      </span>
      <span className={styles.meta}>{fileSize(file.size)}</span>
      {failed && (
        <button
          type="button"
          className={styles.retry}
          onClick={() => {
            setFailed(false);
            setSource("");
            setAttempt((n) => n + 1);
          }}
        >
          预览不可用，重试
        </button>
      )}
      <FileDownload fileId={file.file_id} name={file.name} scope="conversation/attachments" />
    </div>
  );
}
