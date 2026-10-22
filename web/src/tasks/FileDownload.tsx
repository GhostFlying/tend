// Delivery bytes stay behind owner authorization; manifests never supply URLs.
import { useEffect, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import controls from "../shell/controls.module.css";

export function FileDownload({ fileId, name }: { fileId: string; name: string }) {
  const { client } = useServices();
  const pending = useRef<AbortController | null>(null);
  const urls = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  useEffect(() => {
    const activeUrls = urls.current;
    return () => {
      pending.current?.abort();
      for (const [url, timer] of activeUrls) {
        clearTimeout(timer);
        URL.revokeObjectURL(url);
      }
      activeUrls.clear();
    };
  }, []);
  const download = async () => {
    if (pending.current) return;
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const blob = await client.file(fileId, controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = name;
      document.body.append(link);
      link.click();
      link.remove();
      urls.current.set(
        url,
        setTimeout(() => {
          URL.revokeObjectURL(url);
          urls.current.delete(url);
        }, 60_000),
      );
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
