import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type {
  ConversationImage,
  ConversationImageData,
  ConversationImageInput,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Close } from "../shell/icons.tsx";
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
      .get<{ image: ConversationImageData }>(
        `/api/v1/conversation/images/${encodeURIComponent(image.image_id)}`,
        controller.signal,
      )
      .then((response) => {
        if (!controller.signal.aborted) setSource(imageSource(response.image));
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [client, image.image_id, visible, attempt]);
  return (
    <div ref={tile} className={styles.stored}>
      {source ? (
        <ImageTile name={image.name} source={source} />
      ) : (
        <div className={styles.placeholder}>
          <span title={image.name}>{image.name}</span>
          {failed ? (
            <button
              type="button"
              onClick={() => {
                setFailed(false);
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
    </div>
  );
}

function ImageTile({ source, name }: { source: string; name: string }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <>
      <button
        type="button"
        className={styles.tile}
        aria-label={`查看图片 ${name}`}
        onClick={() => setExpanded(true)}
      >
        <img src={source} alt={name} loading="lazy" />
      </button>
      {expanded && <ImagePreview source={source} name={name} onClose={() => setExpanded(false)} />}
    </>
  );
}

function ImagePreview({
  source,
  name,
  onClose,
}: {
  source: string;
  name: string;
  onClose(): void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    dialog.current?.showModal();
  }, []);
  return (
    <dialog
      ref={dialog}
      className={styles.preview}
      aria-label={`图片预览 ${name}`}
      onCancel={onClose}
      onClick={(event) => event.currentTarget === event.target && onClose()}
    >
      <div className={styles.header}>
        <span>{name}</span>
        <button type="button" onClick={onClose} aria-label="关闭图片预览">
          <Close />
        </button>
      </div>
      <img src={source} alt={name} />
    </dialog>
  );
}
