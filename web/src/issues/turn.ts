import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** Re-read every opened continuation page so mutable replies keep advancing. */
export class IssueTurnRead {
  private pages = 2;
  private loadedPages = 0;
  private trigger?: string | undefined;
  private throughMessage?: string | undefined;
  readonly read: WorkItemRead<IssueTranscriptBlock>;
  readonly state: WorkItemRead<IssueTranscriptBlock>["state"];
  constructor(
    api: Reader,
    private readonly readable = false,
  ) {
    this.read = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal) => {
        const throughMessage = this.throughMessage;
        let block = await api.get<IssueTranscriptBlock>(path, signal);
        const items = new Map((block.items ?? []).map((item) => [item.message_id, item]));
        const processItems = new Map(
          (block.process?.items ?? []).map((item) => [item.message_id, item]),
        );
        let after = block.next_seq ?? 0;
        let processAfter = block.process?.next ?? 0;
        let loadedPages = 1;
        while (!signal?.aborted) {
          const findingMessage =
            throughMessage &&
            (block.more || block.process?.more) &&
            !items.has(throughMessage) &&
            !processItems.has(throughMessage);
          const readingMore =
            loadedPages < Math.max(this.pages, this.loadedPages) &&
            (block.more || block.process?.more);
          if (!findingMessage && !readingMore) break;
          const previousAfter = after;
          const previousProcessAfter = processAfter;
          block = await api.get<IssueTranscriptBlock>(
            `${path}&after=${after}&process_after=${processAfter}`,
            signal,
          );
          for (const item of block.items ?? []) items.set(item.message_id, item);
          for (const item of block.process?.items ?? []) processItems.set(item.message_id, item);
          after = block.next_seq ?? after;
          processAfter = block.process?.next ?? processAfter;
          if (after === previousAfter && processAfter === previousProcessAfter) break;
          loadedPages++;
        }
        if (!signal?.aborted) this.loadedPages = Math.max(this.loadedPages, loadedPages);
        return {
          ...block,
          items: [...items.values()],
          ...(block.process
            ? { process: { ...block.process, items: [...processItems.values()] } }
            : {}),
        } as T;
      },
    });
    this.state = this.read.state;
  }
  open(trigger: string, pages = 2, throughMessage?: string): Promise<void> {
    if (trigger !== this.trigger) {
      this.trigger = trigger;
      this.loadedPages = 0;
    }
    this.pages = pages;
    this.throughMessage = throughMessage;
    return this.read.open(
      `/api/v1/conversation/blocks/${encodeURIComponent(trigger)}?projection=${this.readable ? "readable" : "issues"}`,
    );
  }
  nextDepth(requested: number): number {
    return Math.max(requested, this.loadedPages + 1);
  }
  more(): Promise<void> {
    this.pages = this.nextDepth(this.pages + 1);
    return this.read.refresh();
  }
  refresh(): Promise<void> {
    return this.read.refresh();
  }
  close(): void {
    this.read.close();
    this.trigger = undefined;
    this.loadedPages = 0;
  }
}
