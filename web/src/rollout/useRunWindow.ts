import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/client.ts";
import type { RolloutPreview, RunRolloutPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { mergePage } from "./model.ts";

/** Compact status reads collected logs; the full monitor requests native originals explicitly. */
export function useRunWindow(id: string, preferNative = false, record?: string) {
  const target = Number(record);
  const focus = Number.isSafeInteger(target) && target > 0 && target < Number.MAX_SAFE_INTEGER;
  const { client } = useServices();
  const [native, setNative] = useState(preferNative);
  const [request, setRequest] = useState({
    cursor: focus ? `before=${target + 1}` : "",
    mode: "replace" as "replace" | "before" | "after",
    version: 0,
  });
  const [live, setLive] = useState(!focus);
  const [page, setPage] = useState<RunRolloutPage | null>(null);
  const [preview, setPreview] = useState<RolloutPreview | null>(null);
  const [error, setError] = useState(false);
  const [sourceChanged, setSourceChanged] = useState(false);
  const [loading, setLoading] = useState(true);
  const current = useRef<RunRolloutPage | null>(null);
  const generation = useRef("");
  const loaded = useRef(-1);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async (first: boolean) => {
      if (first) setLoading(true);
      const cursor = first ? request.cursor : `after=${current.current?.after ?? 0}`;
      const params = new URLSearchParams(cursor);
      if (native && generation.current) params.set("generation", generation.current);
      try {
        let next: RunRolloutPage;
        let mode = first ? request.mode : ("after" as const);
        if (native) {
          let result = await client.get<RolloutPreview>(
            `/api/v1/runs/${encodeURIComponent(id)}/preview?${params}`,
            abort.signal,
          );
          if (abort.signal.aborted) return;
          if (result.state === "cursor_invalid") {
            setSourceChanged(true);
            result = await client.get<RolloutPreview>(
              `/api/v1/runs/${encodeURIComponent(id)}/preview`,
              abort.signal,
            );
            mode = "replace";
          }
          if (abort.signal.aborted) return;
          setPreview((old) => ({ ...old, ...result }));
          if (!result.page || !["ready", "empty"].includes(result.state)) {
            setLoading(false);
            setError(["runner_unreachable", "timeout", "unavailable"].includes(result.state));
            if (live) timer = setTimeout(() => void poll(first), 2000);
            return;
          }
          generation.current = result.generation ?? "";
          next = result.page;
        } else
          next = await client.get<RunRolloutPage>(
            `/api/v1/runs/${encodeURIComponent(id)}/rollout?${params}`,
            abort.signal,
          );
        if (abort.signal.aborted) return;
        const value = mergePage(current.current, next, mode);
        loaded.current = request.version;
        current.current = value;
        setPage(value);
        setLoading(false);
        setError(false);
        if (live) timer = setTimeout(() => void poll(false), next.more_after ? 0 : 2000);
      } catch (cause) {
        if (abort.signal.aborted) return;
        setLoading(false);
        setError(true);
        if (native)
          setPreview((old) => ({
            ...old,
            state:
              cause instanceof ApiError && cause.status === 404 ? "unsupported" : "unavailable",
          }));
        if (live) timer = setTimeout(() => void poll(first), 3000);
      }
    };
    const first = loaded.current !== request.version;
    if (first || live) void poll(first);
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [client, id, native, live, request]);
  const load = (mode: "before" | "after") => {
    if (!current.current || loading) return;
    setLive(false);
    setRequest((old) => ({
      cursor: `${mode}=${current.current![mode]}`,
      mode,
      version: old.version + 1,
    }));
  };
  return {
    page,
    preview,
    sourceChanged,
    generation: generation.current,
    native,
    error,
    loading,
    live,
    setLive,
    selectSource: (next: boolean) => {
      if (next === native) return;
      current.current = null;
      generation.current = "";
      loaded.current = -1;
      setPage(null);
      setPreview(null);
      setSourceChanged(false);
      setError(false);
      setLoading(true);
      setLive(true);
      setNative(next);
      setRequest((old) => ({ cursor: "", mode: "replace", version: old.version + 1 }));
    },
    earlier: () => load("before"),
    later: () => load("after"),
    reconnect: () =>
      setRequest((old) => ({
        cursor: current.current ? `after=${current.current.after}` : "",
        mode: current.current ? "after" : "replace",
        version: old.version + 1,
      })),
    latest: () => {
      setLive(true);
      setRequest((old) => ({ cursor: "", mode: "replace", version: old.version + 1 }));
    },
  };
}

export function previewStateLabel(state: string): string {
  return (
    (
      {
        unsupported: "此 runner 尚不支持原生过程预览。",
        source_unknown: "尚无法确认这次执行的原生记录来源。",
        missing: "登记的原生记录已丢失。",
        format_unsupported: "原生记录格式尚未支持。",
        runner_unreachable: "runner 暂不可达，已保留已读内容。",
        timeout: "读取超时，已保留已读内容。",
        unavailable: "原生记录暂时无法读取，已保留已读内容。",
        missing_reference: "关联的原始输出已丢失。",
        cursor_invalid: "原件发生变化，请重新读取。",
        empty: "尚未生成原生过程记录。",
      } as Record<string, string>
    )[state] ?? "原生记录暂时无法读取。"
  );
}
