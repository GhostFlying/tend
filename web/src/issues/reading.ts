import type { IssueActivityPage, IssueTranscriptBlock } from "../api/generated/api.ts";

export class IssueReadingState {
  top = 0;
  children = false;
  childCursors = [""];
  childCursor(): string | undefined {
    return this.childCursors.at(-1) || undefined;
  }
  saveChildCursors(cursors: string[]): void {
    this.childCursors = cursors;
  }
  activity: IssueActivityPage | null = null;
  activityExpanded = false;
  continuations: Record<string, IssueTranscriptBlock> = {};
  saveContinuation(block: IssueTranscriptBlock): void {
    this.continuations = { ...this.continuations, [block.trigger.message_id]: block };
  }
  private following = false;
  followLatest(): void {
    this.following = true;
  }
  pauseFollowing(): void {
    this.following = false;
  }
  followsLatest(): boolean {
    return this.following;
  }
  saveActivity(page: IssueActivityPage | null): void {
    if (page) this.activity = page;
  }
  expandActivity(): void {
    this.activityExpanded = true;
  }
  saveTop(top: number): void {
    this.top = top;
  }
  expandChildren(): void {
    this.children = true;
  }
}
export class IssueReadingStates {
  private states = new Map<string, IssueReadingState>();
  get(id: string): IssueReadingState {
    let reading = this.states.get(id);
    if (!reading) {
      reading = new IssueReadingState();
      this.states.set(id, reading);
    }
    return reading;
  }
}
