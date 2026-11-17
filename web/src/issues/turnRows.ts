import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import type { TranscriptEntry } from "../conversation/transcriptBlocks.ts";

/** Keep the first reply mounted when a processing trigger gains its first item. */
export function activityTurnRows(
  rows: readonly TranscriptEntry[],
  block: IssueTranscriptBlock,
): TranscriptEntry[] {
  const key = `${block.position}:reply:${block.trigger.message_id}`;
  const withProcess = (row: Extract<TranscriptEntry, { kind: "agent" }>) => {
    const items = new Map(row.items.map((item) => [item.message_id, item]));
    for (const item of block.process?.items ?? []) items.set(item.message_id, item);
    return {
      ...row,
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
  const first = rows.findIndex((row) => row.kind === "agent");
  if (first >= 0)
    return rows.map((row, index) =>
      index === first && row.kind === "agent" ? withProcess(row) : row,
    );
  if (block.trigger.state !== "processing" && !block.process?.items?.length) return [...rows];
  const output = [...rows];
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
