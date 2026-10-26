import type { TranscriptBlock } from "../api/generated/api.ts";
// A completed silent check remains in followup history, not the chat transcript.
// Failure/unknown/interruption always retains its actual visible state.
export function showTranscriptBlock(block: TranscriptBlock): boolean {
  return !(block.trigger.background?.status === "completed" && block.trigger.background.checkin);
}
