/// <reference lib="dom" />
// Unknown writes reconcile only against later facts for the same delivery.
import assert from "node:assert/strict";
import type { ServerResponse } from "node:http";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, type Browser, type BrowserContext, type Page, type Locator } from "playwright";
import type { Envelope, WorkItemView } from "../src/api/generated/api.ts";

const id = "wi_01M5M3ME31875GDA8F7P5XTD74";
const taskId = "task_01M5M3ME31875GDA8F7P5XTD74";
const task = {
  task_id: taskId,
  title: "Linked task",
  project: "sample",
  lifecycle: "completed",
  control_mode: "managed",
  control_revision: 1,
  spec_version: 1,
  rating: "p2",
  block_reason: "none",
  workspace: "sample",
  next_implement_generation: 1,
  work_item_ids: [id],
  runs: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
};
const item: WorkItemView = {
  work_item_id: id,
  title: "Refresh reconciliation",
  intent: "Track review",
  project: "sample",
  revision: 1,
  status: "open",
  column: "unplanned",
  created_at: 1,
  updated_at: 1,
  stages: [],
  deliveries: [{ delivery_id: "pr", provider: "github", repository: "example/repo", number: 1 }],
  observations: [
    {
      work_item_id: id,
      delivery_id: "pr",
      generation: 1,
      state: "open",
      checked_at: 1000,
      refresh_pending: false,
    },
  ],
};
const streams = new Set<ServerResponse>();
const requests = new Set<string>();
let event = 0;
let acceptWrites = false;
let missingReceipt = false;
const submissions: Envelope[] = [];
const accepted = new Map<string, Envelope>();
const json = (res: ServerResponse, value: unknown, status = 200) => {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(value));
};
const server = await createServer({
  configFile: false,
  server: { host: "127.0.0.1", port: 0, watch: { ignored: ["**/*"] } },
  plugins: [
    react(),
    {
      name: "refresh-fixture",
      configureServer(vite) {
        vite.middlewares.use((req, res, next) => {
          const path = new URL(req.url!, "http://fixture.invalid").pathname;
          if (!path.startsWith("/api/")) return next();
          if (path === "/api/v1/session")
            return json(res, { token: "fixture", login: "owner", scope: "owner" });
          if (path === "/api/v1/events") {
            res.writeHead(200, { "Content-Type": "text/event-stream" });
            res.write('event: start\ndata: {"event_seq":0}\n\n');
            streams.add(res);
            res.on("close", () => streams.delete(res));
            return;
          }
          if (path === `/api/v1/work-items/${id}`) return json(res, item);
          if (path === "/api/v1/work-items") return json(res, { items: [item] });
          if (path === "/api/v1/ops") {
            let body = "";
            req.on("data", (chunk: Buffer) => {
              body += chunk.toString();
            });
            req.on("end", () => {
              const envelope = JSON.parse(body) as Envelope;
              requests.add(envelope.request_id);
              submissions.push(envelope);
              if (acceptWrites) {
                accepted.set(envelope.request_id, envelope);
                json(res, {
                  operation: { request_id: envelope.request_id, status: "succeeded", result: {} },
                });
              } else res.destroy();
            });
            return;
          }
          if (path.startsWith("/api/v1/requests/")) {
            const requestId = path.split("/").at(-1)!;
            if (accepted.has(requestId))
              return json(res, { request_id: requestId, status: "succeeded", result: {} });
            return missingReceipt
              ? json(res, { error: { code: "not_found", message: "Not found" } }, 404)
              : json(res, { request_id: requestId, status: "unknown" });
          }
          if (path === "/api/v1/snapshot")
            return json(res, {
              event_seq: 0,
              tasks: [],
              decisions: [],
              hosts: [],
              projects: [],
              queue: [],
            });
          if (path === `/api/v1/tasks/${taskId}`) return json(res, task);
          if (path === "/api/v1/conversation")
            return json(res, {
              items: [
                {
                  seq: 1,
                  message_id: "message-fixture",
                  author: "main_agent",
                  kind: "message",
                  body: `Linked task ${taskId}`,
                  created_at: "2026-01-01T00:00:00Z",
                },
              ],
              more: false,
            });
          if (path === "/api/v1/conversation/settings" || path === "/api/v1/conversation/handoff")
            return json(res, { enabled: false, revision: 1 });
          if (path === "/api/v1/tasks") return json(res, { tasks: [], next: "" });
          if (path === "/api/v1/repos") return json(res, { repos: [] });
          return json(res, {}, 404);
        });
      },
    },
  ],
});
function publish() {
  event++;
  for (const res of streams)
    res.write(
      `event: ledger\ndata: ${JSON.stringify({ event_seq: event, object_type: "work_item", object_id: id, kind: "work_item.delivery_changed", created_at: "2026-01-01T00:00:00Z" })}\n\n`,
    );
}
let browser: Browser | undefined;
try {
  await server.listen();
  const address = server.httpServer!.address();
  assert(address && typeof address !== "string");
  browser = await chromium.launch();
  const context = await browser.newContext({ serviceWorkers: "block" });
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  await page.goto(`http://127.0.0.1:${address.port}/?work_item=${id}`);
  const panel = page.getByRole("dialog", { name: "工作项详情" });
  const button = panel.getByRole("button", { name: "刷新交付状态", exact: true });
  await button.click();
  const unknown = panel.getByText("刷新请求尚未确认，等待账本事件或连接恢复；不会自动重发。", {
    exact: true,
  });
  await unknown.waitFor();
  assert(await button.isDisabled());
  assert.equal(requests.size, 1);

  // A fresh read and unrelated facts must leave the original uncertainty intact.
  item.intent = "Unrelated plan update";
  publish();
  await panel.getByText(item.intent, { exact: true }).waitFor();
  assert(await button.isDisabled());
  await unknown.waitFor();
  item.observations!.push({
    work_item_id: id,
    delivery_id: "other",
    generation: 9,
    state: "merged",
    checked_at: 9000,
  });
  item.intent = "Another delivery changed";
  publish();
  await panel.getByText(item.intent, { exact: true }).waitFor();
  assert(await button.isDisabled());

  // Reserving a query is not a completed observation.
  item.observations![0] = { ...item.observations![0]!, generation: 2, refresh_pending: true };
  publish();
  await panel.getByText("等待重新观察", { exact: true }).waitFor();
  assert(await button.isDisabled());
  assert.equal(requests.size, 1);

  item.observations![0] = {
    ...item.observations![0]!,
    generation: 3,
    checked_at: 2000,
    refresh_pending: false,
  };
  publish();
  await unknown.waitFor({ state: "hidden" });
  await page.waitForFunction(() =>
    [...document.querySelectorAll("button")].some(
      (b) => b.textContent?.trim() === "刷新交付状态" && !b.disabled,
    ),
  );
  await panel
    .getByText("交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。", { exact: true })
    .waitFor();
  assert.equal(requests.size, 1, "no automatic re-submit after new ledger facts");
  await button.click();
  await unknown.waitFor();
  assert.equal(requests.size, 2, "another refresh is explicit");
  assert(await button.isDisabled());
  await context.close();
  for (const terminal of ["merged", "closed"]) {
    item.observations = [
      {
        work_item_id: id,
        delivery_id: "pr",
        generation: 10,
        state: terminal,
        checked_at: 1000,
        refresh_pending: false,
      },
    ];
    missingReceipt = true;
    acceptWrites = false;
    const terminalContext: BrowserContext = await browser.newContext({ serviceWorkers: "block" });
    const terminalPage: Page = await terminalContext.newPage();
    terminalPage.setDefaultTimeout(10000);
    await terminalPage.goto(`http://127.0.0.1:${address.port}/?work_item=${id}`);
    const details: Locator = terminalPage.getByRole("dialog", { name: "工作项详情" });
    const refresh: Locator = details.getByRole("button", { name: "刷新交付状态", exact: true });
    const before: number = requests.size;
    await refresh.click();
    await details.getByRole("button", { name: "核对刷新请求", exact: true }).waitFor();
    assert.equal(requests.size, before + 1);
    const original = submissions.at(-1)!;
    // Reconnect and reread preserve unknown, even with no future provider polls.
    await terminalContext.setOffline(true);
    for (const stream of streams) stream.end();
    streams.clear();
    await terminalContext.setOffline(false);
    await terminalPage.evaluate(() => window.dispatchEvent(new Event("online")));
    await terminalPage.waitForFunction(() =>
      [...document.querySelectorAll("aside span")].some((el) => el.textContent === "已连接"),
    );
    assert(await refresh.isDisabled());
    assert.equal(requests.size, before + 1);
    // Closing the dialog must not discard the original operation.
    await details.getByRole("button", { name: "关闭工作项详情" }).click();
    await terminalPage
      .locator("aside")
      .getByRole("button", { name: /Refresh reconciliation/ })
      .click();
    await details.getByRole("button", { name: "核对刷新请求", exact: true }).click();
    await details.getByText("未查到原刷新请求，可显式重试同一请求。", { exact: true }).waitFor();
    assert(await refresh.isDisabled());
    assert.equal(requests.size, before + 1);
    acceptWrites = true;
    await details.getByRole("button", { name: "重试原刷新请求", exact: true }).click();
    await details.getByText("已请求重新观察，等待执行主机回报。", { exact: true }).waitFor();
    assert.deepEqual(
      submissions.at(-1),
      original,
      "explicit retry preserves full original envelope",
    );
    assert.equal(requests.size, before + 1, "retry never allocates a fresh request identity");
    assert.equal(await refresh.isDisabled(), false);
    await terminalContext.close();
  }
  for (const [label, status] of [
    ["继续讨论", "open"],
    ["提出暂缓安排", "open"],
    ["讨论结束跟进", "open"],
    ["提出恢复安排", "snoozed"],
    ["提出重新跟进", "closed"],
  ] as const) {
    item.status = status;
    const mobileContext = await browser.newContext({
      viewport: { width: 390, height: 760 },
      serviceWorkers: "block",
    });
    const mobilePage = await mobileContext.newPage();
    mobilePage.setDefaultTimeout(10000);
    await mobilePage.goto(`http://127.0.0.1:${address.port}/?task=${taskId}`);
    const taskDrawer = mobilePage.getByRole("region", { name: "任务详情", exact: true });
    await taskDrawer.getByText("Linked task", { exact: true }).waitFor();
    await taskDrawer.getByRole("button", { name: "关闭", exact: true }).click();
    const composer = mobilePage.getByRole("textbox", { name: "消息", exact: true });
    await composer.fill("Existing unsent constraint");
    await mobilePage.locator('input[type="file"]').setInputFiles({
      name: "evidence.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("fixture evidence"),
    });
    const before: number = requests.size;
    await mobilePage
      .locator("main")
      .getByRole("button", { name: "Linked task", exact: true })
      .click();
    await taskDrawer.getByRole("button", { name: item.title, exact: true }).click();
    await mobilePage
      .getByRole("dialog", { name: "工作项详情" })
      .getByRole("button", { name: label, exact: true })
      .click();
    await composer.click({ trial: true });
    const proof = await composer.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      const app = document.querySelector<HTMLElement>("[data-details]")!;
      return {
        details: app.dataset.details,
        view: app.dataset.view,
        covered: hit !== el && !el.contains(hit),
      };
    });
    assert.deepEqual(proof, { details: "closed", view: "chat", covered: false });
    assert((await composer.inputValue()).includes("Existing unsent constraint"));
    assert((await composer.inputValue()).includes(id));
    await mobilePage
      .getByRole("list", { name: "待发送附件" })
      .getByText("evidence.txt", { exact: true })
      .waitFor();
    assert.equal(requests.size, before, "discussion keeps text and files local without sending");
    await mobileContext.close();
  }
  console.log(
    "PASS unknown refresh: unrelated facts remain blocked; completed observation restores explicit refresh; no automatic writes; terminal lost-before-server reconnect and same-ID recovery; mobile task-to-work-item discussion preserves a reachable draft and attachment",
  );
} finally {
  if (browser) await browser.close();
  for (const res of streams) res.end();
  await server.close();
}
