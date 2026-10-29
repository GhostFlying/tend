import type { IssueConversationItem, ConversationItem } from "../api/generated/api.ts";

export type MessageContent = Omit<IssueConversationItem, "associations" | "background">;

/** Raw ledger metadata is historical; owner links are read through canonical HTTP. */
export function eventContent(item: ConversationItem): MessageContent {
  const content = { ...item };
  delete content.associations;
  delete content.background;
  return content;
}
