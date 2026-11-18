import { IDBFactory } from "fake-indexeddb";
import { expect, test, vi } from "vitest";
import { ApiClient } from "../api/client.ts";
import { LiveData } from "../live/data.ts";
import { ConversationSessions } from "./session.tsx";

test("a quiet cursor resume sends a hidden conversation's original pending message", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const network = { onLine: false };
  vi.stubGlobal("navigator", network);
  const client = new ApiClient();
  const get = vi.spyOn(client, "get").mockResolvedValue({ items: [] });
  const submit = vi.spyOn(client, "submit").mockImplementation(async (envelope) => ({
    operation: {
      request_id: envelope.request_id,
      status: "accepted",
      result: { message_id: "sent", seq: 1 },
    },
  }));
  const root = new LiveData(client);
  const sessions = new ConversationSessions(client);
  const disconnect = sessions.connect(root);
  try {
    const session = sessions.get("issue-chat");
    await session.start();
    const queue = session.queue.get()!;
    const pending = await queue.enqueue({ body: "queued offline" });
    await queue.idle();
    expect(submit).not.toHaveBeenCalled();
    expect(session.active).toBe(false);
    network.onLine = true;
    await root.reload("resume");
    await vi.waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    await queue.idle();
    expect(submit.mock.calls[0]![0]).toEqual(pending.envelope);
    expect(get.mock.calls.map(([path]) => path)).toEqual(["/api/v1/snapshot"]);
  } finally {
    disconnect();
    await sessions.close();
    await root.stop();
    vi.unstubAllGlobals();
  }
});
