import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useServices } from "../app/services.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { Button } from "../ui/Button.tsx";
import { FileDownload } from "./FileDownload.tsx";
import { textFileFormat, type PreviewFile } from "./files.ts";
import styles from "./TextPreview.module.css";

/** Read original bytes only when requested; preserve the original download. */
export default function TextPreview({
  file,
  scope,
  files,
  onClose,
}: {
  file: PreviewFile;
  scope: "files" | "conversation/attachments";
  files: readonly PreviewFile[];
  onClose(): void;
}) {
  const { client } = useServices();
  const panel = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [body, setBody] = useState<string | null>(null);
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useLayoutEffect(() => {
    const dialog = panel.current;
    const opener = document.activeElement;
    dialog?.showModal();
    heading.current?.focus();
    return () => {
      dialog?.close();
      if (opener instanceof HTMLElement && opener.isConnected)
        opener.focus({ preventScroll: true });
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void client
      .file(file.file_id, controller.signal, scope)
      .then((blob) => blob.text())
      .then((text) => {
        if (!controller.signal.aborted) setBody(text);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [client, file.file_id, scope, attempt]);
  const retry = () => {
    setError(false);
    setBody(null);
    setAttempt((n) => n + 1);
  };
  return createPortal(
    <dialog
      ref={panel}
      className={styles.reader}
      aria-label={`阅读 ${file.name}`}
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }}
    >
      <header className={styles.header}>
        <h2 ref={heading} tabIndex={-1}>
          {file.name}
        </h2>
        <div className={styles.actions}>
          <FileDownload fileId={file.file_id} name={file.name} scope={scope} />
          <Button variant="secondary" size="s" onClick={onClose}>
            关闭阅读
          </Button>
        </div>
      </header>
      <article className={styles.body}>
        {error ? (
          <p role="status">
            原文读取失败。<Button onClick={retry}>重试</Button>
          </p>
        ) : body === null ? (
          <p role="status">正在读取原文…</p>
        ) : textFileFormat(file) === "markdown" ? (
          <Markdown text={body} files={files} fileScope={scope} />
        ) : (
          <pre className={styles.plain}>{body}</pre>
        )}
      </article>
    </dialog>,
    document.body,
  );
}
