/// <reference lib="dom" />
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { extname, join, resolve } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";
import type { Envelope, RespondAttentionPayload } from "../src/api/generated/api.ts";

const output = process.env.TEND_LAYOUT_OUTPUT;
if (output) await mkdir(output, { recursive: true });
const id = "attn_fixture_original";
const question = "是否批准这次交付？";
const options = ["approve", "defer", "end_delivery"];
const open = {
  attention_id: id,
  title: question,
  kind: "decision",
  status: "open",
  revision: 7,
  question_version: 2,
  question_options: options,
  delivery_decision: true,
  responsibility: { recipient_kind: "owner" },
  created_at: 1,
  raised_by: { kind: "agent", ref: "fixture" },
  task_context: {
    question_current: true,
    goal: "验证交付",
    lifecycle: "running",
    control_mode: "managed",
    block_reason: "none",
    has_current_artifact: true,
    current_artifact_accepted: true,
  },
  decision: { decision_id: "dec_fixture", version: 2, status: "open", question, options },
};
function resolved(option: string, text = "") {
  return {
    ...current,
    status: "resolved",
    revision: 8,
    question_version: 3,
    decision: { ...open.decision, version: 3, status: "answered", answer: { option, text } },
  };
}
let current = open;
let staleList = false;
let refuse = false;
let lose = false;
let submissions: Envelope[] = [];
let gate = Promise.resolve();
let release = () => {};
let detailReads = 0;
let eventSeq = 1;
const streams = new Set<ServerResponse>();
function publishAnswer() {
  eventSeq++;
  const event = {
    event_seq: eventSeq,
    object_type: "attention",
    object_id: id,
    kind: "attention.updated",
  };
  for (const stream of streams)
    stream.write(`id: ${eventSeq}\nevent: ledger\ndata: ${JSON.stringify(event)}\n\n`);
}
const root = fixtureRoot();
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  const json = (value: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(value));
  };
  if (path === "/api/v1/session")
    return json({ token: "fixture", scope: "owner", deployment_id: "inbox-receipt" });
  if (path === "/api/v1/events") {
    res.setHeader("Content-Type", "text/event-stream");
    res.write(`event: start\ndata: ${JSON.stringify({ event_seq: eventSeq })}\n\n`);
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (path === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], projects: [], hosts: [], queue: [], decisions: [] });
  if (path === "/api/v1/ops" && req.method === "POST") {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk);
    const e = JSON.parse(Buffer.concat(chunks).toString()) as Envelope;
    submissions.push(e);
    await gate;
    if (lose) {
      res.writeHead(503, { "Content-Type": "application/json" }).end("{}");
      return;
    }
    if (refuse)
      return json({
        operation: {
          request_id: e.request_id,
          status: "failed",
          error: { code: "invalid_request", message: "请修改说明" },
        },
      });
    const answer = e.payload as RespondAttentionPayload;
    assert(
      !current.delivery_decision ||
        !["approve", "end_delivery"].includes(answer.option ?? "") ||
        !answer.text,
    );
    current = resolved(answer.option ?? "", answer.text);
    return json({ operation: { request_id: e.request_id, status: "succeeded", result: current } });
  }
  if (path === `/api/v1/attentions/${id}`) {
    detailReads++;
    return json(current);
  }
  if (path === "/api/v1/inbox") {
    const pending = url.searchParams.get("status") === "open";
    if (pending && current.responsibility.recipient_kind !== "owner")
      return json({ items: [], more: false });
    return json({
      items: staleList
        ? [open]
        : url.searchParams.get("status") === "open" && current.status !== "open"
          ? []
          : [current],
      more: false,
    });
  }
  if (path.startsWith("/api/"))
    return json({ items: [], more: false, repos: [], enabled: false, revision: 0 });
  try {
    const file = resolve(root, extname(path) ? "." + path : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      (
        {
          ".js": "text/javascript",
          ".css": "text/css",
          ".json": "application/json",
        } as Record<string, string>
      )[extname(file)] ?? "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
const browser = await chromium.launch({ headless: true });
try {
  for (const scenario of [
    {
      width: 1024,
      name: "desktop",
      local: "延期",
      actual: "延期决定已记录",
      note: "请两天后再处理",
    },
    { width: 390, name: "phone", local: "批准", actual: "批准已记录", note: "" },
    { width: 390, name: "failure", local: "延期", actual: "延期决定已记录", note: "请下周继续" },
    {
      width: 390,
      name: "conditional-reply",
      local: "仅回复说明",
      actual: "答复已记录",
      note: "通过回归后再批准",
    },
    {
      width: 1024,
      name: "ordinary-agent-routed",
      local: "approve",
      actual: "答复已记录",
      note: "普通决定允许保留说明",
      recipient: "agent",
    },
    {
      width: 390,
      name: "ordinary-main-routed",
      local: "end_delivery",
      actual: "答复已记录",
      note: "原始问题仍开放",
      recipient: "main",
    },
    {
      width: 1024,
      name: "unknown-cross-device",
      local: "批准",
      actual: "结束交付决定已记录",
      note: "",
      crossDevice: true,
    },
  ]) {
    current = scenario.recipient
      ? {
          ...open,
          delivery_decision: false,
          responsibility: { recipient_kind: scenario.recipient },
        }
      : open;
    staleList = false;
    refuse = scenario.name === "failure";
    lose = !!scenario.crossDevice;
    submissions = [];
    gate = new Promise<void>((done) => {
      release = done;
    });
    const phone = scenario.width < 761;
    const context = await browser.newContext({
      viewport: { width: scenario.width, height: 844 },
      isMobile: phone,
      hasTouch: phone,
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      page.setDefaultTimeout(10_000);
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error" && !message.text().includes("503"))
          errors.push(message.text());
      });
      await page.goto(`${origin}/inbox/${id}`);
      assert.equal(await page.title(), "tend");
      const submit = page.getByRole("button", { name: "提交决定", exact: true });
      const explanation = page.getByRole("textbox", { name: /说明/ });
      const radio = page.getByRole("radio", { name: scenario.local, exact: true });
      const badge = page
        .getByRole("navigation", {
          name: phone ? "视图" : "主导航",
          exact: true,
        })
        .getByRole("link", { name: "收件箱", exact: true });
      await badge.waitFor({ state: "visible" });
      const pendingBadge = badge.locator('[data-badge="pending"]');
      if (!scenario.recipient) {
        await pendingBadge.waitFor({ state: "visible" });
        assert.equal(await badge.getAttribute("aria-description"), "1 项待处理");
      }
      await radio.check();
      if (scenario.local === "批准") assert(await explanation.isDisabled());
      if (scenario.note) await explanation.fill(scenario.note);
      await submit.click();
      await page.getByText("正在提交决定…", { exact: true }).waitFor();
      assert(
        (await submit.isDisabled()) &&
          (await explanation.isDisabled()) &&
          (await radio.isDisabled()),
      );
      await submit.evaluate((button: HTMLButtonElement) => button.click());
      assert.equal(submissions.length, 1);
      assert.equal(submissions[0]!.expected_control_revision, 7);
      assert.equal((submissions[0]!.payload as RespondAttentionPayload).version, 2);
      const requestID = submissions[0]!.request_id;
      staleList = true;
      release();
      if (refuse) {
        await page.getByText("提交失败：请修改说明", { exact: true }).waitFor();
        assert((await submit.isEnabled()) && (await radio.isChecked()));
        assert.equal(await explanation.inputValue(), scenario.note);
        if (output) await page.screenshot({ path: join(output, "failure.png") });
        refuse = false;
        await submit.click();
      }
      if (lose) {
        const reconcile = page.getByRole("button", { name: "核实原提交结果", exact: true });
        await reconcile.waitFor();
        assert.equal(await submit.count(), 0);
        if (output) await page.screenshot({ path: join(output, "unknown.png") });
        const before = detailReads;
        await reconcile.click();
        await reconcile.waitFor();
        assert(detailReads > before);
        assert.equal(submissions.length, 1);
        assert.equal(submissions[0]!.request_id, requestID);
        current = resolved("end_delivery", "从另一端记录的说明");
        publishAnswer();
      }
      const receipt = page.getByRole("status").filter({ hasText: scenario.actual });
      await receipt.waitFor();
      await page.waitForFunction(
        (title) =>
          document.activeElement?.getAttribute("role") === "status" &&
          document.activeElement.textContent?.includes(title),
        scenario.actual,
      );
      assert.equal(await submit.count(), 0);
      assert.equal(await explanation.count(), 0);
      assert.equal(await page.getByRole("radio").count(), 0);
      assert.equal(new URL(page.url()).pathname, `/inbox/${id}`);
      await page.getByRole("heading", { name: "当前结果" }).waitFor();
      if (scenario.note) assert((await receipt.textContent())?.includes(scenario.note));
      if (lose) {
        assert((await receipt.textContent())?.includes("从另一端记录的说明"));
        assert((await receipt.textContent())?.includes("本次选择未采用"));
      }
      await badge.waitFor({ state: "visible" });
      await pendingBadge.waitFor({ state: "hidden" });
      assert.equal(await badge.getAttribute("aria-description"), "0 项待处理");
      if (lose) {
        assert.equal(
          await page.getByRole("button", { name: "核实原提交结果", exact: true }).count(),
          0,
        );
        await page.waitForFunction(() =>
          [...document.querySelectorAll("button")].some(
            (b) => b.textContent === "标记已读" && !b.disabled,
          ),
        );
        assert.equal(submissions.length, 1);
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), scenario.width);
      assert.equal(await page.locator("vite-error-overlay").count(), 0);
      if (output) await page.screenshot({ path: join(output, `${scenario.name}-receipt.png`) });
      await page.getByRole("button", { name: "返回列表", exact: true }).click();
      await page.waitForURL(`${origin}/inbox`);
      await page.getByText("暂无待处理事项。已处理事项可在全部记录中查看。").waitFor();
      await page.getByRole("tab", { name: "全部记录" }).click();
      await page.getByRole("button", { name: new RegExp(question.replace("？", "")) }).click();
      await receipt.waitFor();
      assert.equal(submissions.length, scenario.name === "failure" ? 2 : 1);
      assert.deepEqual(errors, []);
      console.log(`PASS ${scenario.name} ${scenario.width}px (${submissions.length} writes)`);
    } finally {
      release();
      await context.close();
    }
  }
} finally {
  await browser.close();
  server.closeAllConnections();
  server.close();
}
