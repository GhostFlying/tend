// What the conversation shows, worked out from the items tend holds and
// the messages still in this device's outbox. Pure, so it can be tested.
import type { ConversationItem, MessageState } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";

/** A task ID: task_ and a ULID, 26 Crockford base32 digits (D72). */
const taskIdPattern = /(?<![0-9A-Za-z_])task_[0-7][0-9A-HJKMNP-TV-Z]{25}(?![0-9A-Za-z_])/g;

/** Splits text into plain runs and task IDs, in order. */
export function splitTaskIds(text: string): (string | { taskId: string })[] {
  const out: (string | { taskId: string })[] = [];
  let last = 0;
  for (const m of text.matchAll(taskIdPattern)) {
    if (m.index > last) {
      out.push(text.slice(last, m.index));
    }
    out.push({ taskId: m[0] });
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    out.push(text.slice(last));
  }
  return out;
}

export const messageStateLabels: Record<MessageState, string> = {
  queued: "已收下",
  processing: "正在处理",
  replied: "已回复",
  interrupted: "已停止",
  failed: "处理失败",
  unknown: "处理结果未知",
};

/** One row of the transcript. */
export type Entry =
  | { kind: "divider"; key: string; at: number }
  | { kind: "system"; key: string; item: ConversationItem }
  | { kind: "owner"; key: string; item: ConversationItem }
  /** A message of this device that tend has not shown in the conversation yet. */
  | { kind: "outgoing"; key: string; message: OutgoingMessage }
  /** What the main agent did for one owner message or ledger event: tool uses and replies. */
  | {
      kind: "agent";
      key: string;
      replyTo: string;
      items: ConversationItem[];
      /** Set once the turn is over: when it ended and how long it took. */
      done?: { at: number; tookMs?: number };
    };

/** A divider with the time goes between entries this far apart. */
const dividerGapMs = 60 * 60 * 1000;

export function buildEntries(
  items: readonly ConversationItem[],
  outbox: readonly OutgoingMessage[],
): Entry[] {
  const entries: Entry[] = [];
  const triggers = new Map<string, ConversationItem>();
  let lastAt = 0;
  const stamp = (at: number, key: string) => {
    if (at - lastAt >= dividerGapMs || !sameDay(at, lastAt)) {
      entries.push({ kind: "divider", key: `d-${key}`, at });
    }
    lastAt = at;
  };
  for (const it of items) {
    const at = Date.parse(it.created_at);
    if (it.author === "system") {
      triggers.set(it.message_id, it);
      stamp(at, it.message_id);
      entries.push({ kind: "system", key: it.message_id, item: it });
      continue;
    }
    if (it.author === "owner") {
      triggers.set(it.message_id, it);
      stamp(at, it.message_id);
      entries.push({ kind: "owner", key: it.message_id, item: it });
      continue;
    }
    const replyTo = it.reply_to ?? "";
    const prev = entries.at(-1);
    if (prev?.kind === "agent" && prev.replyTo === replyTo) {
      prev.items.push(it);
    } else {
      stamp(at, it.message_id);
      entries.push({ kind: "agent", key: it.message_id, replyTo, items: [it] });
    }
  }
  for (const e of entries) {
    if (e.kind !== "agent") {
      continue;
    }
    const trigger = triggers.get(e.replyTo);
    if (trigger?.state && trigger.state !== "processing" && trigger.state !== "queued") {
      const at = Date.parse(e.items.at(-1)!.created_at);
      const took = at - Date.parse(trigger.created_at);
      // Streaming items keep their first-created timestamp, which cannot
      // tell how long the completed turn took.
      const streamed = e.items.some((it) => it.assistant_state !== undefined);
      e.done =
        trigger.state === "replied" && took >= 0 && !streamed ? { at, tookMs: took } : { at };
    }
  }
  const lastSeq = items.at(-1)?.seq ?? 0;
  const shown = new Set(items.map((it) => it.message_id));
  for (const m of outbox) {
    // A sent message shows as tend's item once it arrives; one sent
    // before the loaded page is not shown at all.
    if (m.state === "sent" && (m.messageId === undefined || shown.has(m.messageId))) {
      continue;
    }
    if (m.state === "sent" && m.seq !== undefined && m.seq <= lastSeq) {
      continue;
    }
    entries.push({ kind: "outgoing", key: m.requestId, message: m });
  }
  return entries;
}

/** The owner message or ledger event whose turn is running, if any: the one a stop stops. */
export function processing(items: readonly ConversationItem[]): ConversationItem | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i]!;
    if ((it.author === "owner" || it.author === "system") && it.state === "processing") {
      return it;
    }
  }
  return undefined;
}

/** The main agent's latest message, which a new message replies to. */
export function latestReply(items: readonly ConversationItem[]): ConversationItem | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i]!;
    if (it.author === "main_agent" && it.kind === "message") {
      return it;
    }
  }
  return undefined;
}

/** Fields of a tool's arguments that say what the call is about. */
const telling = ["title", "question", "command", "query", "task_id", "decision_id", "project"];

/** A one-line summary of a tool call's input. */
export function toolSummary(input: string | undefined): string {
  const text = (input ?? "").trim();
  if (text === "") {
    return "";
  }
  try {
    const value: unknown = JSON.parse(text);
    if (value && typeof value === "object" && !Array.isArray(value)) {
      const obj = value as Record<string, unknown>;
      const parts = telling
        .map((k) => obj[k])
        .filter((v): v is string | number => typeof v === "string" || typeof v === "number")
        .map(String);
      if (parts.length > 0) {
        return clip(parts.slice(0, 3).join(" · "), 120);
      }
    }
  } catch {
    // Not JSON: the first line says enough.
  }
  return clip(text.split("\n", 1)[0]!, 120);
}

/** Shows a tool's input or output for reading: JSON indented, the rest as is. */
export function readable(text: string | undefined): string {
  const t = (text ?? "").trim();
  if (t.startsWith("{") || t.startsWith("[")) {
    try {
      return JSON.stringify(JSON.parse(t), null, 2);
    } catch {
      // Cut off or not JSON after all.
    }
  }
  return t;
}

function clip(s: string, n: number): string {
  const chars = [...s];
  return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
}

function sameDay(a: number, b: number): boolean {
  const x = new Date(a);
  const y = new Date(b);
  return (
    x.getFullYear() === y.getFullYear() &&
    x.getMonth() === y.getMonth() &&
    x.getDate() === y.getDate()
  );
}

/** "今天 10:12", "昨天 09:03", "10月20日 18:40", or with the year when not this year. */
export function dayLabel(at: number, now: number = Date.now()): string {
  const d = new Date(at);
  const time = clock(at);
  if (sameDay(at, now)) {
    return `今天 ${time}`;
  }
  if (sameDay(at, now - 24 * 60 * 60 * 1000)) {
    return `昨天 ${time}`;
  }
  const date = `${d.getMonth() + 1}月${d.getDate()}日`;
  return d.getFullYear() === new Date(now).getFullYear()
    ? `${date} ${time}`
    : `${d.getFullYear()}年${date} ${time}`;
}

export function clock(at: number): string {
  const d = new Date(at);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** "6 秒", "2 分 5 秒", "1 小时 3 分". */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) {
    return `${s} 秒`;
  }
  const m = Math.floor(s / 60);
  if (m < 60) {
    return s % 60 === 0 ? `${m} 分` : `${m} 分 ${s % 60} 秒`;
  }
  return `${Math.floor(m / 60)} 小时 ${m % 60} 分`;
}
