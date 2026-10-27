import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { clearOldDeploymentData, deploymentChangeTarget } from "./deployment.ts";

afterEach(() => vi.unstubAllGlobals());

test("a delayed old tab never deletes new deployment drafts", async () => {
  const factory = new IDBFactory();
  vi.stubGlobal("indexedDB", factory);
  const list = factory.databases.bind(factory);
  let finish!: (databases: IDBDatabaseInfo[]) => void;
  factory.databases = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const clearing = clearOldDeploymentData("old");
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = factory.open("tend-drafts:deployment:new", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("drafts");
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  db.onversionchange = () => db.close();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction("drafts", "readwrite");
    tx.objectStore("drafts").put("fresh draft", "draft");
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
  finish(await list());
  await clearing;
  expect(await list()).toEqual([{ name: "tend-drafts:deployment:new", version: 1 }]);
  const request = db.transaction("drafts").objectStore("drafts").get("draft");
  expect(
    await new Promise((resolve) => {
      request.onsuccess = () => resolve(request.result);
    }),
  ).toBe("fresh draft");
  db.close();
});

test("delayed broadcasts cannot retire a newer deployment", () => {
  const notice = { previous: "A", deployment: "B" };
  expect(deploymentChangeTarget(notice, "A")).toBe("B");
  expect(deploymentChangeTarget(notice, "B")).toBeNull();
  expect(deploymentChangeTarget(notice, "C")).toBeNull();
  expect(deploymentChangeTarget({ deployment: "B" }, "C")).toBeNull();
});

test("delayed cleanup preserves the new deployment's interrupt and followup journals", async () => {
  const { configureDeployment } = await import("./deployment.ts");
  const { Interrupt } = await import("../conversation/interrupt.ts");
  const { FollowupActions } = await import("../workitems/followupActions.ts");
  configureDeployment("new");
  const disk = {} as Storage;
  Object.defineProperties(disk, {
    length: { get: () => Object.keys(disk).length },
    key: { value: (i: number) => Object.keys(disk)[i] ?? null },
    getItem: { value: (key: string) => disk[key] ?? null },
    setItem: {
      value: (key: string, value: string) => {
        disk[key] = value;
      },
    },
    removeItem: {
      value: (key: string) => {
        delete disk[key];
      },
    },
    clear: {
      value: () => {
        for (const key of Object.keys(disk)) delete disk[key];
      },
    },
  });
  vi.stubGlobal("localStorage", disk);
  const sender = {
    submit: vi.fn(async () => {
      throw new Error("offline");
    }),
    lookup: vi.fn(async () => null),
  };
  await new Interrupt(sender, { storage: disk, newId: () => "interrupt-original" }).start(
    "message",
  );
  const action = new FollowupActions(sender, "item", disk, vi.fn());
  await action.execute({
    operation: "work_items.followups_pause",
    target: { type: "followup", id: "followup" },
    payload: {},
  });
  const original = action.state.get().pending[0]!.request_id;
  disk.setItem("tend.followup-requests.item.legacy", "{}");
  disk.setItem("tend.followup-requests.item.old:deployment:old", "{}");
  expect(new FollowupActions(sender, "item", disk, vi.fn()).state.get().pending).toHaveLength(1);
  await clearOldDeploymentData("old");
  expect(new Interrupt(sender, { storage: disk }).state.get()?.envelope.request_id).toBe(
    "interrupt-original",
  );
  expect(
    new FollowupActions(sender, "item", disk, vi.fn()).state.get().pending[0]?.request_id,
  ).toBe(original);
  await clearOldDeploymentData("new");
  expect(new Interrupt(sender, { storage: disk }).state.get()).toBeNull();
  expect(new FollowupActions(sender, "item", disk, vi.fn()).state.get().pending).toEqual([]);
});

test("instance reset preserves static shell and newer business caches", async () => {
  const remove = vi.fn<(name: string) => Promise<boolean>>(async () => true);
  vi.stubGlobal("caches", {
    keys: async () => [
      "tend-shell-build",
      "tend-api:deployment:old",
      "tend-api:deployment:new",
      "tend-legacy",
      "unrelated",
    ],
    delete: remove,
  });
  await clearOldDeploymentData("old");
  expect(remove.mock.calls.map(([name]) => name)).toEqual([
    "tend-api:deployment:old",
    "tend-legacy",
  ]);
});
