import { useEffect, useRef, useState } from "react";
import type { RunRolloutPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { mergePage } from "./model.ts";

/** One request owner serves both presentations; mode changes never restart it. */
export function useRunWindow(id: string) {
  const { client } = useServices();
  const [request, setRequest] = useState({
    cursor: "",
    mode: "replace" as "replace" | "before" | "after",
    version: 0,
  });
  const [live, setLive] = useState(true);
  const [page, setPage] = useState<RunRolloutPage | null>(null);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const current = useRef<RunRolloutPage | null>(null);
  const loaded = useRef(-1);
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async (first: boolean) => {
      if (first) setLoading(true);
      const cursor = first ? request.cursor : `after=${current.current?.after ?? 0}`;
      try {
        const next = await client.get<RunRolloutPage>(
          `/api/v1/runs/${encodeURIComponent(id)}/rollout?${cursor}`,
          abort.signal,
        );
        if (abort.signal.aborted) return;
        const value = mergePage(current.current, next, first ? request.mode : "after");
        loaded.current = request.version;
        current.current = value;
        setPage(value);
        setLoading(false);
        setError(false);
        if (live) timer = setTimeout(() => void poll(false), next.more_after ? 0 : 2000);
      } catch {
        if (abort.signal.aborted) return;
        setLoading(false);
        setError(true);
        if (live) timer = setTimeout(() => void poll(first), 3000);
      }
    };
    const first = loaded.current !== request.version;
    if (first || live) void poll(first);
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [client, id, live, request]);
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
    error,
    loading,
    live,
    setLive,
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
