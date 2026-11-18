/// <reference lib="dom" />
import assert from "node:assert/strict";
import { join } from "node:path";
import type { Browser } from "playwright";
import type {
  Envelope,
  IssuePage,
  IssueSummary,
  IssueWorkStatus,
} from "../src/api/generated/api.ts";
import { issueBoardFixture, issuePage } from "./issue-fixture.ts";

export async function checkIssueOwnerActionLayout(
  browser: Browser,
  origin: string,
  output: string,
  publish: () => void,
) {
  const sample = issuePage().items![0]!;
  const card = (id: string, title: string, work_status: IssueWorkStatus): IssueSummary => ({
    ...sample,
    issue_id: id,
    title,
    work_status,
  });
  const running = card("issue-owner-action-running", "等你补充结果", "in_progress");
  const review = card("issue-owner-action-review", "等你核对变更", "in_review");
  const todo = card("issue-owner-action-todo", "等你提供范围", "todo");
  const system = card("issue-system-queue", "队列等待 Worker", "in_progress");
  const all = [running, review, todo, system];
  for (const width of [1440, 320]) {
    const mobile = width === 320;
    const context = await browser.newContext({
      viewport: { width, height: 880 },
      isMobile: mobile,
      hasTouch: mobile,
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      const pageErrors: string[] = [];
      const consoleErrors: string[] = [];
      const requests: URL[] = [];
      const writes: Envelope[] = [];
      let matches = [running, review, todo];
      let facts = 1;
      let allowLoad!: () => void;
      const hold = new Promise<void>((resolve) => (allowLoad = resolve));
      let activeAvailable = !mobile;
      page.on("pageerror", (error) => pageErrors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error" && !message.text().includes("503"))
          consoleErrors.push(message.text());
      });
      await page.route("**/api/v1/issues?**", async (route) => {
        const url = new URL(route.request().url());
        if (url.searchParams.get("board") !== "true") return route.fallback();
        requests.push(url);
        const filtered = url.searchParams.get("waiting_on_me") === "true";
        if (filtered) await hold;
        if (
          filtered &&
          url.searchParams.get("board_status") === "in_progress" &&
          !activeAvailable
        ) {
          return route.fulfill({
            status: 503,
            json: { error: { message: "Fixture interruption" } },
          });
        }
        const value = issueBoardFixture(url.href, {
          items: filtered ? matches : all,
          more: false,
        }) as IssuePage;
        await route.fulfill({ json: { ...value, facts_seq: facts } });
      });
      await page.route("**/api/v1/ops", async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        const envelope = route.request().postDataJSON() as Envelope;
        writes.push(envelope);
        await route.fulfill({
          json: {
            operation: {
              operation: envelope.operation,
              request_id: envelope.request_id,
              status: "succeeded",
              result: {},
            },
          },
        });
      });
      await page.goto(origin + "/issues");
      await page.waitForURL(origin + "/issues");
      assert.match(await page.title(), /Tend/i);
      const board = page.getByRole("region", { name: "Issue 白板" });
      await board.getByRole("heading", { name: "所有 Issue" }).waitFor();
      const tabs = page.getByRole("tablist", { name: "工作状态" });
      const activeColumn = board.getByRole("region", { name: "进行中列" });
      const todoColumn = board.getByRole("region", { name: "待推进列" });
      const runningCard = activeColumn.getByRole("button", { name: running.title, exact: true });
      const reviewCard = activeColumn.getByRole("button", { name: review.title, exact: true });
      const unmatched = activeColumn.getByRole("button", { name: system.title, exact: true });
      const toggle = board.getByRole("checkbox", { name: "等我处理" });
      await unmatched.waitFor();
      await runningCard.waitFor();
      await reviewCard.waitFor();
      assert.equal(await reviewCard.count(), 1);
      assert.equal(await board.getByRole("region", { name: /^(待检查|审阅中)列$/ }).count(), 0);
      await toggle.focus();
      await page.keyboard.press("Space");
      assert(await toggle.isChecked());
      await activeColumn.getByText("正在读取…", { exact: true }).waitFor();
      await unmatched.waitFor({ state: "hidden" });
      allowLoad();
      if (mobile) {
        await activeColumn.getByText("读取失败，保留上次记录。").waitFor();
        activeAvailable = true;
        await activeColumn.getByRole("button", { name: "重新读取" }).tap();
      }
      await runningCard.waitFor();
      await reviewCard.waitFor();
      assert(requests.some((url) => url.searchParams.get("waiting_on_me") === "true"));
      assert(requests.every((url) => url.searchParams.get("group_review") === "true"));
      assert(requests.every((url) => url.searchParams.get("board_status") !== "in_review"));
      assert.equal(await reviewCard.count(), 1);
      await page.screenshot({ path: join(output, `board-owner-action-${width}.png`) });

      if (mobile) {
        assert.equal(await tabs.getByRole("tab", { name: /^(待检查|审阅中)/ }).count(), 0);
        const activeTab = tabs.getByRole("tab", { name: /^进行中/ });
        await activeTab.focus();
        await page.keyboard.press("ArrowRight");
        assert.equal(
          await tabs.getByRole("tab", { name: /^已完成/ }).getAttribute("aria-selected"),
          "true",
        );
        await tabs.getByRole("tab", { name: /^待推进/ }).tap();
        await todoColumn.getByRole("button", { name: todo.title, exact: true }).waitFor();
        const firstTab = tabs.getByRole("tab", { name: /^待安排/ });
        await firstTab.tap();
        const scroller = tabs.locator("..");
        const before = await scroller.evaluate((el) => el.scrollLeft);
        const box = await scroller.boundingBox();
        assert(box && (await scroller.evaluate((el) => el.scrollWidth > el.clientWidth)));
        const cdp = await context.newCDPSession(page);
        const x = Math.floor(box.x + box.width * 0.8);
        const y = Math.floor(box.y + box.height / 2);
        await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
        for (const distance of [35, 80, 130, 185]) {
          await cdp.send("Input.dispatchTouchEvent", {
            type: "touchMove",
            touchPoints: [{ x: x - distance, y }],
          });
        }
        await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
        await page.waitForFunction(
          (previous) =>
            document.querySelector('[aria-label="工作状态"]')!.parentElement!.scrollLeft > previous,
          before,
        );
        await activeTab.tap();
        await reviewCard.waitFor();
      } else {
        await todoColumn.getByRole("button", { name: todo.title, exact: true }).waitFor();
        const reviewArticle = board.locator(`[data-issue-id="${review.issue_id}"]`);
        await board.locator(`[data-issue-id="${review.issue_id}"][draggable="true"]`).waitFor();
        const transfer = await page.evaluateHandle(() => new DataTransfer());
        const drop = async (target: typeof activeColumn) => {
          await reviewArticle.dispatchEvent("dragstart", { dataTransfer: transfer });
          await target.dispatchEvent("dragover", { dataTransfer: transfer });
          await target.dispatchEvent("drop", { dataTransfer: transfer });
          await reviewArticle.dispatchEvent("dragend", { dataTransfer: transfer });
        };
        await drop(activeColumn);
        await reviewCard.waitFor();
        assert.equal(writes.length, 0, "same-column drop must preserve in_review");
        const request = page.waitForRequest(
          (req) => req.url().endsWith("/api/v1/ops") && req.method() === "POST",
        );
        await drop(board.getByRole("region", { name: "待安排列" }));
        const submitted = await request;
        await submitted.response();
        const envelope = submitted.postDataJSON();
        assert.equal(envelope.operation, "issues.set_status");
        assert.equal(envelope.target.id, review.issue_id);
        assert.equal(envelope.payload.status, "backlog");
        assert.equal(writes.length, 1, "only the cross-column drop may write");
      }

      matches = [];
      facts++;
      publish();
      await board.getByRole("status").getByText("暂无等你处理的 Issue。").waitFor();
      await runningCard.waitFor({ state: "hidden" });
      await page.screenshot({ path: join(output, `board-owner-action-empty-${width}.png`) });
      if (mobile) await toggle.tap();
      else await toggle.uncheck();
      await page.waitForURL(origin + "/issues");
      await unmatched.waitFor();
      assert(!(await toggle.isChecked()));
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
      assert.deepEqual(pageErrors, []);
      assert.deepEqual(consoleErrors, []);
      console.log(`PASS grouped Issue board waiting on me at ${width}px`);
    } finally {
      await context.close();
    }
  }
}
