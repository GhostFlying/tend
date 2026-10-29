import { expect, test } from "vitest";
import type { Reader } from "../live/data.ts";
import { Issues } from "./data.ts";
import { Archives } from "../archive/data.ts";

// A completed transport may deliver after abort; selection must still win.
class Deferred implements Reader {
  calls: { path: string; signal?: AbortSignal; answer(value: unknown): void }[] = [];
  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return new Promise((resolve) => {
      this.calls.push({
        path,
        ...(signal ? { signal } : {}),
        answer: (value) => resolve(value as T),
      });
    });
  }
}

test("Issue context switches and stop preserve the selected responsibility", async () => {
  const api = new Deferred(),
    issues = new Issues(api);
  const old = issues.openContext("original/issue", 4);
  const selected = issues.openContext("selected");
  expect(api.calls[0]!.signal?.aborted).toBe(true);
  api.calls[1]!.answer({ issue_id: "selected", revision: 7 });
  await selected;
  api.calls[0]!.answer({ issue_id: "original/issue", revision: 100 });
  await old;
  expect(issues.context.state.get().value).toMatchObject({ issue_id: "selected", revision: 7 });
  const pending = issues.openDeliveries("selected");
  const read = api.calls[2]!;
  issues.stop();
  expect(read.signal?.aborted).toBe(true);
  read.answer({ issue_id: "selected", items: [] });
  await pending;
  expect(issues.deliveries.state.get().value).toBeNull();
});

test("archive selection ignores late old records and downloads use sealed archive URLs", async () => {
  const api = new Deferred(),
    archives = new Archives(api);
  const old = archives.openRecord("sealed/one", "messages", "old-id");
  const selected = archives.openRecord("sealed/two", "messages", "same-id");
  expect(api.calls[0]!.signal?.aborted).toBe(true);
  api.calls[1]!.answer({
    archive_id: "sealed/two",
    id: "same-id",
    body: { original: "preserved" },
  });
  await selected;
  api.calls[0]!.answer({ archive_id: "sealed/one", id: "old-id" });
  await old;
  expect(archives.record.state.get().value).toMatchObject({
    archive_id: "sealed/two",
    id: "same-id",
  });
  const controller = new AbortController();
  const download = archives.originalDownload(
    "sealed/two",
    "attachment",
    "historical/file",
    controller.signal,
  );
  expect(api.calls[2]!.path).toBe(
    "/api/v1/archives/sealed%2Ftwo/originals/attachment/historical%2Ffile/download-url",
  );
  expect(api.calls[2]!.signal).toBe(controller.signal);
  api.calls[2]!.answer({ url: "/api/v1/archives/sealed%2Ftwo/download/ticket" });
  expect((await download).url).toContain("/archives/");
  archives.stop();
  expect(archives.record.state.get().value).toBeNull();
});
