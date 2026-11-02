/// <reference lib="dom" />
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Browser, Locator } from "playwright";
import { navigate } from "./navigation.ts";

// Preserve the visible reference prefix; the image cuts off the original suffix.
export const longChildTitles = [
  "修复 Issue 卡片进度仅统计真实直接子 Issue",
  "Review: 依据绑定调查报告、原案例选定证据及当前实际架构，落实最小机制修复：实现独立验收结果需保留真实交付责任与owner合入确认责任，closed Issue 在合法owner来源下重开并恢复协调，不复活已结束的执行任务，保留原身份、历史与独立验收证据。",
  "ReviewUnbrokenEnglishTitleWithNoWhitespace".repeat(8),
  "Review 混合标题：核对 child Issue、owner 确认与交付证据，".repeat(6),
];

// Read every compared box in one browser task. Expanding the focused show-all
// control can trigger scroll anchoring between separate Playwright round trips.
async function childGeometry(root: Locator, board = false) {
  return root.evaluate((root, board) => {
    const box = (element: Element) => {
      const r = element.getBoundingClientRect();
      return {
        x: r.x,
        y: r.y,
        width: r.width,
        left: r.left,
        top: r.top,
        right: r.right,
        bottom: r.bottom,
        height: r.height,
      };
    };
    const buttons = Array.from(root.querySelectorAll("button"));
    const rows = buttons.filter((button) => button.querySelector(board ? "small" : "span"));
    const more = buttons.find((button) => /查看全部|收起子 Issue/.test(button.textContent ?? ""));
    const next = board
      ? root.ownerDocument.querySelector('[data-issue-id="issue-next-card"]')
      : null;
    return {
      cardBox: box(root),
      nextBox: next ? box(next) : null,
      moreBox: more ? box(more) : null,
      scrolls: Array.from(root.ownerDocument.querySelectorAll("*"))
        .filter((element) => element.scrollTop || element.scrollLeft)
        .map((element) => ({ top: element.scrollTop, left: element.scrollLeft })),
      rows: rows.map((button) => ({
        title:
          button.children[
            button.children.length === 3 && button.firstElementChild?.hasAttribute("aria-hidden")
              ? 1
              : 0
          ]!.textContent,
        button: box(button),
        parts: Array.from(button.children, box),
        height: getComputedStyle(button).height,
        scrollWidth: button.scrollWidth,
        clientWidth: button.clientWidth,
      })),
    };
  }, board);
}

