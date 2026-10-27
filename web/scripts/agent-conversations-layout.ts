/// <reference lib="dom" />
// Synthetic conversations exercise UI routing; no native harness or deployed API.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import type { ConversationItem, Envelope } from "../src/api/generated/api.ts";
const root = fileURLToPath(new URL("../../internal/webui/dist/app/", import.meta.url));
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-agent-layout");
await mkdir(output, { recursive: true });
const names = ["后台 Agent 与上下文管理", "消息关联标签与历史来源追溯", "跨会话输入与附件隔离验证"];
const associations = {
  message_id: "owner-main",
  revision: 1,
  items: names.map((title, i) => ({
    work_item_id: `work-${i}`,
    title,
    source: "owner",
    owner_locked: true,
  })),
};
const conversations = names.map((title, i) => ({
  conversation_id: `work-item:work-${i}`,
  work_item_id: `work-${i}`,
  title,
  revision: 1,
  model: "",
  effort: "",
  last_message_seq: 10 - i,
  last_message_at: Date.now() - i * 60000,
  preview: "已读取相关消息，正在核对最新约束。",
  status: i === 0 ? "running" : "idle",
}));
const owner: ConversationItem = {
  message_id: "owner-main",
  seq: 1,
  kind: "message",
  author: "owner",
  body: "我们可以进一步扩大计划：主 Agent 里的消息应该有工作项标签，关联可以直接纠正。",
  created_at: new Date().toISOString(),
  state: "replied",
  associations,
};
const reply: ConversationItem = {
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
let dropAssociationReply = false;
let dropLookupReply = false;
let lostRequestID = "";
function json(res: ServerResponse, value: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value));
}
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://fixture.invalid"),
    p = decodeURIComponent(u.pathname),
    id = u.searchParams.get("conversation_id") ?? "main";
  if (p === "/api/v1/session")
    return json(res, { token: "fixture", login: "Example owner", scope: "owner" });
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
  if (p === "/api/v1/repos") return json(res, { items: [], revision: 0 });
  if (p === "/api/v1/tasks") return json(res, { items: [], more: false });
  if (p === "/api/v1/agent-conversations")
    return json(res, {
      enabled: true,
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
          data: '{"tool":"work_item_context_read","result":"已读取最新来源"}',
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
  if (p === "/api/v1/work-items")
    return json(res, {
      items: names.map((title, i) => ({
        work_item_id: `work-${i}`,
        title,
        status: "open",
        revision: 1,
        column: "unplanned",
        project: "",
        intent: "",
        created_at: 1,
        updated_at: 1,
        stages: [],
        tasks: [],
        follow_ons: [],
      })),
    });
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
  if (p === "/api/v1/conversation/settings")
    return json(res, {
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
    });
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
    if (e.operation === "conversation.set_associations") {
      if (e.expected_control_revision !== associations.revision)
        return json(res, {
          operation: {
            request_id: e.request_id,
            status: "failed",
            error: { code: "stale_revision", message: "关联已被修改，请重新检查选择。" },
          },
        });
      associations.revision++;
      const selected = (e.payload as { work_item_ids: string[] }).work_item_ids;
      associations.items = names.flatMap((title, i) =>
        selected.includes(`work-${i}`)
          ? [{ work_item_id: `work-${i}`, title, source: "owner", owner_locked: true }]
          : [],
      );
    }
    if (e.operation === "conversation.set_associations" && dropAssociationReply) {
      dropAssociationReply = false;
      lostRequestID = e.request_id;
      return res.destroy();
    }
    return json(res, { operation: { request_id: e.request_id, status: "succeeded", result: {} } });
  }
  if (p.startsWith("/api/"))
    return json(res, { error: { code: "not_found", message: "Not in fixture" } }, 404);
  try {
    const file = resolve(root, `.${p}${p.endsWith("/") ? "index.html" : ""}`);
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
    associations.items = names.map((title, i) => ({
      work_item_id: `work-${i}`,
      title,
      source: "owner",
      owner_locked: true,
    }));
    const context = await browser.newContext({
      viewport: { width, height: width === 1505 ? 1045 : 900 },
      serviceWorkers: "block",
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
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
    await page.getByText(owner.body!, { exact: true }).waitFor();
    await page.getByRole("textbox", { name: "消息", exact: true }).fill("主会话草稿保留");
    await page.screenshot({ path: join(output, `tags-${width}.png`), fullPage: true });
    await page.getByRole("button", { name: "后台 Agent ▾" }).click();
    await page.getByRole("dialog", { name: "后台 Agent 会话" }).waitFor();
    await page.screenshot({ path: join(output, `drawer-${width}.png`), fullPage: true });
    await page
      .getByRole("dialog", { name: "后台 Agent 会话" })
      .getByRole("button")
      .filter({ hasText: names[0]! })
      .click();
    const input = page.getByPlaceholder(new RegExp(names[0]!));
    await input.waitFor();
    assert((await input.getAttribute("placeholder"))?.includes(names[0]!));
    assert.equal(await input.inputValue(), "");
    await input.fill("后台独立草稿");
    await page.getByRole("button", { name: "上下文用量 42%" }).click();
    await page.getByRole("dialog", { name: "上下文用量", exact: true }).waitFor();
    const box = await page.getByRole("dialog", { name: "上下文用量", exact: true }).boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= width, "ctx popover must fit viewport");
    await page.screenshot({ path: join(output, `background-ctx-${width}.png`), fullPage: true });
    await page
      .getByRole("dialog", { name: "上下文用量", exact: true })
      .getByRole("button", { name: "关闭", exact: true })
      .click();
    await page
      .getByRole("navigation", { name: "Agent 切换" })
      .getByRole("button", { name: "主 Agent", exact: true })
      .click();
    assert.equal(
      await page.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
      "主会话草稿保留",
    );
    await page.getByRole("button", { name: "修改工作项关联", exact: true }).first().click();
    const editor = page.getByRole("dialog", { name: "修改工作项关联", exact: true });
    await editor.getByRole("checkbox").first().waitFor();
    const openedRevision = associations.revision;
    associations.revision++;
    associations.items = associations.items.slice(0, 2);
    for (const stream of streams)
      stream.write(
        `id: 2\nevent: ledger\ndata: ${JSON.stringify({ event_seq: 2, kind: "conversation.associations_changed", object_type: "message", object_id: owner.message_id, created_at: new Date().toISOString() })}\n\n`,
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
    assert(writes.some((e) => e.operation === "conversation.set_associations"));
    // The server commits but both the response and initial receipt lookup are lost.
    await page.getByRole("button", { name: "修改工作项关联", exact: true }).first().click();
    await editor.getByRole("checkbox").first().check();
    dropAssociationReply = true;
    dropLookupReply = true;
    await editor.getByRole("button", { name: "保存关联" }).click();
    await editor.getByRole("button", { name: "核对原请求" }).waitFor();
    const beforeRecovery = writes.length;
    await page.reload();
    dropLookupReply = false;
    await page.getByRole("button", { name: "修改工作项关联", exact: true }).first().click();
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
    const route = `**/api/v1/agent-conversations/${encodeURIComponent(conversations[1]!.conversation_id)}`;
    await page.route(route, async (r) => {
      requested();
      await held;
      await r.fulfill({ json: conversations[1] });
    });
    await page.evaluate(
      (id) => window.dispatchEvent(new CustomEvent("tend:open-agent", { detail: id })),
      conversations[1]!.conversation_id,
    );
    await arrived;
    await page.evaluate(() => window.dispatchEvent(new Event("tend:main-agent")));
    const answered = page.waitForResponse((r) =>
      r.url().includes(encodeURIComponent(conversations[1]!.conversation_id)),
    );
    release();
    await answered;
    await page.getByRole("heading", { name: "主 Agent", exact: true }).waitFor();
    await page.unroute(route);

    await page.getByRole("button", { name: "查看处理记录" }).click();
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
    assert.deepEqual(errors, []);
    await context.close();
  }
  console.log(`Agent UI scenarios passed; screenshots in ${output}`);
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
