/// <reference lib="dom" />
import assert from "node:assert/strict";
import { join } from "node:path";
import type { Browser } from "playwright";
import type { IssuePage, IssueSummary, IssueWorkStatus } from "../src/api/generated/api.ts";
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
  const system = card("issue-system-queue", "队列等待 Worker", "in_progress");
  const all = [running, review, system];
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
      let matches = [running, review];
      let facts = 1;
      let allowLoad!: () => void;
      const hold = new Promise<void>((resolve) => (allowLoad = resolve));
      let reviewAvailable = !mobile;
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
        if (filtered && url.searchParams.get("board_status") === "in_review" && !reviewAvailable) {
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
      await page.goto(origin + "/issues");
      await page.waitForURL(origin + "/issues");
      assert.match(await page.title(), /Tend/i);
      const board = page.getByRole("region", { name: "Issue 白板" });
      await board.getByRole("heading", { name: "所有 Issue" }).waitFor();
      const tabs = page.getByRole("tablist", { name: "工作状态" });
      const runningColumn = board.getByRole("region", { name: "进行中列" });
      const runningCard = runningColumn.getByRole("button", { name: running.title, exact: true });
      const unmatched = runningColumn.getByRole("button", { name: system.title, exact: true });
      const toggle = board.getByRole("checkbox", { name: "等我处理" });
      await unmatched.waitFor();
      await toggle.focus();
      await page.keyboard.press("Space");
      assert(await toggle.isChecked());
      await runningColumn.getByText("正在读取…", { exact: true }).waitFor();
      await unmatched.waitFor({ state: "hidden" });
      allowLoad();
      await runningCard.waitFor();
      assert(requests.some((url) => url.searchParams.get("waiting_on_me") === "true"));
      assert.equal(await runningCard.count(), 1);
      await page.screenshot({ path: join(output, `board-owner-action-${width}.png`) });

      if (mobile) {
        const reviewTab = tabs.getByRole("tab", { name: /^审阅中/ });
        await tabs.getByRole("tab", { name: /^进行中/ }).focus();
        await page.keyboard.press("ArrowRight");
        assert.equal(await reviewTab.getAttribute("aria-selected"), "true");
        const reviewColumn = board.getByRole("region", { name: "审阅中列" });
        await reviewColumn.getByText("读取失败，保留上次记录。").waitFor();
        reviewAvailable = true;
        await reviewColumn.getByRole("button", { name: "重新读取" }).tap();
        await reviewColumn.getByRole("button", { name: review.title, exact: true }).waitFor();

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
        await scroller.evaluate((el, previous) => {
          if (el.scrollLeft > previous) return;
          return new Promise<void>((resolve, reject) => {
            const timeout = setTimeout(() => {
              el.removeEventListener("scroll", check);
              reject(new Error("Touch swipe did not scroll the mobile status tabs"));
            }, 2000);
            function check() {
              if (el.scrollLeft <= previous) return;
              clearTimeout(timeout);
              el.removeEventListener("scroll", check);
              resolve();
            }
            el.addEventListener("scroll", check);
          });
        }, before);
        await reviewTab.tap();
        await page.screenshot({ path: join(output, "board-owner-action-mobile-review.png") });
        await tabs.getByRole("tab", { name: /^进行中/ }).tap();
      } else {
        await board
          .getByRole("region", { name: "审阅中列" })
          .getByRole("button", { name: review.title, exact: true })
          .waitFor();
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
      console.log(`PASS Issue waiting on me at ${width}px`);
    } finally {
      await context.close();
    }
  }
}
