// Shell-scoped composer text survives navigation without sending an intention.
import type { ConversationImageInput, TaskSummary } from "../api/generated/api.ts";
import { Store } from "../state/store.ts";
import { maxImages } from "./images.ts";

interface DraftState {
  text: string;
  images: ConversationImageInput[];
  focus: number;
}

export class ComposerDraft {
  readonly state = new Store<DraftState>({ text: "", images: [], focus: 0 });
  write(text: string): void {
    this.state.set({ ...this.state.get(), text });
  }
  addImages(images: ConversationImageInput[]): void {
    const current = this.state.get();
    if (current.images.length + images.length > maxImages)
      throw new Error(`每条消息最多添加 ${maxImages} 张图片`);
    this.state.set({ ...current, images: [...current.images, ...images] });
  }
  removeImage(index: number): void {
    const current = this.state.get();
    this.state.set({ ...current, images: current.images.filter((_, i) => i !== index) });
  }
  clear(): void {
    this.state.set({ ...this.state.get(), text: "", images: [] });
  }
  followUp(task: TaskSummary): void {
    const current = this.state.get();
    const heading = `跟进 / 修改任务 ${task.task_id}`;
    const intention = `${heading}（${task.title}）。\n\n需要补充或修改的内容：\n`;
    this.state.set({
      ...current,
      text: current.text.includes(heading)
        ? current.text
        : current.text + (current.text.trim() ? "\n\n" : "") + intention,
      focus: current.focus + 1,
    });
  }
  restore(text: string, images: ConversationImageInput[] = []): void {
    const current = this.state.get();
    this.state.set({
      ...current,
      text: text && current.text ? `${text}\n\n${current.text}` : text || current.text,
      images: [...images, ...current.images],
    });
  }
}
