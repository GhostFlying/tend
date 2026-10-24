/// <reference lib="dom" />
import { workItemPage } from "./work-items-fixture.ts";
// Real Chromium regression against the actual app and a generic owner API fixture.
// Output is optional and must live outside the repository.
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import type { ServerResponse } from "node:http";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, type Locator, type Page } from "playwright";
import type { HostView, RepoRegistry, Snapshot, TaskSummary } from "../src/api/generated/api.ts";

const output = process.env.TEND_LAYOUT_OUTPUT;
if (output) await mkdir(output, { recursive: true });
const registrations: RepoRegistry = {
  repos: [
    { repo: "sample-app", hosts: ["host-a", "host-b"] },
    { repo: "legacy-library", retired: true, hosts: ["host-a"] },
    { repo: "unserved-library", hosts: [] },
    {
      repo: "shared-library-with-a-very-long-logical-name-that-must-wrap-completely-on-mobile-without-truncation",
      hosts: ["host-b"],
    },
    ...Array.from({ length: 26 }, (_, i) => ({
      repo: `sample-library-${String(i + 1).padStart(2, "0")}`,
      hosts: ["host-a"],
    })),
  ],
};
const hosts: HostView[] = Array.from({ length: 12 }, (_, i) => ({
  host_id: `host-${String.fromCharCode(97 + i)}`,
  os: "linux",
  arch: "amd64",
  labels: [],
  slots: 2,
  worker: "appserver",
  open_runs: 0,
  online: i !== 1,
}));
const snapshot: Snapshot = {
  event_seq: 0,
  tasks: [],
  decisions: [],
  hosts,
  projects: [],
  queue: [],
};
function task(lifecycle: TaskSummary["lifecycle"], title: string, project: string): TaskSummary {
  return {
    task_id: `task_${title.replaceAll(" ", "_")}`,
    title,
    project,
    lifecycle,
    control_mode: "managed",
    control_revision: 1,
    spec_version: 1,
    rating: "p2",
    block_reason: "none",
    workspace: "sample",
    next_implement_generation: 1,
  };
}
let registry = registrations;
let fail = false;
let pause = false;
let reads = 0;
let partial = false;
const partialBodies: ServerResponse[] = [];
const held: ServerResponse[] = [];
const streams = new Set<ServerResponse>();
function json(res: ServerResponse, body: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
}
const server = await createServer({
  configFile: false,
  server: {
    host: "127.0.0.1",
    port: 0,
    strictPort: false,
    watch: { ignored: ["**/*"] },
  },
  plugins: [
    react(),
    {
      name: "registry-fixture",
      configureServer(vite) {
        vite.middlewares.use((req, res, next) => {
          const url = new URL(req.url ?? "/", "http://fixture.invalid");
          const path = url.pathname;
          if (!path.startsWith("/api/")) return next();
          if (path === "/api/v1/events") {
            res.writeHead(200, {
              "Content-Type": "text/event-stream",
              "Cache-Control": "no-store",
            });
            res.write('event: start\ndata: {"event_seq":0}\n\n');
            streams.add(res);
            const timer = setInterval(() => res.write(": heartbeat\n\n"), 1000);
            res.on("close", () => {
              clearInterval(timer);
              streams.delete(res);
            });
            return;
          }
          if (path === "/api/v1/repos") {
            reads++;
            if (partial) {
              partial = false;
              res.writeHead(200, { "Content-Type": "application/json" });
              res.write('{"repos":');
              partialBodies.push(res);
              return;
            }
            if (pause) {
              held.push(res);
              return;
            }
            return json(
              res,
              fail ? { error: { code: "internal", message: "fixture read failed" } } : registry,
              fail ? 500 : 200,
            );
          }
          if (path === "/api/v1/work-items")
            return json(
              res,
              workItemPage(url.searchParams.get("project") || "sample-project", [
                task("completed", "Completed sample 1", "sample-project"),
              ]),
            );
          if (path.startsWith("/api/v1/tasks/")) {
            const id = decodeURIComponent(path.split("/").at(-1)!);
            const lifecycle = id.includes("cancelled") ? "cancelled" : "completed";
            return json(res, {
              ...task(lifecycle, `Unlinked historical ${lifecycle} page 2`, "sample-project"),
              task_id: id,
              runs: [],
              artifacts: [],
              acceptances: [],
              decisions: [],
            });
          }
          if (path === "/api/v1/tasks") {
            const lifecycle =
              (url.searchParams.get("lifecycle")?.split(",").at(-1) as TaskSummary["lifecycle"]) ??
              "queued";
            const project = url.searchParams.get("project") || "sample-project";
            return json(res, {
              tasks: url.searchParams.has("before")
                ? [task(lifecycle, `Unlinked historical ${lifecycle} page 2`, project)]
                : lifecycle === "completed"
                  ? Array.from({ length: 5 }, (_, i) =>
                      task("completed", `Completed sample ${i + 1}`, project),
                    )
                  : [task(lifecycle, `Sample ${lifecycle} task`, project)],
              next:
                !url.searchParams.has("before") && ["completed", "cancelled"].includes(lifecycle)
                  ? `page-2-${lifecycle}`
                  : "",
            });
          }
          const views: Record<string, unknown> = {
            "/api/v1/session": {
              login: "owner@example.com",
              scope: "owner",
              client_id: "sample-browser",
              token: "fixture-token",
              expires_at: "2099-01-01T00:00:00Z",
            },
            "/api/v1/snapshot": snapshot,
            "/api/v1/tasks": { tasks: [], next: "" },
            "/api/v1/conversation": { items: [], more: false },
            "/api/v1/conversation/settings": { enabled: false },
            "/api/v1/conversation/handoff": { enabled: false, idle_minutes: 50 },
          };
          json(res, views[path] ?? {}, views[path] ? 200 : 404);
        });
      },
    },
  ],
});
await server.listen();
const address = server.httpServer!.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch({ headless: true });
console.log(`Browser: ${browser.version()}`);

