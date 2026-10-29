// Delivery bytes stay behind owner authorization; manifests never supply URLs.
import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import type { DownloadURLResponse } from "../api/generated/api.ts";
import controls from "../shell/controls.module.css";

export function FileDownload({
  fileId,
  name,
  scope = "files",
  downloadURL,
}: {
  fileId: string;
  name: string;
  scope?: "files" | "conversation/attachments" | "conversation/images";
  /** Archive views supply their dedicated owner endpoint, never a current file ID. */
  downloadURL?: (signal: AbortSignal) => Promise<DownloadURLResponse>;
}) {
  const { client } = useServices();
  const pending = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  useEffect(() => {
    return () => pending.current?.abort();
  }, []);
  const download = async () => {
    if (pending.current) return;
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const { url } = await (downloadURL
        ? downloadURL(controller.signal)
        : client.get<DownloadURLResponse>(
            `/api/v1/${scope}/${encodeURIComponent(fileId)}/download-url`,
            controller.signal,
          ));
      if (controller.signal.aborted) return;
      const link = document.createElement("a");
      link.href = url;
      link.download = name;
      document.body.append(link);
      link.click();
      link.remove();
      setMessage("已开始下载");
    } catch (err) {
      if (!controller.signal.aborted)
        setMessage(`下载失败：${err instanceof Error ? err.message : String(err)}。请重试。`);
    } finally {
      if (!controller.signal.aborted) {
        pending.current = null;
        setBusy(false);
      }
    }
  };
  return (
    <>
      <button
        type="button"
        className={controls.link}
        disabled={busy}
        aria-label={`下载 ${name}`}
        onClick={() => void download()}
      >
        {busy ? "正在下载…" : "下载"}
      </button>
      {message && <span role="status">{message}</span>}
    </>
  );
}
