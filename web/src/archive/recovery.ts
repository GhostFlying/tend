import type { Envelope, ManualSource } from "../api/generated/api.ts";

export interface PendingArchiveIssue {
  requestID: string;
  sourceMessageID: string;
  sourceRevision: number;
}

/** Locate unfinished Issue drafts from their original source, without keeping completed sends. */
export async function pendingArchiveIssues(
  sender: { get<T>(path: string): Promise<T> },
  archiveID: string,
  origin: string,
  pending: { scope: string; envelope: Envelope }[],
): Promise<{ items: PendingArchiveIssue[]; unreadable: boolean }> {
  const expectedURL = `${origin}/api/v1/archives/${encodeURIComponent(archiveID)}`;
  const candidates = pending.flatMap(({ scope, envelope }) => {
    const payload = (envelope.payload ?? {}) as Record<string, unknown>;
    return envelope.operation === "issues.create" &&
      typeof payload.source_message_id === "string" &&
      payload.source_message_id !== "" &&
      scope === `issue-create:${payload.source_message_id}` &&
      typeof payload.expected_source_revision === "number"
      ? [
          {
            requestID: envelope.request_id,
            sourceMessageID: payload.source_message_id,
            sourceRevision: payload.expected_source_revision,
          },
        ]
      : [];
  });
  const observations = await Promise.allSettled(
    candidates.map(async (candidate) => {
      const source = await sender.get<ManualSource>(
        `/api/v1/sources/${encodeURIComponent(candidate.sourceMessageID)}`,
      );
      return source.message_id === candidate.sourceMessageID && source.metadata?.url === expectedURL
        ? candidate
        : undefined;
    }),
  );
  return {
    items: observations.flatMap((result) =>
      result.status === "fulfilled" && result.value ? [result.value] : [],
    ),
    unreadable: observations.some((result) => result.status === "rejected"),
  };
}
