import type { ConversationImageInput, SubmitMessagePayload } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";

export const maxImages = 4;
export const maxImageBytes = 5 * 1024 * 1024;
export const imageTypes = ["image/png", "image/jpeg", "image/gif"];

export function imageSource(image: ConversationImageInput): string {
  return `data:${image.media_type};base64,${image.data_base64}`;
}

export function outgoingImages(message: OutgoingMessage): ConversationImageInput[] {
  return (message.envelope.payload as SubmitMessagePayload | undefined)?.images ?? [];
}

export async function readImages(files: readonly File[]): Promise<ConversationImageInput[]> {
  if (files.length > maxImages) throw new Error(`每条消息最多添加 ${maxImages} 张图片`);
  for (const file of files) {
    if (!imageTypes.includes(file.type)) throw new Error("支持 PNG、JPEG 和 GIF 图片");
    if (file.size === 0 || file.size > maxImageBytes)
      throw new Error(`图片「${file.name}」必须小于或等于 5 MiB，且不能为空`);
  }
  return Promise.all(
    files.map(
      (file) =>
        new Promise<ConversationImageInput>((resolve, reject) => {
          const reader = new FileReader();
          reader.onerror = () => reject(new Error(`无法读取图片「${file.name}」`));
          reader.onabort = () => reject(new Error(`读取图片「${file.name}」已取消`));
          reader.onload = () => {
            const result = reader.result;
            if (typeof result !== "string" || !result.includes(",")) {
              reject(new Error(`无法读取图片「${file.name}」`));
              return;
            }
            resolve({
              name: file.name,
              media_type: file.type,
              data_base64: result.slice(result.indexOf(",") + 1),
            });
          };
          reader.readAsDataURL(file);
        }),
    ),
  );
}
