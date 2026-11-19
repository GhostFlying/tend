import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** Re-read every opened continuation page so mutable replies keep advancing. */
export class IssueTurnRead {
  private pages = 2;
  readonly read: WorkItemRead<IssueTranscriptBlock>;
  readonly state: WorkItemRead<IssueTranscriptBlock>["state"];
  constructor(
    api: Reader,
    private readonly readable = false,
  ) {
    this.read = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal) => {
        let block = await api.get<IssueTranscriptBlock>(path, signal);
        const items = new Map((block.items ?? []).map((item) => [item.message_id, item]));
        const processItems = new Map(
          (block.process?.items ?? []).map((item) => [item.message_id, item]),
        );
        let after = block.next_seq ?? 0;
        let processAfter = block.process?.next ?? 0;
        for (
          let i = 1;
          i < this.pages && (block.more || block.process?.more) && !signal?.aborted;
          i++
        ) {
          block = await api.get<IssueTranscriptBlock>(
            `${path}&after=${after}&process_after=${processAfter}`,
            signal,
          );
          for (const item of block.items ?? []) items.set(item.message_id, item);
          for (const item of block.process?.items ?? []) processItems.set(item.message_id, item);
          after = block.next_seq ?? after;
          processAfter = block.process?.next ?? processAfter;
        }
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
  open(trigger: string, pages = 2): Promise<void> {
    this.pages = pages;
    return this.read.open(
      `/api/v1/conversation/blocks/${encodeURIComponent(trigger)}?projection=${this.readable ? "readable" : "issues"}`,
    );
  }
  more(): Promise<void> {
    this.pages++;
    return this.read.refresh();
  }
  refresh(): Promise<void> {
    return this.read.refresh();
  }
  close(): void {
    this.read.close();
  }
}
