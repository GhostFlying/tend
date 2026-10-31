import { editIssueFilters, navigate } from "./navigation.ts";
import { fixtureRoot } from "./navigation.ts";
import { issueBoardFixture } from "./issue-fixture.ts";
/// <reference lib="dom" />
import { issuePage, issueView } from "./issue-fixture.ts";
// Real Chromium regression against the actual app and a generic owner API fixture.
// Output is optional and must live outside the repository.
import { closeIssue } from "./issue-detail-helpers.ts";
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
    { repo: "legacy-zeta-library", retired: true, hosts: ["host-a"] },
    { repo: "sample-app", hosts: ["host-a", "host-b"] },
    { repo: "legacy-alpha-library", retired: true, hosts: [] },
    { repo: "unserved-library", hosts: [] },
    {
      repo: "shared-library-with-a-very-long-logical-name-that-must-wrap-completely-on-mobile-without-truncation",
      hosts: ["host-b"],
    },
    ...Array.from({ length: 25 }, (_, i) => ({
      repo: `sample-library-${String(i + 1).padStart(2, "0")}`,
      hosts: ["host-a"],
    })),
  ],
};
const registrationOrder = [
  "sample-app",
  "unserved-library",
  "shared-library-with-a-very-long-logical-name-that-must-wrap-completely-on-mobile-without-truncation",
  ...Array.from({ length: 25 }, (_, i) => `sample-library-${String(i + 1).padStart(2, "0")}`),
  "legacy-zeta-library",
  "legacy-alpha-library",
];
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
  projects: [
    {
      project: "selected-project",
      title: "Selected project",
      weight: 1,
      queued: 0,
      open_runs: 0,
      open_tasks: 0,
    },
  ],
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
let holdIssueBoard = false;
let issueReadArrived: (() => void) | undefined;
const heldIssues: (() => void)[] = [];
const partialBodies: ServerResponse[] = [];
const held: ServerResponse[] = [];
const streams = new Set<ServerResponse>();
function json(res: ServerResponse, body: unknown, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(issueBoardFixture(res.req.url, body)));
}
const server = await createServer({
  configFile: false,
  cacheDir: fixtureRoot("cache"),
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
          if (path.endsWith("/activity") || path.endsWith("/results"))
            return json(res, { items: [], more: false, event_seq: 0 });
          if (path === "/api/v1/issues") {
            const send = () =>
              json(
                res,
                issuePage(url.searchParams.get("project") || "sample-project", [
                  task("completed", "Completed sample 1", "sample-project"),
                ]),
              );
            if (holdIssueBoard) {
              heldIssues.push(send);
              issueReadArrived?.();
              return;
            }
            return send();
          }
          if (path === "/api/v1/issues/wi_01M5M3ME31875GDA8F7P5XTD74")
            return json(res, issueView());
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
            const lifecycles = (url.searchParams.get("lifecycle")?.split(",") || [
              "queued",
            ]) as TaskSummary["lifecycle"][];
            const project = url.searchParams.get("project") || "sample-project";
            const pageTwo = url.searchParams.has("before");
            return json(res, {
              tasks: pageTwo
                ? lifecycles
                    .filter((l) => ["completed", "cancelled"].includes(l))
                    .map((l) => task(l, `Unlinked historical ${l} page 2`, project))
                : lifecycles.length === 1 && lifecycles[0] === "completed"
                  ? Array.from({ length: 5 }, (_, i) =>
                      task("completed", `Completed sample ${i + 1}`, project),
                    )
                  : lifecycles.map((l) => ({
                      ...task(l, `Task ${l}`, project),
                      primary_issue_id: "wi_01M5M3ME31875GDA8F7P5XTD74",
                      primary_issue_title: "Mobile work item",
                      execution: {
                        state:
                          l === "running"
                            ? "starting"
                            : l === "awaiting_acceptance"
                              ? "queued"
                              : l === "changes_required"
                                ? "waiting_owner"
                                : l === "draft"
                                  ? "idle"
                                  : l,
                        ...(l === "awaiting_acceptance"
                          ? {
                              role: "accept",
                              queue_reason: {
                                code: "host_capacity",
                                message: "waiting for slot",
                                host_id: "host-a",
                              },
                            }
                          : {}),
                      },
                    })),
              next: !pageTwo && lifecycles.includes("cancelled") ? "history-page-2" : "",
            });
          }

          const views: Record<string, unknown> = {
            "/api/v1/session": {
              deployment_id: "layout-fixture",
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
    .getByRole("region", { name: "已注册仓库", exact: true })
    .waitFor({ state: "detached" });
  assert(
    await entry.evaluate((element) => globalThis.document.activeElement === element),
    "return focus to entry",
  );
}
async function ordered(panel: Locator, expected: string[]) {
  assert.deepEqual(
    await panel.getByRole("list", { name: "仓库登记列表" }).getByRole("heading").allTextContents(),
    expected,
    "active registrations precede archived registrations, preserving both input orders",
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
        .getByRole("link", { name: "Issues", exact: true })
        .click();
    else await page.getByRole("link", { name: "Issues", exact: true }).click();
    assert.equal(await page.getByRole("button", { name: "资源与记录", exact: true }).count(), 0);
    const resourcesEntry = page.getByRole("button", { name: "新建 Issue", exact: true });
    await resourcesEntry.focus();
    await navigate(page, "/hosts");
    const hostsPage = page.getByRole("region", { name: "执行主机", exact: true });
    const hostEntry = hostsPage.getByRole("button", { name: "主机详情：host-a", exact: true });
    await hostEntry.click();
    const hostDetails = page.getByRole("dialog", { name: "主机详情", exact: true });
    await hostDetails.getByRole("heading", { name: "host-a", exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await hostDetails.waitFor({ state: "detached" });
    assert(await hostsPage.isVisible(), "host Escape must preserve the hosts page");
    assert(
      await hostEntry.evaluate((element) => document.activeElement === element),
      "host Escape must return focus to the invoking host button",
    );
    if (output) await page.screenshot({ path: join(output, `host-escape-${viewport.width}.png`) });
    // The heading mounts before board counts add a header child. Hold that read
    // to exercise the router's asynchronous opener restoration, not just its heading.
    holdIssueBoard = true;
    const boardArrived = new Promise<void>((resolve) => {
      issueReadArrived = resolve;
    });
    await page.goBack();
    await page.getByRole("heading", { name: "所有 Issue", exact: true }).waitFor();
    await boardArrived;
    assert.equal(
      await resourcesEntry.evaluate((element) => document.activeElement === element),
      false,
    );
    holdIssueBoard = false;
    issueReadArrived = undefined;
    for (const send of heldIssues.splice(0)) send();
    await page.waitForFunction(() => document.activeElement?.textContent === "新建 Issue");
    assert(
      await resourcesEntry.evaluate((element) => document.activeElement === element),
      "resource back must return focus to its entry",
    );
    if (mobile) {
      await editIssueFilters(page, () =>
        page.getByLabel("按项目筛选 Issue").fill("selected-project"),
      );
      await navigate(page, "/tasks");
      await page
        .locator("main details > summary")
        .filter({ hasText: /^资源与记录$/ })
        .click();
    }
    const entry = mobile
      ? page.locator("main").getByRole("button", { name: /^已注册仓库/ })
      : page.locator("aside").getByRole("link", { name: "仓库", exact: true });
    await usable(entry, page);
    const before = reads;
    const panel = page.getByRole("region", {
      name: "已注册仓库",
      exact: true,
    });
    const openRegistry = async () => {
      await entry.click();
    };
    const closeRegistry = { click: () => page.goBack() };
    const registryRefresh = panel.getByRole("button", { name: "刷新仓库", exact: true });
    await openRegistry();
    await panel.getByText("已读取登记。", { exact: true }).waitFor();
    assert(reads > before, "reopening reads the independent API");
    assert.equal(
      await panel.getByRole("list", { name: "仓库登记列表" }).locator(":scope > li").count(),
      30,
    );
    await panel.getByText("启用", { exact: true }).first().waitFor();
    assert.equal(await panel.getByText("已归档", { exact: true }).count(), 2);
    assert.equal(await panel.getByText(/已退役/).count(), 0);
    await panel.getByText("全部 30 个 · 含已归档仓库", { exact: true }).waitFor();
    await ordered(panel, registrationOrder);
    await panel.getByRole("button", { name: "刷新仓库" }).click();
    await panel.getByText("已读取登记。", { exact: true }).waitFor();
    await ordered(panel, registrationOrder);
    await panel
      .getByText("没有已登记且未退役的执行主机配置为服务此仓库。", { exact: true })
      .first()
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
    await usable(panel.getByRole("heading", { name: "legacy-alpha-library", exact: true }), page);
    await panel
      .getByText("停止新任务挂载，已有任务仍可由配置主机服务。", { exact: true })
      .last()
      .waitFor();
    if (output && viewport.height !== 640)
      await page.screenshot({ path: join(output, `repos-archived-${viewport.width}.png`) });
    // Refresh data changes must apply the same partition without alphabetizing either group.
    for (const scenario of [
      {
        repos: [
          { repo: "archive-zeta", retired: true, hosts: [] },
          { repo: "active-zeta", retired: false, hosts: [] },
          { repo: "archive-alpha", retired: true, hosts: ["host-a"] },
          { repo: "active-alpha", hosts: ["host-b"] },
        ],
        expected: ["active-zeta", "active-alpha", "archive-zeta", "archive-alpha"],
      },
      {
        repos: [
          { repo: "archive-zeta", retired: true, hosts: [] },
          { repo: "archive-alpha", retired: true, hosts: ["host-a"] },
        ],
        expected: ["archive-zeta", "archive-alpha"],
      },
      {
        repos: [
          { repo: "active-zeta", retired: false, hosts: [] },
          { repo: "active-alpha", hosts: ["host-b"] },
        ],
        expected: ["active-zeta", "active-alpha"],
      },
      { repos: [], expected: [] },
    ]) {
      registry = { repos: scenario.repos };
      for (let refresh = 0; refresh < 2; refresh++) {
        await panel.getByRole("button", { name: "刷新仓库" }).click();
        await panel.getByText("已读取登记。", { exact: true }).waitFor();
        if (scenario.expected.length) await ordered(panel, scenario.expected);
        else {
          await panel.getByText("账本中暂无已注册仓库。", { exact: true }).waitFor();
          assert.equal(await panel.getByRole("list", { name: "仓库登记列表" }).count(), 0);
        }
      }
    }
    registry = registrations;
    await panel.getByRole("button", { name: "刷新仓库" }).click();
    await panel.getByText("已读取登记。", { exact: true }).waitFor();
    await ordered(panel, registrationOrder);
    await usable(registryRefresh, page);
    await panel.getByRole("heading", { name: "已注册仓库", exact: true, level: 1 }).focus();
    await page.keyboard.press("Tab");
    assert(
      await panel.evaluate((element) => element.contains(globalThis.document.activeElement)),
      "page controls remain keyboard reachable",
    );
    await closeRegistry.click();
    await closed(page, entry);
    await openRegistry();
    await panel.waitFor();
    await page.keyboard.press("Escape");
    assert(await panel.isVisible(), "Escape does not leave a page");
    await closeRegistry.click();
    await closed(page, entry);
    await openRegistry();
    await panel.waitFor();
    await page.mouse.click(1, 1);
    assert(await panel.isVisible(), "page content stays mounted after outside clicks");
    await closeRegistry.click();
    await closed(page, entry);

    if (mobile) {
      const resources = page
        .locator("main details")
        .filter({ has: page.locator(":scope > summary", { hasText: /^资源与记录$/ }) });
      await resources
        .locator("summary")
        .filter({ hasText: /^最近完成的任务$/ })
        .click();
      await usable(resources.getByRole("button", { name: /Completed sample 5/ }), page);
      await resources
        .locator("summary")
        .filter({ hasText: /^执行主机/ })
        .click();
      const lastHost = resources.getByRole("button", { name: "主机详情：host-l" });
      await usable(lastHost, page);
      await lastHost.click();
      await page.getByRole("dialog", { name: "主机详情", exact: true }).waitFor();
      await page.getByRole("button", { name: "关闭主机详情" }).click();
      await resources.locator(":scope > summary").click();
      await page.goBack();
      await usable(
        page
          .getByRole("region", { name: "Issue 白板" })
          .getByRole("button", { name: "Sample ongoing Issue", exact: true }),
        page,
      );
      await navigate(page, "/tasks");
    } else {
      await page.getByRole("link", { name: "Issues", exact: true }).click();
      await navigate(page, "/tasks");
    }
    // Historical tasks that predate work items must remain reachable and paginated.
    await page.getByRole("heading", { name: "任务执行", exact: true }).waitFor();
    const executions = page.getByRole("region", { name: "任务执行列表", exact: true });
    await executions.getByText("正在启动", { exact: true }).waitFor();
    await executions.getByText("排队中 · 待验收", { exact: true }).waitFor();
    await executions.getByText("主机 host-a", { exact: true }).waitFor();
    assert.equal(await executions.locator("article").count(), 6);
    const firstRow = await executions.locator("article").first().boundingBox();
    assert(firstRow && firstRow.height <= (mobile ? 160 : 110), "execution entries stay compact");
    const taskTabs = executions.getByRole("tablist", { name: "任务范围" });
    assert(
      await taskTabs.evaluate((el) => el.getBoundingClientRect().bottom < innerHeight / 2),
      "execution tabs appear in the first half of the viewport",
    );
    if (mobile) {
      const resources = page
        .locator("main details")
        .filter({ has: page.locator("summary", { hasText: /^资源与记录$/ }) });
      assert.equal(
        await resources.getAttribute("open"),
        null,
        "resources remain collapsed after returning",
      );
      await resources.locator(":scope > summary").click();
      await resources.getByText("最近完成的任务", { exact: true }).waitFor();
      await resources.locator(":scope > summary").click();
    }

    if (output)
      await page.screenshot({
        path: join(output, `tasks-current-${viewport.width}x${viewport.height}.png`),
      });
    await executions
      .getByRole("button", { name: "所属 Issue：Mobile work item ›", exact: true })
      .first()
      .click();
    const parentDetail = page.getByRole("region", { name: "Issue 详情", exact: true });
    await parentDetail
      .getByRole("heading", { name: "Sample ongoing Issue", exact: true })
      .waitFor();
    await closeIssue(page);
    const ended = executions.getByRole("tab", { name: /^已结束(?: · \d+\+?)?$/ });
    await ended.focus();
    await page.keyboard.press("Enter");
    await executions.getByRole("button", { name: "查看结果与验收 ›", exact: true }).waitFor();
    await executions.getByRole("button", { name: "查看取消记录 ›", exact: true }).waitFor();
    assert.equal(await executions.locator("article").count(), 2);
    if (output)
      await page.screenshot({
        path: join(output, `tasks-ended-${viewport.width}x${viewport.height}.png`),
      });
    await executions.getByRole("button", { name: "加载更多任务", exact: true }).click();
    for (const lifecycle of ["completed", "cancelled"]) {
      const oldTask = executions.getByRole("button", {
        name: new RegExp(`Unlinked historical ${lifecycle} page 2`),
      });
      await oldTask.click();
      const details = page.getByRole("region", { name: "任务详情", exact: true });
      await details.getByText(`Unlinked historical ${lifecycle} page 2`, { exact: true }).waitFor();
      await details.getByRole("button", { name: /← 返回/ }).click();
    }
    await executions.getByRole("tab", { name: /^全部(?: · \d+\+?)?$/ }).click();
    await executions.getByText("正在启动", { exact: true }).waitFor();
    assert.equal(await executions.locator("article").count(), 8);
    await executions.getByRole("tab", { name: /^全部(?: · \d+\+?)?$/ }).focus();
    await page.keyboard.press("Home");
    assert.equal(
      await executions
        .getByRole("tab", { name: /^当前(?: · \d+\+?)?$/ })
        .getAttribute("aria-selected"),
      "true",
    );
    assert(
      await executions.evaluate((el) => el.scrollWidth <= el.clientWidth),
      "task list does not overflow horizontally",
    );
    await page.goBack();
    await page.getByRole("region", { name: "Issue 白板", exact: true }).waitFor();
    if (mobile) {
      await navigate(page, "/tasks");
      await page
        .locator("main details > summary")
        .filter({ hasText: /^资源与记录$/ })
        .click();
    }

    if (viewport.width === 390 && viewport.height === 844) {
      // Visible loading retains the last successful list; failures cannot become zero.
      pause = true;
      await openRegistry();
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
      assert.match((await panel.textContent())!, /上次成功读取的记录/);
      assert.equal(
        await panel.getByRole("list", { name: "仓库登记列表" }).locator(":scope > li").count(),
        30,
      );
      fail = false;
      registry = { repos: [...registrations.repos!, { repo: "new-registration", hosts: [] }] };
      await panel.getByRole("button", { name: "重试读取仓库" }).click();
      await panel.getByText("全部 31 个 · 含已归档仓库", { exact: true }).waitFor();
      await ordered(panel, [
        ...registrationOrder.slice(0, -2),
        "new-registration",
        ...registrationOrder.slice(-2),
      ]);
      await context.setOffline(true);
      for (const response of streams) response.end();
      await panel.getByText(/连接已断开/).waitFor();
      assert.match((await panel.textContent())!, /上次成功读取的记录/);
      assert.equal(
        await panel.getByRole("list", { name: "仓库登记列表" }).locator(":scope > li").count(),
        31,
      );
      await context.setOffline(false);
      registry = { repos: [...registry.repos!, { repo: "reconnected-registration", hosts: [] }] };
      await panel
        .getByText("全部 32 个 · 含已归档仓库", { exact: true })
        .waitFor({ timeout: 15000 });
      await panel.getByText("已读取登记。", { exact: true }).waitFor();
      await ordered(panel, [
        ...registrationOrder.slice(0, -2),
        "new-registration",
        "reconnected-registration",
        ...registrationOrder.slice(-2),
      ]);
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
      await closeRegistry.click();
      registry = registrations;
      await page.reload();
      await page
        .getByRole("navigation", { name: "视图" })
        .getByRole("link", { name: "Issues", exact: true })
        .click();
      await navigate(page, "/tasks");
      await page
        .locator("main details > summary")
        .filter({ hasText: /^资源与记录$/ })
        .click();
      await openRegistry();
      await panel.getByText("全部 30 个 · 含已归档仓库", { exact: true }).waitFor();
      await ordered(panel, registrationOrder);
    }
    const currentOnly = "**/api/v1/tasks?*";
    await page.route(currentOnly, async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.get("lifecycle")?.startsWith("draft"))
        await route.fulfill({ json: { tasks: [] } });
      else await route.continue();
    });
    await navigate(page, "/");
    await navigate(page, "/tasks");
    await executions.getByRole("tab", { name: /^已结束(?: · \d+\+?)?$/ }).waitFor();
    await page.waitForFunction(() =>
      document
        .querySelector('[role="tab"][aria-selected="true"]')
        ?.textContent?.startsWith("已结束"),
    );
    await executions
      .getByRole("button", { name: "查看结果与验收 ›", exact: true })
      .first()
      .waitFor();
    assert.equal(
      await executions.getByRole("tab", { name: /^当前 · 0$/ }).count(),
      1,
      "known empty current count",
    );
    await page.unroute(currentOnly);
    assert.deepEqual(errors, [], "no browser runtime errors");
    console.log(
      `PASS ${viewport.width}x${viewport.height}: archive labels, stable mixed/archived/active/empty order, refresh, registry, scrolling, focus, resources, navigation`,
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
