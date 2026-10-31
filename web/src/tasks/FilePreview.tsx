import { useOverlayState } from "../ui/focus.ts";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { DownloadURLResponse } from "../api/generated/api.ts";
import { ApiError } from "../api/client.ts";
import { useServices } from "../app/services.tsx";
import { ImageTile } from "../conversation/Images.tsx";
import { FileDownload } from "./FileDownload.tsx";
import { isImage, type PreviewFile } from "./files.ts";
import { Button } from "../ui/Button.tsx";
import styles from "./FilePreview.module.css";

type Scope = "files" | "conversation/attachments";
export function FilePreview({
  file,
  scope = "files",
  compact = false,
}: {
  file: PreviewFile;
  scope?: Scope;
  compact?: boolean;
}) {
  const { client } = useServices();
  const tile = useRef<HTMLSpanElement>(null);
  const [visible, setVisible] = useState(false);
  const [source, setSource] = useState("");
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [html, setHtml] = useOverlayState(false);
  const image = isImage(file);
  const path = `/api/v1/${scope}/${encodeURIComponent(file.file_id)}/download-url?inline=1`;
  useEffect(() => {
    if (!image) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin: "200px" },
    );
    if (tile.current) observer.observe(tile.current);
    return () => observer.disconnect();
  }, [image]);
  useEffect(() => {
    if (!visible || !image) return;
    const controller = new AbortController();
    void client
      .get<DownloadURLResponse>(path, controller.signal)
      .then(({ url }) => {
        if (!controller.signal.aborted) setSource(url);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [client, path, visible, image, attempt]);
  return (
    <span ref={tile} className={styles.file} data-compact={compact}>
      {image &&
        (source && !failed ? (
          <ImageTile
            source={source}
            name={file.name}
            previewPath={path}
            onError={() => setFailed(true)}
          />
        ) : (
          <span className={styles.placeholder}>
            {failed ? (
              <Button
                variant="link"
                onClick={() => {
                  setFailed(false);
                  setSource("");
                  setAttempt((n) => n + 1);
                }}
              >
                图片读取失败，重试
              </Button>
            ) : (
              "正在读取图片…"
            )}
          </span>
        ))}
      <strong>{file.name}</strong>
      {!compact && <span className={styles.caption}>{file.size.toLocaleString()} 字节</span>}
      <span className={styles.actions}>
        {scope === "files" && /\.html?$/i.test(file.name) && (
          <Button variant="link" onClick={() => setHtml(true)}>
            预览设计稿
          </Button>
        )}
        <FileDownload fileId={file.file_id} name={file.name} scope={scope} />
      </span>
      {html && <HTMLPreview name={file.name} path={path} onClose={() => setHtml(false)} />}
    </span>
  );
}

function HTMLPreview({ name, path, onClose }: { name: string; path: string; onClose(): void }) {
  const { client } = useServices();
  const panel = useRef<HTMLDialogElement>(null);
  const [source, setSource] = useState("");
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useLayoutEffect(() => {
    const dialog = panel.current;
    const opener = document.activeElement;
    dialog?.showModal();
    return () => {
      dialog?.close();
      if (opener instanceof HTMLElement && opener.isConnected)
        opener.focus({ preventScroll: true });
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void client
      .get<DownloadURLResponse>(path, controller.signal)
      .then(({ url }) => {
        if (!controller.signal.aborted) setSource(url);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [client, path, attempt]);
  return createPortal(
    <dialog
      ref={panel}
      className={styles.html}
      aria-label={`设计稿预览 ${name}`}
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }}
    >
      <header>
        <span>{name}</span>
        <Button
          variant="link"
          onClick={() => {
            setSource("");
            setError(false);
            setAttempt((n) => n + 1);
          }}
        >
          重新加载
        </Button>
        <Button variant="secondary" size="s" onClick={onClose}>
          关闭预览
        </Button>
      </header>
      <p className={styles.caption}>静态预览 · 脚本和外部资源已停用，完整原件可下载。</p>
      {error ? (
        <button
          onClick={() => {
            setError(false);
            setAttempt((n) => n + 1);
          }}
        >
          预览读取失败，重试
        </button>
      ) : source ? (
        <iframe title={name} src={source} sandbox="" referrerPolicy="no-referrer" />
      ) : (
        <p role="status">正在读取设计稿…</p>
      )}
    </dialog>,
    document.body,
  );
}

/** Background references may predate spec.inputs; recover metadata without guessing filenames. */
export function FileReference({ id }: { id: string }) {
  const { client } = useServices();
  const [result, setResult] = useState<{ file: PreviewFile; scope: Scope } | null>(null);
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const read = async () => {
      try {
        let file: PreviewFile;
        let scope: Scope = "conversation/attachments";
        try {
          file = await client.get<PreviewFile>(
            `/api/v1/${scope}/${id}/metadata`,
            controller.signal,
          );
        } catch (err) {
          if (!(err instanceof ApiError) || err.status !== 404) throw err;
          scope = "files";
          file = await client.get<PreviewFile>(
            `/api/v1/${scope}/${id}/metadata`,
            controller.signal,
          );
        }
        if (!controller.signal.aborted) setResult({ file, scope });
      } catch {
        if (!controller.signal.aborted) setError(true);
      }
    };
    void read();
    return () => controller.abort();
  }, [client, id, attempt]);
  return result ? (
    <FilePreview file={result.file} scope={result.scope} />
  ) : (
    <p>
      <code>{id}</code> ·{" "}
      {error ? (
        <Button
          variant="link"
          onClick={() => {
            setError(false);
            setAttempt((n) => n + 1);
          }}
        >
          文件读取失败，重试
        </Button>
      ) : (
        "正在读取原件…"
      )}
    </p>
  );
}
