import { issueBoardFixture } from "./issue-fixture.ts";
/// <reference lib="dom" />
// Unknown writes reconcile only against later facts for the same delivery.
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
const output = process.env.TEND_LAYOUT_OUTPUT;
if (output) await mkdir(output, { recursive: true });
import type { ServerResponse } from "node:http";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, type Browser, type BrowserContext, type Page, type Locator } from "playwright";
import type { Envelope, IssueView, IssueDeliveryObservation } from "../src/api/generated/api.ts";

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
  issue_ids: [id],
  primary_issue_id: id,
  primary_issue_title: "Refresh reconciliation",
  runs: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
};
const item: IssueView = {
  issue_id: id,
  plan: {
    title: "Refresh reconciliation",
    intent: "Track review",
    project: "sample",
    stages: [],
    deliveries: [{ delivery_id: "pr", provider: "github", repository: "example/repo", number: 1 }],
  },
  revision: 1,
  assignment_epoch: 1,
  depends_on: [],
  assignment_state: "active",
  assignee: { kind: "owner", ref: "owner" },
  state: "active",
  created_at: 1,
  updated_at: 1,
};
let observations: IssueDeliveryObservation[] = [
  {
    issue_id: id,
    delivery_id: "pr",
    generation: 1,
    state: "open",
    checked_at: 1000,
    refresh_pending: false,
  },
];
const streams = new Set<ServerResponse>();
const requests = new Set<string>();
let event = 0;
let acceptWrites = false;
let missingReceipt = false;
const submissions: Envelope[] = [];
const accepted = new Map<string, Envelope>();
const json = (res: ServerResponse, value: unknown, status = 200) => {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(issueBoardFixture(res.req.url, value)));
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
            return json(res, {
              token: "fixture",
              login: "owner",
              scope: "owner",
              deployment_id: "layout-fixture",
            });
          if (path === "/api/v1/events") {
            res.writeHead(200, { "Content-Type": "text/event-stream" });
            res.write('event: start\ndata: {"event_seq":0}\n\n');
            streams.add(res);
            res.on("close", () => streams.delete(res));
            return;
          }
          if (path === `/api/v1/issues/${id}`) return json(res, item);
          if (path === "/api/v1/issues")
            return json(res, {
              items: [{ ...item, title: item.plan.title, project: item.plan.project }],
            });
          if (path === `/api/v1/issues/${id}/deliveries`)
            return json(res, {
              items: (item.plan.deliveries ?? []).map((delivery) => ({
                delivery,
                observation: observations.find((o) => o.delivery_id === delivery.delivery_id) ?? {
                  issue_id: id,
                  delivery_id: delivery.delivery_id,
                  generation: 0,
                  state: "unknown",
                },
              })),
            });
          if (path === `/api/v1/issues/${id}/followups`)
            return json(res, { issue_id: id, enabled: false, items: [], event_seq: event });
          if (path === `/api/v1/issues/${id}/context`)
            return json(res, { issue_id: id, sources: [], context_revision: 1 });
          if (path === "/api/v1/inbox" || path === "/api/v1/next") return json(res, { items: [] });
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
  await page.goto(`http://127.0.0.1:${address.port}/?issue=${id}`);
  const panel = page.getByRole("dialog", { name: "Issue详情" });
  await panel.getByRole("button", { name: "跟进与交付", exact: true }).click();
  const button = panel.getByRole("button", { name: "重新观察交付", exact: true });
  await button.click();
  const unknown = panel.getByText("刷新请求尚未确认，等待账本事件或连接恢复；不会自动重发。", {
    exact: true,
  });
  await unknown.waitFor();
  assert(await button.isDisabled());
  assert.equal(requests.size, 1);
  if (output) {
    await unknown.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(output, "delivery-unknown.png") });
  }

  // A fresh read and unrelated facts must leave the original uncertainty intact.
  await panel.getByRole("button", { name: "概览", exact: true }).click();
  item.plan.intent = "Unrelated plan update";
  publish();
  await panel.getByRole("button", { name: "概览", exact: true }).click();
  await panel.getByText(`目标：${item.plan.intent}`, { exact: true }).waitFor();
  await panel.getByRole("button", { name: "跟进与交付", exact: true }).click();
  assert(await button.isDisabled());
  await unknown.waitFor();
  observations.push({
    issue_id: id,
    delivery_id: "other",
    generation: 9,
    state: "merged",
    checked_at: 9000,
  });
  item.plan.intent = "Another delivery changed";
  publish();
  await panel.getByRole("button", { name: "概览", exact: true }).click();
  await panel.getByText(`目标：${item.plan.intent}`, { exact: true }).waitFor();
  await panel.getByRole("button", { name: "跟进与交付", exact: true }).click();
  assert(await button.isDisabled());

  // Reserving a query is not a completed observation.
  observations[0] = { ...observations[0]!, generation: 2, refresh_pending: true };
  publish();
  await panel.getByText(/观察请求待回报/).waitFor();
  assert(await button.isDisabled());
  assert.equal(requests.size, 1);

  observations[0] = {
    ...observations[0]!,
    generation: 3,
    checked_at: 2000,
    refresh_pending: false,
  };
  publish();
  await panel
    .getByText("交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。", { exact: true })
    .waitFor();
  await page.waitForFunction(() =>
    [...document.querySelectorAll("button")].some(
      (b) => b.textContent?.trim() === "重新观察交付" && !b.disabled,
    ),
  );
  await panel
    .getByText("交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。", { exact: true })
    .waitFor();
  assert.equal(requests.size, 1, "no automatic re-submit after new ledger facts");
  if (output)
    await panel
      .getByText("交付事实已更新，可查看最新观察；原刷新请求的结果仍未确认。", { exact: true })
      .scrollIntoViewIfNeeded();
  if (output)
    await page.screenshot({ path: join(output, "delivery-completed-facts-original-unknown.png") });
  await button.click();
  await unknown.waitFor();
  assert.equal(requests.size, 2, "another refresh is explicit");
  assert(await button.isDisabled());
  await context.close();
  for (const terminal of ["merged", "closed"]) {
    observations = [
      {
        issue_id: id,
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
    await terminalPage.goto(`http://127.0.0.1:${address.port}/?issue=${id}`);
    const details: Locator = terminalPage.getByRole("dialog", { name: "Issue详情" });
    await details.getByRole("button", { name: "跟进与交付", exact: true }).click();
    const refresh: Locator = details.getByRole("button", { name: "重新观察交付", exact: true });
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
    await details.getByRole("button", { name: "关闭详情" }).click();
    await terminalPage.goto(`http://127.0.0.1:${address.port}/?issue=${id}`);
    await details.getByRole("button", { name: "跟进与交付", exact: true }).click();
    if (output)
      await details
        .getByRole("button", { name: "核对刷新请求", exact: true })
        .scrollIntoViewIfNeeded();
    if (output)
      await terminalPage.screenshot({
        path: join(output, `delivery-${terminal}-reload-unknown.png`),
      });
    await details.getByRole("button", { name: "核对刷新请求", exact: true }).click();
    await details.getByText("未查到原刷新请求，可显式重试同一请求。", { exact: true }).waitFor();
    assert(await refresh.isDisabled());
    assert.equal(requests.size, before + 1);
    acceptWrites = true;
    await details.getByRole("button", { name: "重试原刷新请求", exact: true }).click();
    await details
      .getByRole("button", { name: "核对刷新请求", exact: true })
      .waitFor({ state: "hidden" });
    assert.deepEqual(
      submissions.at(-1),
      original,
      "explicit retry preserves full original envelope",
    );
    assert.equal(requests.size, before + 1, "retry never allocates a fresh request identity");
    assert.equal(await refresh.isDisabled(), false);
    await terminalContext.close();
  }
  // The approved canonical detail replaces legacy discussion templates. Opening
  // its real task backlink and returning must still retain the local text/files.
  const mobileContext = await browser.newContext({
    viewport: { width: 390, height: 760 },
    serviceWorkers: "block",
  });
  const mobilePage = await mobileContext.newPage();
  mobilePage.setDefaultTimeout(10000);
  await mobilePage.goto(`http://127.0.0.1:${address.port}/?task=${taskId}`);
  const taskDrawer = mobilePage.getByRole("dialog", { name: "任务详情", exact: true });
  await taskDrawer.getByText("Linked task", { exact: true }).first().waitFor();
  await taskDrawer.getByRole("button", { name: "关闭详情", exact: true }).click();
  const composer = mobilePage.getByRole("textbox", { name: "消息", exact: true });
  await composer.fill("Existing unsent constraint");
  await mobilePage.locator('input[type="file"]').setInputFiles({
    name: "evidence.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("fixture evidence"),
  });
  const before = requests.size;
  await mobilePage
    .locator("main")
    .getByRole("button", { name: "Linked task", exact: true })
    .click();
  await taskDrawer.getByRole("button", { name: "Refresh reconciliation", exact: true }).click();
  const issueDrawer = mobilePage.getByRole("dialog", { name: "Issue详情", exact: true });
  await issueDrawer.getByRole("heading", { name: item.plan.title, exact: true }).waitFor();
  await issueDrawer.getByRole("button", { name: "关闭详情", exact: true }).click();
  await composer.click({ trial: true });
  assert.equal(await composer.inputValue(), "Existing unsent constraint");
  await mobilePage
    .getByRole("list", { name: "待发送附件" })
    .getByText("evidence.txt", { exact: true })
    .waitFor();
  assert.equal(requests.size, before, "reading and closing never sends the local draft");
  await mobileContext.close();
  console.log(
    "PASS unknown refresh: unrelated facts remain blocked; completed observation restores explicit refresh; no automatic writes; terminal lost-before-server reconnect and same-ID recovery; mobile canonical task-to-Issue reading preserves a reachable draft and attachment",
  );
} finally {
  if (browser) await browser.close();
  for (const res of streams) res.end();
  await server.close();
}