export async function captureChildTitleLayout(browser: Browser, origin: string, output: string) {
  const failures: string[] = [];
  const check = (condition: boolean, message: string) => {
    if (!condition) failures.push(message);
  };
  for (const width of [390, 360, 320, 1440]) {
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error") errors.push(message.text());
      });
      await page.goto(`${origin}/issues`);
      assert.equal(await page.title(), "tend");
      const card = page.locator('[data-issue-id="issue-root"]');
      const region = card.getByRole("region", { name: "子 Issue 进展" });
      await region.getByText("子 Issue · 0/4 已完成", { exact: true }).waitFor();
      const rows = region.locator("button").filter({ has: page.locator("small") });
      const observations = [];
      for (const expanded of [false, true]) {
        if (expanded) {
          await region.getByRole("button", { name: "查看全部 4 个子 Issue ↓" }).click();
          await rows.nth(3).waitFor();
        }
        for (const scrolled of expanded ? [false, true] : [false]) {
          if (scrolled)
            await page.locator('[data-issue-id="issue-next-card"]').scrollIntoViewIfNeeded();
          const snapshot = await childGeometry(card, true);
          const { rows: geometry, cardBox } = snapshot;
          assert(snapshot.nextBox && snapshot.moreBox, "next card and show-all must exist");
          const { nextBox, moreBox } = snapshot;
          observations.push({ expanded, scrolled, ...snapshot });
          assert.deepEqual(
            geometry.map((row) => row.title),
            expanded ? longChildTitles : longChildTitles.slice(0, 2),
          );
          for (const [index, row] of geometry.entries()) {
            const label = `${width}/${expanded ? "expanded" : "collapsed"}/${scrolled ? "scrolled/" : ""}${index}`;
            for (const part of row.parts)
              check(
                part.left >= row.button.left - 1 &&
                  part.right <= row.button.right + 1 &&
                  part.top >= row.button.top - 1 &&
                  part.bottom <= row.button.bottom + 1,
                `${label}: title/status outside child button`,
              );
            check(row.button.bottom <= cardBox.y + cardBox.height, `${label}: child outside card`);
            check(
              row.button.left >= cardBox.left &&
                row.button.right <= cardBox.right &&
                row.button.top >= cardBox.top,
              `${label}: child outside card edges`,
            );
            check(row.scrollWidth <= row.clientWidth + 1, `${label}: child horizontal overflow`);
            if (index > 0)
              check(geometry[index - 1]!.button.bottom <= row.button.top, `${label}: rows overlap`);
          }
          check(geometry.at(-1)!.button.bottom <= moreBox.y, `${width}: child overlaps show-all`);
          check(
            moreBox.y + moreBox.height <= cardBox.y + cardBox.height,
            `${width}: show-all outside card`,
          );
          check(
            moreBox.left >= cardBox.left &&
              moreBox.right <= cardBox.right &&
              moreBox.top >= cardBox.top,
            `${width}: show-all outside card edges`,
          );
          check(cardBox.y + cardBox.height <= nextBox.y, `${width}: cards overlap`);
          check(
            await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
            `${width}: page horizontal overflow`,
          );
          await page.screenshot({
            path: join(
              output,
              `child-title-${width}-${expanded ? "expanded" : "collapsed"}${scrolled ? "-bottom" : ""}.png`,
            ),
            fullPage: true,
          });
        }
      }

      assert.deepEqual(await rows.locator("span:nth-child(2)").allTextContents(), longChildTitles);
      // Refresh must keep the same direct-child collection and count.
      await page.reload();
      await region.getByText("子 Issue · 0/4 已完成", { exact: true }).waitFor();
      await region.getByRole("button", { name: "查看全部 4 个子 Issue ↓" }).click();
      await rows.nth(3).waitFor();
      assert.deepEqual(await rows.locator("span:nth-child(2)").allTextContents(), longChildTitles);
      // The short row remains clickable even on the pre-fix evidence run.
      await rows.first().click();
      await page.waitForURL(`${origin}/issues/issue-long-0`);
      await page
        .getByRole("heading", { name: longChildTitles[0]!, exact: true, level: 1 })
        .waitFor();
      await navigate(page, "/issues/issue-root");
      const details = page.getByRole("region", { name: "Issue 详情", exact: true });
      const children = details
        .locator("section")
        .filter({ has: page.getByRole("heading", { name: "子 Issue", exact: true }) });
      const detailRows = children.locator("button").filter({ has: page.locator("span") });
      await detailRows.nth(3).waitFor();
      assert.deepEqual(
        await detailRows.locator(":scope > span:first-child").allTextContents(),
        longChildTitles,
      );
      const { rows: detailGeometry } = await childGeometry(children);
      for (const row of detailGeometry)
        for (const part of row.parts)
          check(
            part.left >= row.button.left - 1 &&
              part.right <= row.button.right + 1 &&
              part.top >= row.button.top - 1 &&
              part.bottom <= row.button.bottom + 1,
            `${width}: detail child overflow`,
          );
      await children.scrollIntoViewIfNeeded();
      await page.screenshot({
        path: join(output, `child-title-${width}-details.png`),
        fullPage: true,
      });
      const interactionPaths = [new URL(page.url()).pathname];
      // Once geometry is valid, click the formerly overflowing long rows, including
      // the last line (a fixed-height row cannot expose this hit target).
      if (failures.length === 0) {
        await detailRows.nth(3).click();
        await page.waitForURL(`${origin}/issues/issue-long-3`);
        interactionPaths.push(new URL(page.url()).pathname);
        for (const index of [1, 2, 3]) {
          await navigate(page, "/issues");
          await region.getByRole("button", { name: "查看全部 4 个子 Issue ↓" }).click();
          await rows.nth(3).waitFor();
          const title = rows.nth(index).locator("span:nth-child(2)");
          await title.scrollIntoViewIfNeeded();
          const titleBox = (await title.boundingBox())!;
          await title.click({ position: { x: 8, y: titleBox.height - 4 } });
          await page.waitForURL(`${origin}/issues/issue-long-${index}`);
          await page
            .getByRole("heading", { name: longChildTitles[index]!, exact: true, level: 1 })
            .waitFor();
          interactionPaths.push(new URL(page.url()).pathname);
        }
      }
      await writeFile(
        join(output, `child-title-${width}.json`),
        JSON.stringify({ observations, detailGeometry, interactionPaths, errors }, null, 2) + "\n",
      );
      assert.deepEqual(errors, []);
      console.log(`MEASURED child title layout ${width}`);
    } finally {
      await context.close();
    }
  }
  assert.deepEqual(failures, [], failures.join("\n"));
  console.log("PASS long child titles, card geometry, direct-child sets, refresh and clicks");
}
