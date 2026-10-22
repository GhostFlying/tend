// Shell-scoped composer text survives navigation without sending an intention.
import type { TaskSummary } from "../api/generated/api.ts";
import { Store } from "../state/store.ts";

export class ComposerDraft {
  readonly state = new Store({ text: "", focus: 0 });
  write(text: string): void {
    this.state.set({ ...this.state.get(), text });
  }
  followUp(task: TaskSummary): void {
    const current = this.state.get();
    const heading = `跟进 / 修改任务 ${task.task_id}`;
    const intention = `${heading}（${task.title}）。\n\n需要补充或修改的内容：\n`;
    this.state.set({
      text: current.text.includes(heading)
        ? current.text
        : current.text + (current.text.trim() ? "\n\n" : "") + intention,
      focus: current.focus + 1,
    });
  }
  restore(text: string): void {
    const current = this.state.get().text;
    this.write(current ? `${text}\n\n${current}` : text);
  }
}