async function usable(locator: Locator, page: Page) {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  const viewport = page.viewportSize()!;
  assert(box && box.width > 0 && box.height > 0);
  assert(box.x >= 0 && box.x + box.width <= viewport.width + 1);
  assert(box.y >= 0 && box.y + box.height <= viewport.height + 1);
  assert(
    await locator.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const target = globalThis.document.elementFromPoint(
        rect.x + rect.width / 2,
        rect.y + rect.height / 2,
      );
      return target === element || (target !== null && element.contains(target));
    }),
    "control must be reachable without clipping or overlap",
  );
}
async function closed(page: Page, entry: Locator) {
  await page
    .getByRole("dialog", { name: "已注册仓库", exact: true })
    .waitFor({ state: "detached" });
  assert(
    await entry.evaluate((element) => globalThis.document.activeElement === element),
    "return focus to entry",
  );
}

try {
  for (const viewport of [
    { width: 390, height: 844 },
    { width: 430, height: 932 },
    { width: 1440, height: 1000 },
    { width: 390, height: 640 },
  ]) {
    registry = registrations;
    fail = false;
    pause = false;
    const context = await browser.newContext({ viewport, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors: string[] = [];
    page.on("pageerror", (err) => errors.push(err.message));
    await page.goto(`http://127.0.0.1:${address.port}`);
    const mobile = viewport.width < 761;
    if (mobile)
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("button", { name: "工作项", exact: true })
        .click();
    const entry = page
      .locator(mobile ? "main" : "aside")
      .locator('button[aria-haspopup="dialog"]')
      .filter({ hasText: "已注册仓库", visible: true });
    await page.waitForFunction(() =>
      [...globalThis.document.querySelectorAll("button")].some(
        (b) =>
          b.textContent?.includes("已注册仓库") &&
          b.textContent.includes("30 个") &&
          !b.textContent.includes("上次记录"),
      ),
    );
    await usable(entry, page);
    // Project filtering must not affect registration scope or count.
    if (mobile)
      await page.getByRole("combobox", { name: "按项目筛选工作项" }).fill("selected-project");
    const before = reads;
    await entry.click();
    const panel = page.getByRole("dialog", { name: "已注册仓库", exact: true });
    await panel.getByText("已读取登记。", { exact: true }).waitFor();
    assert(reads > before, "reopening reads the independent API");
    assert.equal(
      await panel.getByRole("list", { name: "仓库登记列表" }).locator(":scope > li").count(),
      30,
    );
    await panel.getByText("启用", { exact: true }).first().waitFor();
    await panel.getByText("已退役", { exact: true }).waitFor();
    await panel
      .getByText("没有已登记且未退役的执行主机配置为服务此仓库。", { exact: true })
      .waitFor();
    assert.equal(
      await panel
        .getByRole("heading", { name: "sample-app", exact: true })
        .locator("../..")
        .getByText("host-b", { exact: true })
        .count(),
      1,
      "offline service host remains registered",
    );
    assert(
      await panel.evaluate((element) => element.scrollWidth <= element.clientWidth),
      "no dialog horizontal overflow",
    );
    if (output && viewport.height !== 640)
      await page.screenshot({ path: join(output, `repos-${viewport.width}.png`) });
    const long = panel.getByRole("heading", {
      name: "shared-library-with-a-very-long-logical-name-that-must-wrap-completely-on-mobile-without-truncation",
      exact: true,
    });
    await usable(long, page);
    assert(
      await long.evaluate((element) => element.scrollWidth <= element.clientWidth),
      "long logical name wraps",
    );
    await usable(panel.getByRole("heading", { name: "sample-library-26", exact: true }), page);
    await usable(panel.getByRole("button", { name: "关闭已注册仓库" }), page);
    await page.keyboard.press("Tab");
    assert(
      await panel.evaluate((element) => element.contains(globalThis.document.activeElement)),
      "modal traps focus",
    );
    await panel.getByRole("button", { name: "关闭已注册仓库" }).click();
    await closed(page, entry);
    await entry.click();
    await panel.waitFor();
    await page.keyboard.press("Escape");
    await closed(page, entry);
    await entry.click();
    await panel.waitFor();
    await page.mouse.click(1, 1);
    await closed(page, entry);

    if (mobile) {
      const summary = page.locator("summary").filter({ hasText: "执行主机" });
      await usable(summary, page);
      await summary.click();
      const recent = page.locator("summary").filter({ hasText: "最近完成" });
      await recent.click();
      // Both panels expanded with real task rows must leave usable board space.
      await usable(page.getByRole("button", { name: "Completed sample 5 已完成" }).last(), page);
      const queuedCard = page
        .getByRole("region", { name: "待安排", exact: true })
        .getByRole("button", { name: "Sample ongoing work item", exact: true });
      await usable(queuedCard, page);
      const cardBox = await queuedCard.locator("..").boundingBox();
      assert(
        cardBox && cardBox.height >= 60,
        "expanded resources must not squeeze work-item cards",
      );
      const lastHost = page.getByRole("button", { name: "主机详情：host-l" }).last();
      await usable(lastHost, page);
      await lastHost.click();
      await page.getByRole("dialog", { name: "主机详情", exact: true }).waitFor();
      await page.getByRole("button", { name: "关闭主机详情" }).click();
      await usable(entry, page);
      await usable(page.getByRole("region", { name: "工作项白板" }), page);
      await summary.click();
      await recent.click();
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("button", { name: "待处理", exact: true })
        .click();
      const settings = page
        .getByRole("button", { name: "设置", exact: true })
        .filter({ visible: true });
      await settings.click({ trial: true });
      await usable(settings, page);
      await settings.click();
      await page.getByRole("dialog", { name: "设置", exact: true }).waitFor();
      await page.getByRole("button", { name: "关闭设置" }).click();
      const tabs = page.getByRole("navigation", { name: "视图" });
      for (const name of ["对话", "待处理", "工作项"]) {
        const tab = tabs.getByRole("button", { name, exact: true });
        await usable(tab, page);
        await tab.click();
      }
    }

    if (!mobile) await page.getByRole("button", { name: "工作项 · 白板 ›", exact: true }).click();
    // Historical tasks that predate work items must remain reachable and paginated.
    await page
      .locator("main header")
      .getByRole("button", { name: "执行记录", exact: true })
      .click();
    await page.getByRole("heading", { name: "执行记录", exact: true }).waitFor();
    const executions = page.getByRole("region", { name: "分阶段任务看板", exact: true });
    for (const [lifecycle, label] of [
      ["completed", "已完成"],
      ["cancelled", "已取消"],
    ] as const) {
      const column = executions.getByRole("region", { name: label, exact: true });
      const more = column.getByRole("button", { name: `加载更多${label}`, exact: true });
      await more.scrollIntoViewIfNeeded();
      await more.click();
      const oldTask = column.getByRole("button", {
        name: new RegExp(`Unlinked historical ${lifecycle} page 2`),
      });
      await oldTask.scrollIntoViewIfNeeded();
      await oldTask.click();
      const details = page.getByRole("region", { name: "任务详情", exact: true });
      await details.getByText(`Unlinked historical ${lifecycle} page 2`, { exact: true }).waitFor();
      await details.getByRole("button", { name: "关闭", exact: true }).click();
    }
    await page.getByRole("button", { name: "返回工作项", exact: true }).click();
    await page.getByRole("region", { name: "工作项白板", exact: true }).waitFor();

    if (viewport.width === 390 && viewport.height === 844) {
      // Visible loading retains the last successful list; failures cannot become zero.
      pause = true;
      await entry.click();
      await panel.getByText(/正在读取登记/).waitFor();
      assert.equal(
        await panel.getByRole("heading", { name: "sample-app", exact: true }).count(),
        1,
      );
      pause = false;
      for (const response of held.splice(0)) json(response, registry);
      await panel.getByText("已读取登记。", { exact: true }).waitFor();
      fail = true;
      await panel.getByRole("button", { name: "刷新仓库" }).click();
      await panel.getByText(/读取失败，请重试/).waitFor();
      assert((await entry.textContent())?.includes("30 个 · 上次记录"));
      fail = false;
      registry = { repos: [...registrations.repos!, { repo: "new-registration", hosts: [] }] };
      await panel.getByRole("button", { name: "重试读取仓库" }).click();
      await panel.getByText("全部 31 个 · 含已退役仓库", { exact: true }).waitFor();
      await context.setOffline(true);
      for (const response of streams) response.end();
      await panel.getByText(/连接已断开/).waitFor();
      assert((await entry.textContent())?.includes("31 个 · 上次记录"));
      await context.setOffline(false);
      registry = { repos: [...registry.repos!, { repo: "reconnected-registration", hosts: [] }] };
      await panel
        .getByText("全部 32 个 · 含已退役仓库", { exact: true })
        .waitFor({ timeout: 15000 });
      await panel.getByText("已读取登记。", { exact: true }).waitFor();
      // Superseding after headers must not make the healthy connection offline.
      partial = true;
      const partialResponse = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/v1/repos",
      );
      await panel.getByRole("button", { name: "刷新仓库" }).click();
      await partialResponse;
      await panel.getByRole("button", { name: "刷新仓库" }).click();
      await panel.getByText("已读取登记。", { exact: true }).waitFor();
      assert.equal(await panel.getByRole("button", { name: "刷新仓库" }).isEnabled(), true);
      assert.equal(await panel.getByText(/连接已断开/).count(), 0);
      for (const response of partialBodies.splice(0)) response.end();
      registry = { repos: [] };
      await panel.getByRole("button", { name: "刷新仓库" }).click();
      await panel.getByText("账本中暂无已注册仓库。", { exact: true }).waitFor();
      await panel.getByRole("button", { name: "关闭已注册仓库" }).click();
      registry = registrations;
      await page.reload();
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("button", { name: "工作项", exact: true })
        .click();
      await entry.click();
      await panel.getByText("全部 30 个 · 含已退役仓库", { exact: true }).waitFor();
    }
    assert.deepEqual(errors, [], "no browser runtime errors");
    console.log(
      `PASS ${viewport.width}x${viewport.height}: registry, scrolling, focus, resources, navigation`,
    );
    await context.close();
  }
} finally {
  for (const response of partialBodies) response.end();
  for (const response of held) response.end();
  for (const response of streams) response.end();
  await browser.close();
  await server.close();
}
