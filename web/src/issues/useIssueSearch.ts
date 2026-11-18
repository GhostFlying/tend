import { useEffect, useState } from "react";
import type { IssuePage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";

export function useIssueSearch(term: string, after = "") {
  const { client } = useServices();
  const query = term
    ? new URLSearchParams({ search: term, include_closed: "true", limit: "8", after }).toString()
    : "";
  const [result, setResult] = useState<{ query: string; page?: IssuePage; error?: boolean }>();
  useEffect(() => {
    if (!query) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      void client
        .get<IssuePage>(`/api/v1/issues?${query}`, controller.signal)
        .then((page) => {
          if (!controller.signal.aborted) setResult({ query, page });
        })
        .catch(() => {
          if (!controller.signal.aborted) setResult({ query, error: true });
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [client, query]);
  return query && result?.query === query ? result : undefined;
}
