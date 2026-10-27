import { createPortal } from "react-dom";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type {
  ConversationImage,
  ConversationImageInput,
  DownloadURLResponse,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Close } from "../shell/icons.tsx";
import { FileDownload } from "../tasks/FileDownload.tsx";
import { imageSource } from "./images.ts";
import styles from "./Images.module.css";

export function MessageImages({ images }: { images: ConversationImage[] }) {
  return (
    <div className={styles.gallery} aria-label="消息图片">
      {images.map((image) => (
        <StoredImage key={image.image_id} image={image} />
      ))}
    </div>
  );
}

export function OutgoingImages({ images }: { images: ConversationImageInput[] }) {
  return (
    <div className={styles.gallery} aria-label="待发送消息图片">
      {images.map((image, index) => (
        <ImageTile key={index} name={image.name} source={imageSource(image)} />
      ))}
    </div>
  );
}

function StoredImage({ image }: { image: ConversationImage }) {
  const { client } = useServices();
  const tile = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  const [source, setSource] = useState("");
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  // Old transcript pages do not fetch every image before the owner scrolls there.
  useEffect(() => {
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin: "300px" },
    );
    if (tile.current) observer.observe(tile.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    void client
      .get<DownloadURLResponse>(
        `/api/v1/conversation/images/${encodeURIComponent(image.image_id)}/download-url?inline=1`,
        controller.signal,
      )
      .then((response) => {
        if (!controller.signal.aborted) setSource(response.url);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [client, image.image_id, visible, attempt]);
  return (
    <div ref={tile} className={styles.stored}>
      {source && !failed ? (
        <ImageTile
          name={image.name}
          source={source}
          previewPath={`/api/v1/conversation/images/${encodeURIComponent(image.image_id)}/download-url?inline=1`}
          onError={() => setFailed(true)}
        />
      ) : (
        <div className={styles.placeholder}>
          <span title={image.name}>{image.name}</span>
          {failed ? (
            <button
              type="button"
              onClick={() => {
                setFailed(false);
                setSource("");
                setAttempt((n) => n + 1);
              }}
            >
              图片读取失败，重试
            </button>
          ) : (
            <span>正在读取图片…</span>
          )}
        </div>
      )}
      <FileDownload fileId={image.image_id} name={image.name} scope="conversation/images" />
    </div>
  );
}

export function ImageTile({
  source,
  name,
  onError,
  previewPath,
}: {
  source: string;
  name: string;
  onError?(): void;
  previewPath?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  return (
    <>
      <button
        type="button"
        className={styles.tile}
        aria-label={`查看图片 ${name}`}
        onClick={() => setExpanded(true)}
      >
        <img src={source} alt={name} loading="lazy" onError={onError} />
      </button>
      {expanded && (
        <ImagePreview
          source={source}
          name={name}
          {...(previewPath ? { previewPath } : {})}
          onClose={() => setExpanded(false)}
        />
      )}
    </>
  );
}

function ImagePreview({
  source,
  name,
  onClose,
  previewPath,
}: {
  source: string;
  name: string;
  onClose(): void;
  previewPath?: string;
}) {
  const { client } = useServices();
  const [previewSource, setPreviewSource] = useState(previewPath ? "" : source);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  // A long-lived thumbnail can outlast its grant. Expansion and retries each
  // obtain a fresh grant, without replacing the already loaded thumbnail.
  useEffect(() => {
    if (!previewPath) return;
    const controller = new AbortController();
    void client
      .get<DownloadURLResponse>(previewPath, controller.signal)
      .then(({ url }) => {
        if (!controller.signal.aborted) setPreviewSource(url);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [client, previewPath, attempt]);
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const panel = dialog.current;
    const opener = document.activeElement;
    panel?.showModal();
    return () => {
      panel?.close();
      if (opener instanceof HTMLElement && opener.isConnected)
        opener.focus({ preventScroll: true });
    };
  }, []);
  return createPortal(
    <dialog
      ref={dialog}
      className={styles.preview}
      aria-label={`图片预览 ${name}`}
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }}
      onClick={(event) => event.currentTarget === event.target && onClose()}
    >
      <div className={styles.header}>
        <span>{name}</span>
        <button type="button" onClick={onClose} aria-label="关闭图片预览">
          <Close />
        </button>
      </div>
      {failed ? (
        <div role="status" className={styles.previewError}>
          <span>图片预览不可用</span>
          <button
            type="button"
            onClick={() => {
              setFailed(false);
              setPreviewSource(previewPath ? "" : source);
              setAttempt((n) => n + 1);
            }}
          >
            重试图片预览
          </button>
        </div>
      ) : previewSource ? (
        <img src={previewSource} alt={name} onError={() => setFailed(true)} />
      ) : (
        <p role="status">正在读取图片…</p>
      )}
    </dialog>,
    document.body,
  );
}
