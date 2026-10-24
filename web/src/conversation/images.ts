import type { ConversationImageInput, SubmitMessagePayload } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";

export function imageSource(image: ConversationImageInput): string {
  return `data:${image.media_type};base64,${image.data_base64}`;
}

export function outgoingImages(message: OutgoingMessage): ConversationImageInput[] {
  return (message.envelope.payload as SubmitMessagePayload | undefined)?.images ?? [];
}
