// A parser for server-sent events, as the owner API's event stream sends
// them (D69). The stream is read with fetch rather than EventSource, which
// cannot send the bearer token, so the parsing is ours. It follows the
// HTML standard's rules for the fields the stream uses.

/** One dispatched event. */
export interface SseMessage {
  /** The event type; "message" when the event names none. */
  event: string;
  data: string;
  /** The last event ID the stream set, if any. */
  id?: string;
}

export class SseParser {
  private buffer = "";
  private event = "";
  private data: string[] = [];
  private lastId: string | undefined;

  /** Takes the next piece of the stream and returns the events it completes. */
  feed(text: string): SseMessage[] {
    this.buffer += text;
    const out: SseMessage[] = [];
    for (;;) {
      const end = this.buffer.search(/[\r\n]/);
      if (end < 0) {
        return out;
      }
      if (this.buffer[end] === "\r" && end === this.buffer.length - 1) {
        // A CR at the end may be the first half of a CRLF.
        return out;
      }
      const line = this.buffer.slice(0, end);
      const width = this.buffer.startsWith("\r\n", end) ? 2 : 1;
      this.buffer = this.buffer.slice(end + width);
      const message = this.line(line);
      if (message) {
        out.push(message);
      }
    }
  }

  private line(line: string): SseMessage | undefined {
    if (line === "") {
      return this.dispatch();
    }
    if (line.startsWith(":")) {
      // A comment, such as the heartbeat.
      return undefined;
    }
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) {
      value = value.slice(1);
    }
    switch (field) {
      case "event":
        this.event = value;
        break;
      case "data":
        this.data.push(value);
        break;
      case "id":
        if (!value.includes("\0")) {
          this.lastId = value;
        }
        break;
    }
    return undefined;
  }

  private dispatch(): SseMessage | undefined {
    const event = this.event || "message";
    const data = this.data;
    this.event = "";
    this.data = [];
    if (data.length === 0) {
      return undefined;
    }
    const message: SseMessage = { event, data: data.join("\n") };
    if (this.lastId !== undefined) {
      message.id = this.lastId;
    }
    return message;
  }
}
