// Shell-scoped composer text survives navigation without sending an intention.
import type { TaskSummary } from "../api/generated/api.ts";
import { Store } from "../state/store.ts";

interface DraftState {
  text: string;
  files: File[];
  focus: number;
}

export class ComposerDraft {
  readonly state = new Store<DraftState>({ text: "", files: [], focus: 0 });
  write(text: string): void {
    this.state.set({ ...this.state.get(), text });
  }
  addFiles(files: File[]): void {
    const current = this.state.get();
    this.state.set({ ...current, files: [...current.files, ...files] });
  }
  removeFile(index: number): void {
    const current = this.state.get();
    this.state.set({ ...current, files: current.files.filter((_, i) => i !== index) });
  }
  clear(): void {
    this.state.set({ ...this.state.get(), text: "", files: [] });
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
  restore(text: string, files: File[] = []): void {
    const current = this.state.get();
    this.state.set({
      ...current,
      text: text && current.text ? `${text}\n\n${current.text}` : text || current.text,
      files: [...files, ...current.files],
    });
  }
}
