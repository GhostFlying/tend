import { describe, expect, test } from "vitest";
import { SseParser } from "./sse.ts";

describe("SseParser", () => {
  test("parses the events tend sends", () => {
    const p = new SseParser();
    const out = p.feed(
      'event: start\ndata: {"event_seq":7}\n\n' +
        'id: 8\nevent: ledger\ndata: {"event_seq":8}\n\n' +
        ": heartbeat\n\n",
    );
    expect(out).toEqual([
      { event: "start", data: '{"event_seq":7}' },
      { event: "ledger", data: '{"event_seq":8}', id: "8" },
    ]);
  });

  test("events split across chunks anywhere", () => {
    const text = 'id: 3\nevent: ledger\ndata: {"a":"中文"}\n\nevent: start\ndata: 1\n\n';
    for (let cut = 0; cut <= text.length; cut++) {
      const p = new SseParser();
      const out = [...p.feed(text.slice(0, cut)), ...p.feed(text.slice(cut))];
      expect(out).toEqual([
        { event: "ledger", data: '{"a":"中文"}', id: "3" },
        { event: "start", data: "1", id: "3" },
      ]);
    }
  });

  test("accepts CRLF and CR line endings, also split between chunks", () => {
    const p = new SseParser();
    expect(p.feed("data: a\r")).toEqual([]);
    expect(p.feed("\n\r\n")).toEqual([{ event: "message", data: "a" }]);
    expect(p.feed("data: b\r\rdata: c\n\n")).toEqual([
      { event: "message", data: "b" },
      { event: "message", data: "c" },
    ]);
  });

  test("joins data lines and ignores comments, unknown fields and empty events", () => {
    const p = new SseParser();
    expect(p.feed(": hi\nretry: 5\nevent: x\n\ndata: one\ndata:two\nfoo\n\n")).toEqual([
      { event: "message", data: "one\ntwo" },
    ]);
  });

  test("an incomplete event waits for its blank line", () => {
    const p = new SseParser();
    expect(p.feed("event: ledger\ndata: 1\n")).toEqual([]);
    expect(p.feed("\n")).toEqual([{ event: "ledger", data: "1" }]);
  });
});
