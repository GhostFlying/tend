import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import type { TranscriptEntry } from "../conversation/transcriptBlocks.ts";

/** Keep the first reply mounted when a processing trigger gains its first item. */
export function activityTurnRows(
  rows: readonly TranscriptEntry[],
  block: IssueTranscriptBlock,
): TranscriptEntry[] {
  const key = `${block.position}:reply:${block.trigger.message_id}`;
  const first = rows.findIndex((row) => row.kind === "agent");
  if (first >= 0) return rows.map((row, index) => (index === first ? { ...row, key } : row));
  if (block.trigger.state !== "processing") return [...rows];
  const output = [...rows];
  const more = output.findIndex((row) => row.kind === "block-more");
  output.splice(more < 0 ? output.length : more, 0, {
    kind: "agent",
    key,
    replyTo: block.trigger.message_id,
    items: [],
    state: "processing",
    startedAt: Date.parse(block.trigger.created_at),
  });
  return output;
}
