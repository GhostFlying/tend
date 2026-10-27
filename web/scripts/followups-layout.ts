/// <reference lib="dom" />
// Synthetic owner API only; browser coverage never writes to a deployed ledger.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Page } from "playwright";
import type {
  Envelope,
  FollowupRule,
  FollowupSpec,
  WorkItemView,
} from "../src/api/generated/api.ts";
const root = fileURLToPath(new URL("../../internal/webui/dist/app/", import.meta.url));
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-followups-layout");
await mkdir(output, { recursive: true });
const item: WorkItemView = {
  work_item_id: "work-example",
  project: "",
  title: "等待交付并按约定继续",
  intent: "核对已授权工作的结果",
  revision: 1,
  status: "open",
  column: "unplanned",
  created_at: 1,
  updated_at: 1,
  stages: [],
  tasks: [],
  follow_ons: [],
};
let rules: FollowupRule[] = [];
let seq = 1;
let drop = false;
let sourceCancelled = false;
const writes: Envelope[] = [];
const streams = new Set<ServerResponse>();
const receipts = new Map<string, unknown>();
function json(res: ServerResponse, value: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(value));
}
function publish() {
  item.followups = {
    active: rules.filter((r) => r.status === "active").length,
    paused: rules.filter((r) => r.status === "paused").length,
    purpose: rules[0]?.purpose ?? "",
    next_at: rules[0]?.next_at ?? 0,
  };
  seq++;
  for (const res of streams)
    res.write(
      `event: ledger\ndata: ${JSON.stringify({ event_seq: seq, kind: "followup.changed", object_type: "followup", object_id: rules[0]?.rule_id, created_at: "2026-10-26T00:00:00Z" })}\n\n`,
    );
}
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://fixture.invalid"),
    path = u.pathname;
  if (path === "/api/v1/session")
    return json(res, {
      token: "fixture",
      login: "Example owner",
      scope: "owner",
      deployment_id: "layout-fixture",
    });
  if (path === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`event: start\ndata: {"event_seq":${seq}}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/snapshot")
    return json(res, {
      event_seq: seq,
      tasks: [],
      decisions: [],
      hosts: [],
      queue: [],
      projects: [],
    });
  if (path === "/api/v1/work-items") return json(res, { items: [item] });
  if (path === `/api/v1/work-items/${item.work_item_id}/followups`)
    return json(res, { enabled: true, items: rules, more: false, event_seq: seq });
  if (path === `/api/v1/work-items/${item.work_item_id}`) return json(res, item);
  if (path.startsWith("/api/v1/followups/"))
    return json(res, {
      rule: rules[0],
      revisions: rules,
      occurrences: [1, 2, 3].map((i) => ({
        occurrence_id: `occ-${i}`,
        rule_id: rules[0]!.rule_id,
        revision: 1,
        work_item_id: item.work_item_id,
        message_id: `source-${i}`,
        background_message_id: `batch-${i}`,
        reason: "every",
        status: i === 3 ? (sourceCancelled ? "cancelled" : "queued") : "completed",
        evidence: "没有新的交付变化",
        created_at: Date.parse("2026-10-26T01:00:00Z") + i * 3600000,
        checkin: i === 3 ? undefined : { note: "交付仍未合入，继续等待", created_at: 1 },
      })),
      more: false,
    });
  if (path.startsWith("/api/v1/background/"))
    return json(res, {
      batch: {
        batch_id: "batch",
        message_id: path.split("/")[4],
        status: path.includes("batch-3")
          ? sourceCancelled
            ? "cancelled"
            : "pending"
          : "completed",
        source_count: sourceCancelled ? 0 : 1,
        created_at: 1,
      },
      sources: [
        {
          seq: 1,
          message_id: "source",
          event_id: "event",
          kind: "followup.triggered",
          cancelled_at: sourceCancelled && path.includes("batch-3") ? 1 : undefined,
          body: "核对现有交付状态；保留 owner 的原始约束。",
        },
      ],
      more: false,
    });
  if (path === "/api/v1/conversation") return json(res, { items: [], more: false });
  if (path === "/api/v1/conversation/queue")
    return json(res, {
      enabled: true,
      category: u.searchParams.get("category") ?? "owner",
      items: [],
      count: 0,
      more: false,
      event_seq: seq,
    });
  if (path === "/api/v1/conversation/transcript")
    return json(res, { enabled: true, blocks: [], more: false, event_seq: seq });
  if (path === "/api/v1/conversation/settings" || path === "/api/v1/conversation/handoff")
    return json(res, { enabled: false, revision: 1 });
  if (path.startsWith("/api/v1/requests/")) {
    const result = receipts.get(path.split("/").at(-1)!);
    return result
      ? json(res, result)
      : json(res, { error: { code: "not_found", message: "No receipt yet" } }, 404);
  }
  if (path === "/api/v1/ops" && req.method === "POST") {
    let raw = "";
    for await (const chunk of req) raw += String(chunk);
    const e = JSON.parse(raw) as Envelope;
    writes.push(e);
    if (drop) {
      res.destroy();
      return;
    }
    if (!receipts.has(e.request_id)) {
      const verb = e.operation.split("_").at(-1);
      if (verb === "create") {
        const spec = e.payload as FollowupSpec;
        rules.push({
          ...spec,
          rule_id: `followup-${rules.length + 1}`,
          work_item_id: item.work_item_id,
          status: "active",
          revision: 1,
          trigger_count: 0,
          next_at: spec.at ?? Date.now() + 3600000,
          created_at: 1,
          updated_at: 1,
        });
      } else {
        const rule = rules.find((r) => r.rule_id === e.target.id)!;
        assert.equal(e.expected_control_revision, rule.revision);
        rule.revision++;
        if (verb === "update") Object.assign(rule, (e.payload as { spec: FollowupSpec }).spec);
        if (verb === "pause") rule.status = "paused";
        if (verb === "resume") rule.status = "active";
        if (verb === "cancel") rule.status = "cancelled";
        if (verb === "trigger") rule.trigger_count++;
      }
      receipts.set(e.request_id, { request_id: e.request_id, status: "succeeded", result: {} });
      publish();
    }
    return json(res, { operation: receipts.get(e.request_id) });
  }
  if (path.startsWith("/api/"))
    return json(res, { error: { code: "not_found", message: "Not in fixture" } }, 404);
  try {
    const file = resolve(root, `.${path}${path.endsWith("/") ? "index.html" : ""}`);
    assert(file.startsWith(root));
    res.setHeader(
      "Content-Type",
      path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html",
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
  for (const width of [390, 1280]) {
    rules = [];
    sourceCancelled = false;
    writes.length = 0;
    receipts.clear();
    drop = false;
    const context = await browser.newContext({
      viewport: { width, height: 900 },
      serviceWorkers: "block",
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.setDefaultTimeout(10000);
    const open = async (p: Page) => {
      await p.goto(`http://127.0.0.1:${address.port}`);
      if (process.env.TEND_LAYOUT_FONT) {
        const font = await readFile(process.env.TEND_LAYOUT_FONT);
        await p.addStyleTag({
          content: `@font-face{font-family:FixtureChinese;src:url(data:font/otf;base64,${font.toString("base64")})}body,button,input,select,textarea{font-family:FixtureChinese,sans-serif!important}`,
        });
        await p.evaluate(() => document.fonts.ready);
      }
      if (width < 760)
        await p
          .getByRole("navigation", { name: "视图" })
          .getByRole("button", { name: "工作项", exact: true })
          .click();
      else await p.getByRole("button", { name: "工作项 · 白板 ›" }).click();
      await p
        .getByRole("region", { name: "工作项白板", exact: true })
        .getByRole("button", { name: item.title, exact: true })
        .click();
      await p
        .getByRole("dialog", { name: "工作项详情", exact: true })
        .getByRole("heading", { name: item.title, exact: true })
        .waitFor();
      await p.getByRole("button", { name: "跟进与交付", exact: true }).click();
    };
    await open(page);
    await page.getByRole("button", { name: "跟进与交付", exact: true }).click();
    const section = page.getByRole("region", { name: "跟进安排" });
    await section.getByRole("button", { name: "新增安排" }).click();
    const form = page.getByRole("form", { name: "新增跟进安排" });
    await form.getByLabel("目的", { exact: true }).fill("明天核对交付");
    await form.getByLabel("检查内容").fill("核对已有交付，继续已授权事项");
    await form.getByLabel("IANA 时区").fill("Asia/Shanghai");
    await form.getByLabel("核对时间").fill("2026-10-27T09:30");
    await form.getByRole("button", { name: "保存安排" }).click();
    await section.getByText("明天核对交付", { exact: true }).waitFor();
    assert.equal((writes[0]!.payload as FollowupSpec).at, Date.parse("2026-10-27T01:30:00Z"));
    await section.getByRole("button", { name: "暂停", exact: true }).click();
    await section.getByText("已暂停", { exact: true }).waitFor();
    await section.getByRole("button", { name: "恢复", exact: true }).click();
    await section.getByText("等待触发", { exact: true }).waitFor();
    await section.getByRole("button", { name: "编辑", exact: true }).click();
    const edit = page.getByRole("form", { name: "编辑跟进安排" });
    await edit.getByLabel("触发方式").selectOption("every");
    await edit.getByLabel("固定间隔（分钟，非当地每日时间）").fill("60");
    await edit.getByRole("button", { name: "保存安排" }).click();
    await edit.waitFor({ state: "hidden" });
    await section.getByRole("button", { name: "历史", exact: true }).click();
    await section.getByText(/连续 2 次/).waitFor();
    await section.getByText("证据与来源", { exact: true }).first().click();
    assert.equal(await section.getByRole("button", { name: "读取批次来源" }).count(), 2);
    await section.getByText("证据与来源", { exact: true }).last().click();
    assert.equal(await section.getByRole("button", { name: "读取批次来源" }).count(), 3);
    await section.getByRole("button", { name: "读取批次来源" }).last().click();
    await section.getByText("核对现有交付状态；保留 owner 的原始约束。", { exact: true }).waitFor();
    sourceCancelled = true;
    publish();
    await section.getByText(/已撤销来源，仅作历史参考/).waitFor();
    const other = await context.newPage();
    await open(other);
    drop = true;
    await section.getByRole("button", { name: "现在检查", exact: true }).click();
    await section.getByRole("button", { name: "核对结果" }).waitFor();
    const unknown = writes.at(-1)!;
    await open(page);
    await page.getByRole("button", { name: "跟进与交付", exact: true }).click();
    await page.getByRole("button", { name: "核对结果" }).waitFor();
    assert.equal(writes.at(-1)!.request_id, unknown.request_id);
    drop = false;
    await page.getByRole("button", { name: "重试原请求" }).click();
    await page.getByRole("button", { name: "核对结果" }).waitFor({ state: "hidden" });
    assert.equal(writes.at(-1)!.request_id, unknown.request_id);
    await other.getByText("已触发 1 / 20", { exact: false }).waitFor();
    const panel = page.getByRole("dialog", { name: "工作项详情", exact: true });
    await panel.getByRole("heading", { name: "跟进安排" }).scrollIntoViewIfNeeded();
    assert(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: join(output, `followups-${width}.png`) });
    assert.deepEqual(errors, []);
    console.log(
      `followups ${width}px: create, timezone, edit, pause/resume, folded history, live source cancellation, reload and second-tab reconciliation passed`,
    );
    await context.close();
  }
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  await new Promise<void>((done) => server.close(() => done()));
}
