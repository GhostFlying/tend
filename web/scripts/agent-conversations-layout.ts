import { navigate, fixtureRoot } from "./navigation.ts";
import { issueBoardFixture } from "./issue-fixture.ts";
/// <reference lib="dom" />
// Synthetic conversations exercise UI routing; no native harness or deployed API.
import { closeIssue } from "./issue-detail-helpers.ts";
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { chromium } from "playwright";
import type { IssueConversationItem, Envelope } from "../src/api/generated/api.ts";
const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-agent-layout");
await mkdir(output, { recursive: true });
const names = ["后台 Agent 与上下文管理", "消息关联标签与历史来源追溯", "跨会话输入与附件隔离验证"];
const associations = {
  message_id: "owner-main",
  content_revision: 1,
  disposition: "linked",
  facts_reviewed: true,
  owner_locked: true,
  revision: 1,
  items: names.map((title, i) => ({
    issue_id: `work-${i}`,
    title,
    source: "owner",
    owner_locked: true,
  })),
};
const conversations = names.map((title, i) => ({
  conversation_id: `work-item:work-${i}`,
  kind: "issue",
  issue_id: `work-${i}`,
  title,
  revision: 1,
  model: "",
  effort: "",
  last_message_seq: 10 - i,
  last_message_at: Date.now() - i * 60000,
  preview: "已读取相关消息，正在核对最新约束。",
  status: i === 0 ? "running" : "idle",
}));
const owner: IssueConversationItem = {
  message_id: "owner-main",
  seq: 1,
  kind: "message",
  author: "owner",
  body: "我们可以进一步扩大计划：主 Agent 里的消息应该有工作项标签，关联可以直接纠正。",
  created_at: new Date().toISOString(),
  state: "replied",
  associations,
};
const reply: IssueConversationItem = {
  message_id: "reply-main",
  seq: 2,
  kind: "message",
  author: "main_agent",
  body: "已把消息关联纳入计划。\n\n工作项标签显示在消息底部；后台会话按工作项组织。你可以查看处理过程，也可以直接补充指令。业务调度仍由主 Agent 负责。",
  reply_to: owner.message_id,
  created_at: new Date().toISOString(),
  associations: {
    ...associations,
    message_id: "reply-main",
    items: associations.items.slice(0, 2),
  },
  return_link: {
    conversation_id: conversations[0]!.conversation_id,
    message_id: "background-source",
  },
};
const streams = new Set<ServerResponse>();
const writes: Envelope[] = [];
const settings = {
  enabled: true,
  available: true,
  revision: 1,
  model: "gpt-6.1-sol",
  effort: "medium",
  models: [
    {
      id: "gpt-6.1-sol",
      label: "GPT-6.1-Sol",
      supported_efforts: ["medium", "high"],
      default_effort: "medium",
    },
  ],
};
let dropAssociationReply = false;
let dropLookupReply = false;
let lostRequestID = "";
function json(res: ServerResponse, value: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(issueBoardFixture(res.req.url, value)));
}
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://fixture.invalid"),
    p = decodeURIComponent(u.pathname),
    id = u.searchParams.get("conversation_id") ?? "main";
  if (p === "/api/v1/session")
    return json(res, {
      token: "fixture",
      login: "Example owner",
      scope: "owner",
      deployment_id: "layout-fixture",
    });
  if (p === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/snapshot")
    return json(res, {
      event_seq: 1,
      tasks: [],
      decisions: [],
      hosts: [],
      queue: [],
      projects: [],
    });
  if (p === "/api/v1/hosted-agents") return json(res, { items: [] });
  if (p === "/api/v1/repos") return json(res, { items: [], revision: 0 });
  if (p === "/api/v1/tasks") return json(res, { items: [], more: false });
  if (p === "/api/v1/agent-conversations")
    return json(res, {
      runtime_available: true,
      items: conversations.filter((c) => c.title.includes(u.searchParams.get("search") ?? "")),
    });
  if (p.endsWith("/usage"))
    return json(res, {
      available: true,
      compact_supported: true,
      thread_id: id,
      revision: 4,
      used_percentage: 42,
      active_tokens: 108528,
      effective_window: 258400,
      measurement: "observed",
      sampled_at: Date.now(),
      updated_at: Date.now(),
    });
  if (p.endsWith("/compactions"))
    return json(res, {
      items: [
        {
          operation_id: "compact-example",
          conversation_id: id,
          thread_id: id,
          source: "automatic",
          status: "completed",
          created_at: Date.now() - 3600000,
          updated_at: Date.now() - 3600000,
        },
      ],
    });
  if (p.endsWith("/rollout"))
    return json(res, {
      conversation_id: id,
      coverage: "Fixture",
      threads: ["thread-example"],
      records: [
        {
          seq: 1,
          thread_id: "thread-example",
          turn_id: "turn-example",
          item_id: "tool-example",
          kind: "item_completed",
          data: '{"tool":"issue_context_read","result":"已读取最新来源"}',
          characters: 64,
          truncated: false,
          created_at: Date.now(),
        },
      ],
      next: 1,
      more: false,
    });
  if (p.startsWith("/api/v1/agent-conversations/"))
    return json(
      res,
      conversations.find((c) => p.endsWith(c.conversation_id)) ?? {
        conversation_id: "main",
        title: "主 Agent",
        revision: 1,
      },
    );
  if (p.endsWith("/associations")) return json(res, associations);
  if (p === "/api/v1/inbox" || p === "/api/v1/next-actions") return json(res, { items: [] });
  if (p.endsWith("/activity") || p.endsWith("/results") || p.endsWith("/deliveries"))
    return json(res, { items: [], more: false, event_seq: 1 });
  if (p === "/api/v1/issues")
    return json(res, {
      items: names.map((title, i) => ({
        issue_id: `work-${i}`,
        title,
        project: "",
        state: "active",
        revision: 1,
        assignee: { kind: "owner" },
        assignment_epoch: 1,
        assignment_state: "active",
      })),
    });
  if (/^\/api\/v1\/issues\/[^/]+$/.test(p)) {
    const index = names.findIndex((_, i) => p.endsWith(`work-${i}`));
    if (index >= 0)
      return json(res, {
        issue_id: `work-${index}`,
        plan: { title: names[index], project: "", intent: "", stages: [] },
        conversation: { conversation_id: conversations[index]!.conversation_id, can_send: true },
        created_at: 1,
        updated_at: 1,
        depends_on: [],
        tasks: [],
        state: "active",
        revision: 1,
        assignment_epoch: 1,
        assignment_state: "active",
        assignee: { kind: "owner" },
      });
  }
  if (p === "/api/v1/conversation")
    return json(res, {
      items:
        id === "main"
          ? [owner, reply]
          : [
              {
                ...reply,
                message_id: "background-reply",
                conversation_id: id,
                body: "已读取工作项的关联消息与最新约束，正在核对处理记录。",
                return_link: undefined,
              },
            ],
      more: false,
    });
  if (p === "/api/v1/conversation/queue")
    return json(res, {
      enabled: true,
      category: u.searchParams.get("category") ?? "owner",
      items: [],
      total: 0,
      more: false,
      event_seq: 1,
    });
  if (p === "/api/v1/conversation/transcript")
    return json(res, {
      blocks: [
        {
          position: 1,
          trigger:
            id === "main"
              ? owner
              : {
                  ...owner,
                  message_id: "background-owner",
                  conversation_id: id,
                  body: "核对这个工作项的上下文。",
                  associations: { ...associations, items: associations.items.slice(0, 1) },
                },
          items: [
            id === "main"
              ? reply
              : {
                  ...reply,
                  message_id: "background-reply",
                  conversation_id: id,
                  body: "已读取工作项的关联消息与最新约束，正在核对处理记录。",
                  return_link: undefined,
                },
          ],
          more: false,
        },
      ],
      more: false,
      event_seq: 1,
    });
  if (p === "/api/v1/conversation/settings") return json(res, settings);
  if (p === "/api/v1/conversation/handoff") return json(res, { enabled: false, revision: 1 });
  if (p.startsWith("/api/v1/requests/")) {
    if (dropLookupReply) return res.destroy();
    const requestId = p.slice("/api/v1/requests/".length);
    return writes.some((w) => w.request_id === requestId)
      ? json(res, { request_id: requestId, status: "succeeded", result: {} })
      : json(res, { error: { code: "not_found", message: "Unknown request" } }, 404);
  }
  if (p === "/api/v1/ops") {
    let raw = "";
    for await (const chunk of req) raw += String(chunk);
    const e = JSON.parse(raw) as Envelope;
    const prior = writes.find((w) => w.request_id === e.request_id);
    if (prior) {
      assert.deepEqual(e, prior, "request identity must retain the original payload");
      if (e.request_id === lostRequestID && dropLookupReply) return res.destroy();
      return json(res, {
        operation: { request_id: e.request_id, status: "succeeded", result: {} },
      });
    }
    writes.push(e);
    if (e.operation === "conversation.set_settings") {
      Object.assign(settings, e.payload);
      settings.revision++;
    }
    if (e.operation === "conversation.set_issue_associations") {
      if (e.expected_control_revision !== associations.revision)
        return json(res, {
          operation: {
            request_id: e.request_id,
            status: "failed",
            error: { code: "stale_revision", message: "关联已被修改，请重新检查选择。" },
          },
        });
      associations.revision++;
      associations.disposition = (e.payload as { disposition: string }).disposition;
      const selected = (e.payload as { issue_ids: string[] }).issue_ids;
      associations.items = names.flatMap((title, i) =>
        selected.includes(`work-${i}`)
          ? [{ issue_id: `work-${i}`, title, source: "owner", owner_locked: true }]
          : [],
      );
    }
    if (e.operation === "conversation.set_issue_associations" && dropAssociationReply) {
      dropAssociationReply = false;
      lostRequestID = e.request_id;
      return res.destroy();
    }
    return json(res, { operation: { request_id: e.request_id, status: "succeeded", result: {} } });
  }
  if (p.startsWith("/api/")) console.log("UNHANDLED", p, u.search);
  if (p.startsWith("/api/"))
    return json(res, { error: { code: "not_found", message: "Not in fixture" } }, 404);
  try {
    const file = resolve(root, p.split("/").at(-1)?.includes(".") ? `.${p}` : "index.html");
    assert(file.startsWith(root));
    res.setHeader(
      "Content-Type",
      p.endsWith(".js") ? "text/javascript" : p.endsWith(".css") ? "text/css" : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404);
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch({
  ...(process.env.TEND_LAYOUT_BROWSER ? { executablePath: process.env.TEND_LAYOUT_BROWSER } : {}),
});
try {
  for (const width of [1505, 1280, 390]) {
    settings.effort = "medium";
    associations.disposition = "linked";
    associations.items = names.map((title, i) => ({
      issue_id: `work-${i}`,
      title,
      source: "owner",
      owner_locked: true,
    }));
    const context = await browser.newContext({
      viewport: { width, height: width === 1505 ? 1045 : 900 },
      hasTouch: width === 390,
      isMobile: width === 390,
      serviceWorkers: "block",
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const consoleErrors: string[] = [];
    page.on("console", (message) => {
      if (message.text() === "Service Worker registration blocked by Playwright") return;
      if (["warning", "error"].includes(message.type())) consoleErrors.push(message.text());
    });
    page.setDefaultTimeout(15000);
    await page.goto(`http://127.0.0.1:${address.port}`);
    if (process.env.TEND_LAYOUT_FONT) {
      const font = await readFile(process.env.TEND_LAYOUT_FONT);
      await page.addStyleTag({
        content: `@font-face{font-family:FixtureChinese;src:url(data:font/otf;base64,${font.toString("base64")})}body,button,input,select,textarea{font-family:FixtureChinese,sans-serif!important}`,
      });
      await page.evaluate(() => document.fonts.ready);
    }
    await page.getByRole("button", { name: "上下文用量 42%" }).waitFor();
    assert.equal(await page.title(), "tend");
    assert.equal(new URL(page.url()).host, `127.0.0.1:${address.port}`);
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    const selector = page.getByRole("button", { name: "主会话设置", exact: true });
    await selector.waitFor();
    const before = await selector.boundingBox();
    assert(before);
    const labels: string[] = [];
    await page.exposeFunction("recordSettingLabel", (label: string) => labels.push(label));
    await selector.evaluate((element) => {
      new MutationObserver(() => {
        (window as unknown as { recordSettingLabel: (value: string) => void }).recordSettingLabel(
          element.textContent ?? "",
        );
      }).observe(element, { subtree: true, childList: true, characterData: true });
    });
    let releaseSave!: () => void;
    const saveHeld = new Promise<void>((done) => (releaseSave = done));
    await page.route("**/api/v1/ops", async (route) => {
      if (route.request().postDataJSON().operation === "conversation.set_settings") await saveHeld;
      await route.continue();
    });
    await selector.click();
    await page.getByRole("menuitem", { name: "思考强度", exact: true }).click();
    await page.getByRole("menuitemradio", { name: "high", exact: true }).click();
    await selector.locator('[role="status"][aria-label="保存中"]').waitFor();
    assert.equal(await selector.innerText(), "GPT-6.1-Sol\nmedium");
    assert.deepEqual(await selector.boundingBox(), before, "saving must not move the selector");
    releaseSave();
    await page.waitForFunction(() =>
      document.querySelector('[aria-label="主会话设置"]')?.textContent?.includes("high"),
    );
    assert(labels.every((label) => ["GPT-6.1-Solmedium", "GPT-6.1-Solhigh"].includes(label)));
    await page.unroute("**/api/v1/ops");
    const ring = page.getByRole("button", { name: "上下文用量 42%" });
    const details = page.getByRole("dialog", { name: "上下文用量", exact: true });
    const input = page.getByRole("textbox", { name: "消息", exact: true });
    await input.focus();
    assert.equal(await ring.innerText(), "", "percentage must only appear in details");
    const focusBeforeHover = await page.evaluate(() =>
      document.activeElement?.getAttribute("aria-label"),
    );
    await ring.hover();
    await details.waitFor();
    assert.equal(
      await page.evaluate(() => document.activeElement?.getAttribute("aria-label")),
      focusBeforeHover,
      "hover must not move keyboard focus",
    );
    await details.getByText("42% 已使用", { exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await details.waitFor({ state: "hidden" });
    assert(await input.evaluate((element) => element === document.activeElement));
    await page.mouse.move(1, 1);
    await ring.hover();
    await details.waitFor();
    await details.getByRole("button", { name: "手动 compact", exact: true }).hover();
    await page.waitForTimeout(250);
    assert(await details.isVisible(), "pointer can enter and use details across the gap");
    await ring.hover();
    await page.waitForTimeout(250);
    assert(await details.isVisible(), "details stay open when the pointer returns to the ring");
    await selector.focus();
    await page.keyboard.press("ArrowDown");
    const settingsMenu = page.getByRole("menu", { name: "主会话设置菜单", exact: true });
    await settingsMenu.waitFor();
    await page.keyboard.press("ArrowRight");
    await page.getByRole("menu", { name: "选择模型", exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await settingsMenu.waitFor();
    assert.equal(await selector.getAttribute("aria-expanded"), "true");
    await page.keyboard.press("Escape");
    await settingsMenu.waitFor({ state: "hidden" });
    assert(await details.isVisible(), "the focused menu handles Escape before hover details");
    await page.keyboard.press("Escape");
    await details.waitFor({ state: "hidden" });
    assert(await selector.evaluate((element) => element === document.activeElement));
    await page.mouse.move(1, 1);
    await ring.hover();
    await details.waitFor();
    await page.screenshot({ path: join(output, `context-hover-${width}.png`), fullPage: true });
    await page.mouse.move(1, 1);
    await details.waitFor({ state: "hidden" });
    await selector.focus();
    await page.keyboard.press("Tab");
    assert(await ring.evaluate((element) => element === document.activeElement));
    await details.waitFor();
    await page.keyboard.press("Enter");
    assert(await details.evaluate((element) => element === document.activeElement));
    await page.keyboard.press("Tab");
    assert(
      await details
        .getByRole("button", { name: "手动 compact", exact: true })
        .evaluate((element) => element === document.activeElement),
    );
    await page.keyboard.press("Escape");
    await details.waitFor({ state: "hidden" });
    assert(await ring.evaluate((element) => element === document.activeElement));
    if (width === 390) await ring.tap();
    else await ring.click();
    await details.waitFor();
    await page.mouse.click(1, 1);
    await details.waitFor({ state: "hidden" });
    assert.deepEqual(consoleErrors, [], "composer interactions must not emit console errors");
    await page.getByText(owner.body!, { exact: true }).waitFor();
    await page.getByRole("textbox", { name: "消息", exact: true }).fill("主会话草稿保留");
    const mainInput = page.getByRole("textbox", { name: "消息", exact: true });
    await mainInput.evaluate((element) => {
      element.setAttribute("data-retained-composer", "main");
    });
    const transcript = page.getByRole("region", { name: "对话记录", exact: true });
    const mainTop = await transcript.evaluate((element) => element.scrollTop);
    for (const [opener, dialogName] of [
      ["来源", "补充来源"],
      ["切换会话：主 Agent", "会话"],
      ["查看处理记录", "持久处理记录"],
      ["上下文用量 42%", "上下文用量"],
    ]) {
      await navigate(page, "/issues");
      await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
      await navigate(page, "/");
      await mainInput.waitFor();
      if (opener === "来源" || opener === "查看处理记录") {
        await page.getByRole("button", { name: "更多", exact: true }).click();
        await page
          .getByRole("menuitem", { name: opener === "来源" ? "补充来源" : "执行记录", exact: true })
          .click();
      } else await page.getByRole("button", { name: opener!, exact: true }).click();
      await page.getByRole("dialog", { name: dialogName!, exact: true }).waitFor();
      await page.goBack();
      await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
      assert.equal(await page.locator("dialog[open]").count(), 0);
      assert.equal(await page.getByRole("dialog").count(), 0);
      const filter = page.getByLabel("按项目筛选Issue");
      await filter.click();
      assert(await filter.evaluate((element) => element === document.activeElement));
      if (width === 390) await page.getByRole("navigation", { name: "视图" }).waitFor();
      await page.screenshot({ path: join(output, `overlay-back-${dialogName}-${width}.png`) });
      await navigate(page, "/");
      assert.equal(await mainInput.getAttribute("data-retained-composer"), "main");
      assert.equal(await mainInput.inputValue(), "主会话草稿保留");
      assert.equal(await transcript.evaluate((element) => element.scrollTop), mainTop);
      assert.equal(await page.getByRole("dialog").count(), 0, "closed overlays do not reopen");
    }
    await page
      .getByRole("textbox", { name: "消息", exact: true })
      .locator("..")
      .screenshot({ path: join(output, `composer-after-${width}.png`) });
    await page.screenshot({ path: join(output, `tags-${width}.png`), fullPage: true });
    await page.getByRole("button", { name: /^切换会话：/ }).click();
    await page.getByRole("dialog", { name: "会话" }).waitFor();
    await page.screenshot({ path: join(output, `drawer-${width}.png`), fullPage: true });
    let releaseSelection!: () => void;
    let selectionArrived!: () => void;
    const selectionHeld = new Promise<void>((resolve) => {
      releaseSelection = resolve;
    });
    const selectionRequested = new Promise<void>((resolve) => {
      selectionArrived = resolve;
    });
    const selectionRoute = `**/api/v1/agent-conversations/${encodeURIComponent(conversations[0]!.conversation_id)}?*`;
    await page.route(selectionRoute, async (route) => {
      selectionArrived();
      await selectionHeld;
      await route.fulfill({ json: conversations[0] });
    });
    await page
      .getByRole("dialog", { name: "会话" })
      .getByRole("button")
      .filter({ hasText: names[0]! })
      .click();
    await selectionRequested;
    await page.getByRole("heading", { name: "会话", exact: true, level: 1 }).waitFor();
    assert.equal(await page.getByRole("heading", { level: 1 }).count(), 1);
    const refreshedShell = page.waitForResponse((response) =>
      response.url().endsWith("/api/v1/snapshot"),
    );
    for (const stream of streams)
      stream.write(
        `id: 2\nevent: ledger\ndata: ${JSON.stringify({ event_seq: 2, kind: "issue.updated", object_type: "issue", object_id: "work-0", created_at: new Date().toISOString() })}\n\n`,
      );
    await refreshedShell;
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    releaseSelection();

    const backgroundInput = page
      .getByRole("region", { name: "Issue详情", exact: true })
      .getByRole("textbox", { name: "消息", exact: true });
    await backgroundInput.waitFor();
    await page.unroute(selectionRoute);
    assert.equal(
      decodeURIComponent(new URL(page.url()).pathname.slice("/issues/".length)),
      "work-0",
    );
    assert.equal(await page.getByRole("dialog", { name: "会话", exact: true }).count(), 0);
    assert.equal(await backgroundInput.inputValue(), "");
    await backgroundInput.fill("后台独立草稿");
    await page
      .locator("summary")
      .filter({ hasText: /^会话设置$/ })
      .click();
    await page.getByRole("button", { name: "上下文用量 42%" }).click();
    await page.getByRole("dialog", { name: "上下文用量", exact: true }).waitFor();
    const box = await page.getByRole("dialog", { name: "上下文用量", exact: true }).boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= width, "ctx popover must fit viewport");
    await page.screenshot({ path: join(output, `background-ctx-${width}.png`), fullPage: true });
    await page
      .getByRole("dialog", { name: "上下文用量", exact: true })
      .getByRole("button", { name: "关闭", exact: true })
      .click();
    await closeIssue(page);
    assert.equal(
      await page.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
      "主会话草稿保留",
    );
    await page.getByRole("button", { name: "修改Issue关联", exact: true }).first().click();
    const editor = page.getByRole("dialog", { name: "修改Issue关联", exact: true });
    await editor.getByRole("checkbox").first().waitFor();
    const openedRevision = associations.revision;
    associations.revision++;
    associations.items = associations.items.slice(0, 2);
    for (const stream of streams)
      stream.write(
        `id: 3\nevent: ledger\ndata: ${JSON.stringify({ event_seq: 3, kind: "conversation.associations_changed", object_type: "message", object_id: owner.message_id, created_at: new Date().toISOString() })}\n\n`,
      );
    await page.waitForFunction(
      () =>
        !Array.from(document.querySelectorAll("button")).some(
          (b) => b.textContent?.trim() === "+1",
        ),
    );
    await editor.getByRole("button", { name: "保存关联" }).click();
    await editor.getByRole("alert").filter({ hasText: "关联已被修改" }).waitFor();
    assert.equal(
      writes.at(-1)?.expected_control_revision,
      openedRevision,
      "an SSE update must not silently rebase an open edit",
    );
    await editor.getByRole("checkbox").first().uncheck();
    await editor.getByRole("button", { name: "保存关联" }).click();
    await editor.waitFor({ state: "hidden" });
    assert(writes.some((e) => e.operation === "conversation.set_issue_associations"));
    // The server commits but both the response and initial receipt lookup are lost.
    await page.getByRole("button", { name: "修改Issue关联", exact: true }).first().click();
    await editor.getByRole("checkbox").first().check();
    dropAssociationReply = true;
    dropLookupReply = true;
    await editor.getByRole("button", { name: "保存关联" }).click();
    await editor.getByRole("button", { name: "核对原请求" }).waitFor();
    const beforeRecovery = writes.length;
    await page.reload();
    dropLookupReply = false;
    await page.getByRole("button", { name: "修改Issue关联", exact: true }).first().click();
    await editor.getByRole("button", { name: "核对原请求" }).click();
    await editor.waitFor({ state: "hidden" });
    assert.equal(writes.length, beforeRecovery, "reload recovery must not create a second write");
    // A delayed background lookup must not override a later return to main.
    let release!: () => void;
    let requested!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const arrived = new Promise<void>((resolve) => {
      requested = resolve;
    });
    const route = `**/api/v1/agent-conversations/${encodeURIComponent(conversations[1]!.conversation_id)}?*`;
    await page.route(route, async (r) => {
      requested();
      await held;
      await r.fulfill({ json: conversations[1] });
    });
    await navigate(page, `/chat/${encodeURIComponent(conversations[1]!.conversation_id)}`);
    await arrived;
    await navigate(page, "/");
    const answered = page.waitForResponse((r) =>
      r.url().includes(encodeURIComponent(conversations[1]!.conversation_id)),
    );
    release();
    await answered;
    await page.getByRole("heading", { name: "主 Agent", exact: true }).waitFor();
    await page.unroute(route);

    await page.getByRole("button", { name: "更多", exact: true }).click();
    await page.getByRole("menuitem", { name: "执行记录", exact: true }).click();
    await page.getByRole("dialog", { name: "持久处理记录" }).waitFor();
    await page
      .getByRole("dialog", { name: "持久处理记录" })
      .getByRole("button", { name: "关闭" })
      .click();
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth),
      false,
      "page overflow",
    );
    if (process.env.TEND_LAYOUT_FONT) {
      const font = await readFile(process.env.TEND_LAYOUT_FONT);
      await page.addStyleTag({
        content: `@font-face{font-family:FixtureChinese;src:url(data:font/otf;base64,${font.toString("base64")})}body,button,input,select,textarea{font-family:FixtureChinese,sans-serif!important}`,
      });
      await page.evaluate(() => document.fonts.ready);
    }
    await page.emulateMedia({ colorScheme: "dark" });
    await page.screenshot({ path: join(output, `dark-${width}.png`), fullPage: true });
    const originalOwner = owner.associations!;
    const originalReply = reply.associations!;
    owner.associations = {
      ...associations,
      disposition: "pending",
      items: [],
      facts_reviewed: false,
      processing: {
        logical_turn_id: "logical-example",
        revision: 2,
        status: "needs_review",
        reason: "自动补齐未完成，可继续处理。",
      },
    };
    reply.associations = { ...associations, disposition: "unassigned", items: [] };
    await page.reload();
    await page.getByText("关联处理中", { exact: true }).waitFor();
    await page.getByText("无Issue", { exact: true }).waitFor();
    await page.getByRole("button", { name: "继续处理", exact: true }).waitFor();
    await page.screenshot({
      path: join(output, `classification-recovery-${width}.png`),
      fullPage: true,
    });
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth),
      false,
    );
    owner.associations = originalOwner;
    reply.associations = originalReply;
    assert.deepEqual(errors, []);
    await context.close();
  }
  console.log(`Agent UI scenarios passed; screenshots in ${output}`);
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
