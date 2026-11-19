import type { IssueConversationItem, IssueTranscriptBlock } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";

export interface ProcessDetails {
  items: IssueConversationItem[];
  depth: number;
  after: number;
  processAfter: number;
  more: boolean;
}

interface ProcessRequest {
  block: IssueTranscriptBlock;
  controller: AbortController;
  depth: number;
  promise: Promise<ProcessDetails>;
}

const prefetchLimit = 4;

/** Shares the first detail request for each turn within one open Issue. */
export class IssueProcessReads {
  private readonly first = new Map<string, ProcessRequest>();
  private readonly attempted = new Set<string>();
  private prefetching = false;
  private generation = 0;

  constructor(private readonly api: Reader) {}

  private async page(
    block: IssueTranscriptBlock,
    controller: AbortController,
    priority: RequestPriority,
    previous?: ProcessDetails,
  ): Promise<ProcessDetails> {
    const query = new URLSearchParams({ projection: "issues" });
    if (previous?.after) query.set("after", String(previous.after));
    if (previous?.processAfter) query.set("process_after", String(previous.processAfter));
    const id = encodeURIComponent(block.trigger.message_id);
    const result = await this.api.get<IssueTranscriptBlock>(
      `/api/v1/conversation/blocks/${id}?${query}`,
      controller.signal,
      priority,
    );
    if (result.process?.error) throw new Error(result.process.error);
    const items = new Map(previous?.items.map((item) => [item.message_id, item]));
    // Preserve the public projection's redaction and arrival order. A callback
    // can also carry the actionable choice associated with the same tool.
    for (const item of result.process?.items ?? []) items.set(item.message_id, item);
    for (const item of result.items ?? []) {
      if (!item.tool) continue;
      const existing = items.get(item.message_id);
      items.set(
        item.message_id,
        existing ? { ...existing, ...(item.choices ? { choices: item.choices } : {}) } : item,
      );
    }
    return {
      items: [...items.values()],
      depth: (previous?.depth ?? 0) + 1,
      after: result.next_seq ?? previous?.after ?? 0,
      processAfter: result.process?.next ?? previous?.processAfter ?? 0,
      more: result.more || !!result.process?.more,
    };
  }

  load(
    block: IssueTranscriptBlock,
    priority: RequestPriority = "high",
    depth = 1,
  ): Promise<ProcessDetails> {
    const id = block.trigger.message_id;
    const previous = this.first.get(id);
    const same = previous?.block === block ? previous : undefined;
    if (same && same.depth >= depth) return same.promise;
    if (!same) previous?.controller.abort();
    const controller = same?.controller ?? new AbortController();
    const fetchThroughDepth = async () => {
      let details = same ? await same.promise : await this.page(block, controller, priority);
      while (details.more && details.depth < depth && !controller.signal.aborted) {
        details = await this.page(block, controller, priority, details);
      }
      return details;
    };
    const promise = fetchThroughDepth().catch((error: unknown) => {
      if (this.first.get(id)?.promise === promise) this.first.delete(id);
      throw error;
    });
    this.first.set(id, { block, controller, depth, promise });
    return promise;
  }

  more(block: IssueTranscriptBlock, previous: ProcessDetails): Promise<ProcessDetails> {
    return this.load(block, "high", previous.depth + 1);
  }

  async prefetch(candidates: readonly IssueTranscriptBlock[]) {
    if (this.prefetching) return;
    this.prefetching = true;
    const generation = this.generation;
    try {
      for (const block of candidates) {
        if (generation !== this.generation || this.attempted.size >= prefetchLimit) break;
        const id = block.trigger.message_id;
        if (this.attempted.has(id) || this.first.has(id)) continue;
        this.attempted.add(id);
        // A speculative failure must not affect the discussion or cache a failure.
        await this.load(block, "low").catch(() => {});
      }
    } finally {
      if (generation === this.generation) this.prefetching = false;
    }
  }

  close() {
    this.generation++;
    for (const request of this.first.values()) request.controller.abort();
    this.first.clear();
    this.attempted.clear();
    this.prefetching = false;
  }
}
