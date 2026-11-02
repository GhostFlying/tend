import { useEffect, useRef, useState } from "react";
import type { RolloutPreview } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { Button } from "../ui/Button.tsx";
import { previewStateLabel } from "./useRunWindow.ts";
import styles from "./Monitor.module.css";

type Props = {
  run: string;
  generation: string;
  record: number;
  field: "raw" | "text" | "input" | "output";
  label: string;
  fallback?: string;
};
export function NativeContent(props: Props) {
  return (
    <NativeContentWindow
      key={`${props.run}-${props.generation}-${props.record}-${props.field}`}
      {...props}
    />
  );
}
function NativeContentWindow({ run, generation, record, field, label, fallback = "" }: Props) {
  const { client } = useServices();
  const [reference, setReference] = useState(0);
  const [request, setRequest] = useState({ offset: 0, version: 0 });
  const [body, setBody] = useState({
    text: fallback,
    more: false,
    next: 0,
    error: "",
    loading: true,
  });
  const [references, setReferences] = useState<
    NonNullable<NonNullable<RolloutPreview["content"]>["references"]>
  >([]);
  const code = useRef<HTMLElement>(null);
  const end = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const abort = new AbortController();
    const read = async () => {
      const params = new URLSearchParams({
        generation,
        record: String(record),
        field,
        offset: String(request.offset),
      });
      if (reference) params.set("reference", String(reference));
      try {
        const result = await client.get<RolloutPreview>(
          `/api/v1/runs/${encodeURIComponent(run)}/preview?${params}`,
          abort.signal,
        );
        if (abort.signal.aborted) return;
        if (result.state !== "ready" || !result.content) {
          setBody((old) => ({
            ...old,
            loading: false,
            more: false,
            error: previewStateLabel(result.state),
          }));
          return;
        }
        const content = result.content;
        setBody((old) => ({
          text: request.offset ? old.text + content.text : content.text,
          next: content.next_offset,
          more: content.more,
          error: "",
          loading: false,
        }));
        if (content.references) setReferences(content.references);
      } catch {
        if (!abort.signal.aborted)
          setBody((old) => ({
            ...old,
            loading: false,
            more: false,
            error: "正文暂不可读取，已保留当前内容。",
          }));
      }
    };
    void read();
    return () => abort.abort();
  }, [client, run, generation, record, field, reference, request]);
  useEffect(() => {
    if (!body.more || body.loading || !end.current || !code.current) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        observer.disconnect();
        setBody((old) => ({ ...old, loading: true }));
        setRequest((old) => ({ offset: body.next, version: old.version + 1 }));
      },
      { root: code.current, rootMargin: "80px" },
    );
    observer.observe(end.current);
    return () => observer.disconnect();
  }, [body.more, body.loading, body.next]);
  return (
    <section className={styles.detailSection}>
      <h2>{label}</h2>
      {field === "output" && references.length > 0 && (
        <div className={styles.references}>
          <Button
            variant="ghost"
            size="s"
            aria-pressed={!reference}
            onClick={() => {
              setReference(0);
              setBody({ text: fallback, next: 0, more: false, loading: true, error: "" });
              setRequest((old) => ({ offset: 0, version: old.version + 1 }));
            }}
          >
            记录输出
          </Button>
          {references.map((item) => (
            <Button
              key={item.id}
              variant="ghost"
              size="s"
              aria-pressed={reference === item.id}
              onClick={() => {
                setReference(item.id);
                setBody({ text: "", next: 0, more: false, loading: true, error: "" });
                setRequest((old) => ({ offset: 0, version: old.version + 1 }));
              }}
            >
              {item.name}
              {item.missing ? "（已丢失）" : ""}
            </Button>
          ))}
        </div>
      )}
      {field === "text" ? (
        <div
          className={`${styles.prose} ${styles.pagedProse}`}
          ref={(node) => {
            code.current = node;
          }}
          aria-label={label}
        >
          <Markdown text={body.text || (body.loading ? "正在读取原始内容…" : "未记录正文。")} />
          {body.more && (
            <span ref={end} className={styles.contentEnd}>
              向下滚动读取后续内容
            </span>
          )}
        </div>
      ) : (
        <pre
          ref={(node) => {
            code.current = node;
          }}
          aria-label={label}
        >
          {body.text || (body.loading ? "正在读取原始内容…" : "未记录正文。")}
          {body.more && (
            <span ref={end} className={styles.contentEnd}>
              向下滚动读取后续内容
            </span>
          )}
        </pre>
      )}
      {body.error && (
        <p className={styles.warning} role="status">
          {body.error}
          <Button
            variant="link"
            onClick={() => {
              setBody((old) => ({ ...old, loading: true }));
              setRequest((old) => ({ offset: 0, version: old.version + 1 }));
            }}
          >
            重新读取
          </Button>
        </p>
      )}
    </section>
  );
}
