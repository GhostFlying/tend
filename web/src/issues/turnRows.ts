import type { IssueConversationItem, IssueTranscriptBlock } from "../api/generated/api.ts";
import type { TranscriptEntry } from "../conversation/transcriptBlocks.ts";

type ActivityRow = TranscriptEntry | { kind: "source"; key: string; item: IssueConversationItem };

/** Keep the first reply mounted when a processing trigger gains its first item. */
export function activityTurnRows(
  rows: readonly TranscriptEntry[],
  block: IssueTranscriptBlock,
  readable = false,
): ActivityRow[] {
  if (block.trigger.issue_owner_steer && !readable) return [...rows];
  const key = `${block.position}:reply:${block.trigger.message_id}`;
  // Only this consumer reads and renders the independent process cursor.
  const publicRows: ActivityRow[] = [];
  for (const row of rows) {
    if (
      row.kind === "agent" &&
      block.trigger.author === "external" &&
      row.items.some((item) => item.message_id === block.trigger.message_id)
    ) {
      publicRows.push({ kind: "source", key: row.key + ":source", item: block.trigger });
      const remaining = row.items.filter((item) => item.message_id !== block.trigger.message_id);
      if (remaining.length) publicRows.push({ ...row, items: remaining });
    } else publicRows.push(row);
  }
  const entries: readonly ActivityRow[] =
    block.process?.more && !publicRows.some((row) => row.kind === "block-more")
      ? [...publicRows, { kind: "block-more", key: `${block.position}:more`, block }]
      : publicRows;
  const withProcess = (row: Extract<TranscriptEntry, { kind: "agent" }>) => {
    // Process sequence is its own arrival order. Starting with transcript
    // callbacks would move later callbacks ahead of earlier deferred tools.
    const items = new Map((block.process?.items ?? []).map((item) => [item.message_id, item]));
    for (const item of row.items) {
      const existing = items.get(item.message_id);
      if (!existing) items.set(item.message_id, item);
      else if (item.choices?.length)
        items.set(item.message_id, { ...existing, choices: item.choices });
    }
    const state =
      (readable ? block.process?.state : undefined) ??
      row.state ??
      (readable && !items.size && block.process?.has_tools ? "unknown" : undefined);
    return {
      ...row,
      ...(state ? { state } : {}),
      key,
      items: [...items.values()],
      ...(block.process
        ? {
            startedAt: block.process.started_at || null,
            endedAt: block.process.ended_at || null,
          }
        : { startedAt: null, endedAt: null }),
    };
  };
  const first = entries.findIndex((row) => row.kind === "agent");
  if (first >= 0)
    return entries.map((row, index) =>
      index === first && row.kind === "agent" ? withProcess(row) : row,
    );
  if (
    block.trigger.state !== "processing" &&
    block.process?.state !== "processing" &&
    !block.process?.has_tools &&
    !block.process?.items?.length
  )
    return [...entries];
  const output = [...entries];
  const more = output.findIndex((row) => row.kind === "block-more");
  output.splice(
    more < 0 ? output.length : more,
    0,
    withProcess({
      kind: "agent",
      key,
      replyTo: block.trigger.message_id,
      items: [],
      ...(block.trigger.state ? { state: block.trigger.state } : {}),
    }),
  );
  return output;
}
