/// <reference lib="dom" />
// Read-only projections retain old assessments without presenting them as current work.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";
import type { AttentionView, IssueView } from "../src/api/generated/api.ts";

const output = process.env.TEND_LAYOUT_OUTPUT;
assert(output, "Set an isolated screenshot output directory");
await mkdir(output, { recursive: true });
const root = fixtureRoot();
const now = Date.now();
const issue: IssueView = {
  issue_id: "wi_01ARZ3NDEKTSV4RRFFQ69G5FAV",
  state: "closed",
  work_status: "in_progress",
  effective_work_status: "done",
  assignee: { kind: "owner", ref: "fixture-owner" },
  assignment_epoch: 1,
  assignment_state: "active",
  revision: 81,
  depends_on: [],
  created_at: now - 1000,
  updated_at: now,
  plan: { title: "已完成的发布", project: "sample", intent: "完成既定发布", stages: [] },
  tasks: [],
  progress: {
    summary: "尚未安装所选版本。".repeat(20),
    next_step: "安装先前选定的版本",
    issue_revision: 73,
    updated_at: now - 500,
    updated_by: { kind: "owner", ref: "fixture-owner" },
    historical: true,
    historical_reason: "issue_ended",
  },
  conversation: { can_send: false, reason: "ended" },
};
const information: AttentionView = {
  attention_id: "earlier-information",
  issue_id: issue.issue_id,
  issue_title: issue.plan.title,
  kind: "information",
  title: "早先的安装提示",
  status: "open",
  revision: 1,
  historical: true,
  historical_reason: "issue_ended",
  latest_action_reminders: false,
  raised_by: { kind: "owner", subject: "fixture-owner" },
  created_at: now - 500,
  updated_at: now - 500,
};
const streams = new Set<ServerResponse>();
const writes: string[] = [];
const server = createServer(async (request, response) => {
  const url = new URL(request.url!, "http://localhost");
  const path = url.pathname;
  response.setHeader("Cache-Control", "no-store");
  const json = (value: unknown) => {
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify(value));
  };
  if (path === "/api/v1/session")
    return json({
      token: "fixture",
      login: "owner",
      client_id: "browser",
      scope: "owner",
      deployment_id: "history-fixture",
    });
  if (path === "/api/v1/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(response);
    response.on("close", () => streams.delete(response));
    return;
  }
  if (path === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], decisions: [], hosts: [], projects: [] });
  if (path === `/api/v1/issues/${issue.issue_id}`) return json(issue);
  if (path === "/api/v1/issues") return json({ items: [], more: false });
  if (path === `/api/v1/attentions/${information.attention_id}`) return json(information);
  if (path === "/api/v1/inbox") return json({ items: [information], more: false });
  if (path === "/api/v1/ops") {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk));
    const command = JSON.parse(Buffer.concat(chunks).toString()) as { operation: string };
    writes.push(command.operation);
    return json({ operation: { status: "succeeded", result: information } });
  }
  if (path.startsWith("/api/")) return json({ items: [], more: false });
  try {
    const file = resolve(root, path.split("/").at(-1)?.includes(".") ? `.${path}` : "index.html");
    assert(file.startsWith(root));
    response.setHeader(
      "Content-Type",
      path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html",
    );
    response.end(await readFile(file));
  } catch {
    response.writeHead(404);
    response.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch();
const checks: unknown[] = [];
try {
  for (const width of [1440, 390]) {
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (
        message.text() !== "Service Worker registration blocked by Playwright" &&
        ["warning", "error"].includes(message.type())
      )
        errors.push(message.text());
    });
    await page.goto(`http://127.0.0.1:${address.port}/issues/${issue.issue_id}`);
    await page.getByRole("heading", { name: "历史进展", exact: true }).waitFor();
    const progress = page.locator("[data-issue-progress]");
    const expand = progress.getByRole("button", { name: "展开全文", exact: true });
    const expanded = await expand.isVisible();
    if (expanded) await expand.click();
    else {
      const definition = page
        .locator("details")
        .filter({ has: page.locator("summary", { hasText: "目标与完成条件" }) });
      await definition.locator(":scope > summary").click();
      assert.equal(await definition.getAttribute("open"), "");
    }
    await progress.getByText("当时下一步：安装先前选定的版本", { exact: true }).waitFor();
    assert.match(await page.locator("body").innerText(), /已完成/);
    assert.match(await progress.innerText(), /记录版本：73/);
    assert.equal(
      await progress.getByRole("button", { name: "编辑", exact: true }).isEnabled(),
      false,
    );
    if (expanded)
      assert.equal(
        await progress
          .getByRole("button", { name: "收起", exact: true })
          .getAttribute("aria-expanded"),
        "true",
      );
    await page.screenshot({ path: join(output, `issue-history-${width}.png`), fullPage: false });
    await page.goto(`http://127.0.0.1:${address.port}/inbox/${information.attention_id}`);
    await page.getByText("原责任或状态已结束，此提示保留为历史记录。", { exact: true }).waitFor();
    await page.screenshot({
      path: join(output, `information-history-${width}.png`),
      fullPage: false,
    });
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    assert.deepEqual(errors, []);
    checks.push({
      width,
      url: page.url(),
      title: await page.title(),
      historical_progress: true,
      original_information_status: information.status,
      errors,
    });
    await context.close();
  }
  assert(
    writes.every((operation) => operation === "attentions.present"),
    "reading changed business state",
  );
  await writeFile(
    join(output, "checks.json"),
    JSON.stringify(
      { browser: "Playwright", reason: "Browser plugin not available", checks, writes },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
