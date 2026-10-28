import type { ConversationItem, TranscriptBlock } from "../api/generated/api.ts";
import { showTranscriptBlock } from "./background.ts";
import { buildEntries, type Entry } from "./model.ts";

export type TranscriptEntry = Entry | { kind: "block-more"; key: string; block: TranscriptBlock };

/** Shared attribution remains in each block; the transcript renders one item. */
export function buildTranscriptEntries(blocks: readonly TranscriptBlock[]): TranscriptEntry[] {
  const visible = blocks.filter(showTranscriptBlock).toSorted((a, b) => a.position - b.position);
  const triggers = new Map(visible.map((block) => [block.trigger.message_id, block]));
  const items = new Map<string, ConversationItem>();
  const fallback = new Map<string, number>();
  for (const block of visible) {
    for (const item of block.items ?? []) {
      const previous = items.get(item.message_id);
      if (!previous || (item.assistant_revision ?? 0) >= (previous.assistant_revision ?? 0)) {
        items.set(item.message_id, item);
      }
      fallback.set(
        item.message_id,
        Math.max(fallback.get(item.message_id) ?? block.position, block.position),
      );
    }
  }
  const output = new Map<number, ConversationItem[]>();
  for (const [id, item] of items) {
    const position = triggers.get(item.reply_to ?? "")?.position ?? fallback.get(id)!;
    const rows = output.get(position) ?? [];
    rows.push(item);
    output.set(position, rows);
  }
  const entries: TranscriptEntry[] = [];
  for (const block of visible) {
    const rows = (output.get(block.position) ?? []).toSorted((a, b) => a.seq - b.seq);
    entries.push(
      ...buildEntries([block.trigger, ...rows], []).map((entry) => ({
        ...entry,
        key: `${block.position}:${entry.key}`,
      })),
    );
    if (block.more) entries.push({ kind: "block-more", key: `${block.position}:more`, block });
  }
  return entries;
}
