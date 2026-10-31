import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

/** Re-read every opened continuation page so mutable replies keep advancing. */
export class IssueTurnRead {
  private pages = 2;
  readonly read: WorkItemRead<IssueTranscriptBlock>;
  readonly state: WorkItemRead<IssueTranscriptBlock>["state"];
  constructor(api: Reader) {
    this.read = new WorkItemRead({
      get: async <T>(path: string, signal?: AbortSignal) => {
        let block = await api.get<IssueTranscriptBlock>(path, signal);
        const items = new Map((block.items ?? []).map((item) => [item.message_id, item]));
        for (let i = 1; i < this.pages && block.more && !signal?.aborted; i++) {
          block = await api.get<IssueTranscriptBlock>(`${path}&after=${block.next_seq}`, signal);
          for (const item of block.items ?? []) items.set(item.message_id, item);
        }
        return { ...block, items: [...items.values()] } as T;
      },
    });
    this.state = this.read.state;
  }
  open(trigger: string, pages = 2): Promise<void> {
    this.pages = pages;
    return this.read.open(
      `/api/v1/conversation/blocks/${encodeURIComponent(trigger)}?projection=issues`,
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
