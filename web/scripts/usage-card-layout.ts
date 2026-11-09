/// <reference lib="dom" />
// Built product, fictional API data, no business writes.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";
import type { TokenUsageTotals, TokenUsageView } from "../src/api/generated/api.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT ?? join(tmpdir(), "tend-usage-card-layout");
await mkdir(output, { recursive: true });
const title = "核查 Issue 消息“正在自动补齐”标签的含义、重复显示与收敛";
const longTitle = title.repeat(3);
const totals: TokenUsageTotals = {
  requests: 1,
  input: 13489814,
  input_known: 1,
  cache_read: 12522496,
  cache_read_known: 1,
  cache_write: 0,
  cache_write_known: 1,
  output: 65462,
  output_known: 1,
  total: 13555276,
  total_known: 1,
  final: 1,
  estimated_requests: 1,
  estimated_usd: "3.84",
};
const issues = [
  { id: "wi_01M6T8XYREBXEZRSTBK5H3AMEY", title, parent_id: "", models: ["gpt-6.1-sol"] },
  { id: "", title: "主会话与未归属用量", parent_id: "", models: ["gpt-6.1-sol", "model-fixture"] },
  { id: "wi_parent", title: longTitle, parent_id: "", models: ["gpt-6.1-sol"] },
  {
    id: "wi_child",
    title: longTitle,
    parent_id: "wi_parent",
    models: ["long-model-name-".repeat(9)],
  },
].map((issue) => ({
  ...issue,
  path: issue.id ? [{ id: issue.id, title: issue.title }] : [],
  direct: totals,
  totals: { ...totals, estimated_usd: issue.id ? "3.84" : "84.99" },
  sessions: [],
}));
const sessions = [
  {
    id: "session_issue",
    attributions: [{ issue_path: [{ id: issues[0]!.id, title: longTitle }], role: "implement" }],
  },
  { id: "session_main", attributions: [{ role: "main" }] },
  { id: "session_unassigned", attributions: [] },
].map((session) => ({
  ...session,
  attributions: session.attributions.map((attribution) => ({
    agent: "Fixture agent",
    agent_id: "fixture/agent",
    categories: [],
    run_id: "",
    task_id: "",
    task_title: "",
    issue_path: [],
    ...attribution,
  })),
  parent_session_id: "",
  models: ["gpt-6.1-sol"],
  first_at: 1793066400000,
  last_at: 1793066400000,
  totals,
  executions: [],
}));
const reads: string[] = [];
const writes: string[] = [];
const server = createServer(async (req, res) => {
  const url = new URL(req.url!, "http://localhost");
  const path = url.pathname;
  const json = (value: unknown) => {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(value));
  };
  res.setHeader("Cache-Control", "no-store");
  if (req.method !== "GET" && path !== "/api/v1/session") {
    writes.push(`${req.method} ${path}`);
    res.statusCode = 405;
    return json({ error: { code: "forbidden" } });
  }
  if (path.startsWith("/api/")) reads.push(path + url.search);
  if (path === "/api/v1/session")
    return json({
      token: "fixture",
      login: "Fixture",
      scope: "owner",
      deployment_id: "usage-card-fixture",
    });
  if (path === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], projects: [], hosts: [], queue: [], decisions: [] });
  if (path === "/api/v1/issues") return json({ items: [], more: false });
  if (path === "/api/v1/agent-conversations") return json({ enabled: false, items: [] });
  if (path === "/api/v1/token-usage") {
    const selected = url.searchParams.get("issue");
    const view: TokenUsageView = {
      revision: 1,
      as_of: 1793066400000,
      timezone: "Asia/Shanghai",
      window: url.searchParams.get("window") ?? "total",
      start: null,
      end: 1793066400000,
      totals,
      issues: selected ? issues.filter((i) => (i.id || "_unassigned") === selected) : issues,
      sessions: url.searchParams.has("session")
        ? sessions.filter((s) => s.id === url.searchParams.get("session"))
        : sessions,
      models: [{ name: "gpt-6.1-sol", ...totals }],
      roles: [],
      records: [],
      trend: [],
      unattributed: { ...totals, requests: 0 },
      more: false,
      next: 0,
      conflicts: 0,
      collection: { sources: 1, pending: 0, legacy_requests: 0, last_synced_at: 1793066400000 },
      pricing: { source: "OpenRouter", currency: "USD", quoted_at: 1793066400000 },
      coverage: "Fictional layout fixture; not real usage.",
    };
    return json(view);
  }
  if (path === "/api/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(": fixture\n\n");
    return;
  }
  if (path.startsWith("/api/")) return json({ items: [], enabled: false });
  try {
    const file = resolve(root, path.split("/").at(-1)?.includes(".") ? `.${path}` : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "Content-Type",
      path.endsWith(".js") ? "text/javascript" : path.endsWith(".css") ? "text/css" : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const browser = await chromium.launch();
const measurements: unknown[] = [];
const failures: string[] = [];
try {
  for (const width of [392, 320, 1440]) {
    const context = await browser.newContext({
      viewport: { width, height: 900 },
      serviceWorkers: "block",
    });
    try {
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${address.port}/usage`);
      const panel = page.getByRole("region", { name: "Token 用量", exact: true });
      await panel.getByText("13,555,276", { exact: true }).first().waitFor();
      assert.equal(
        await panel.getByRole("combobox", { name: "用量时间范围" }).inputValue(),
        "total",
      );
      for (const group of ["issue", "session"]) {
        if (group === "issue")
          await panel.getByRole("button", { name: `展开${longTitle}`, exact: true }).click();
        else await panel.getByRole("tab", { name: "按 Session", exact: true }).click();
        const table = panel.getByRole("table", {
          name: group === "issue" ? "Issue 用量" : "Session 用量",
        });
        await table.getByRole("button").first().waitFor();
        await page.evaluate(() => document.fonts.ready);
        const rows = table.getByRole("row").filter({ has: page.getByRole("cell") });
        const geometry = await rows.evaluateAll((rows) =>
          rows.map((row) => {
            const cells = [...row.querySelectorAll('[role="cell"]')];
            const button = cells[0]!.querySelector("button:last-child")!;
            const rect = (el: Element) => {
              const r = el.getBoundingClientRect();
              return {
                x: r.x,
                y: r.y,
                right: r.right,
                bottom: r.bottom,
                width: r.width,
                height: r.height,
              };
            };
            const text = [...button.children].map((el) => {
              const range = document.createRange();
              range.selectNodeContents(el);
              const r = range.getBoundingClientRect();
              return { text: el.textContent, x: r.x, y: r.y, right: r.right, bottom: r.bottom };
            });
            return {
              label: button.textContent,
              row: rect(row),
              button: rect(button),
              text,
              cells: cells.map(rect),
              values: cells.map((el) => el.textContent),
            };
          }),
        );
        const overflow = await page.evaluate(() => ({
          width: innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
        }));
        measurements.push({ width, group, overflow, geometry });
        for (const row of geometry) {
          for (const text of row.text) {
            if (
              text.x < row.button.x - 1 ||
              text.right > row.button.right + 1 ||
              text.y < row.button.y - 1 ||
              text.bottom > row.button.bottom + 1
            )
              failures.push(`${width}/${group}: text escapes title button: ${row.label}`);
            if (width <= 760 && text.bottom > row.cells[1]!.y)
              failures.push(`${width}/${group}: title overlaps model: ${row.label}`);
          }
          const overlap = (a: typeof row.button, b: typeof row.button) =>
            Math.min(a.right, b.right) > Math.max(a.x, b.x) + 1 &&
            Math.min(a.bottom, b.bottom) > Math.max(a.y, b.y) + 1;
          for (let i = 0; i < row.cells.length; i++)
            for (let j = i + 1; j < row.cells.length; j++)
              if (overlap(row.cells[i]!, row.cells[j]!))
                failures.push(`${width}/${group}: cells ${i}/${j} overlap`);
          assert.match(row.values[2]!, /13,555,276/);
          assert.match(row.values[2]!, /输入 13,489,814/);
          assert.match(row.values[2]!, /缓存 12,522,496/);
          assert.match(row.values[2]!, /输出 65,462/);
          assert.equal(
            row.values[3],
            group === "issue" && row.label?.includes("主会话") ? "$84.99" : "$3.84",
          );
        }
        if (overflow.scrollWidth > width) failures.push(`${width}/${group}: page overflows`);
        await page.screenshot({ path: join(output, `${group}-${width}.png`), fullPage: true });
        await rows.first().getByRole("button").last().focus();
        await page.keyboard.press("Enter");
        const dialog = page.getByRole("dialog");
        await dialog.getByText(/13,555,276 Token/).waitFor();
        await dialog.getByRole("button", { name: /^关闭/ }).click();
        await dialog.waitFor({ state: "detached" });
        assert.equal(new URL(page.url()).pathname, "/usage");
      }
      await page.reload();
      assert.equal(
        await panel.getByRole("tab", { name: "按 Session" }).getAttribute("aria-selected"),
        "true",
      );
      await panel.getByRole("tab", { name: "按 Issue", exact: true }).click();
      await panel.getByRole("button", { name: /主会话与未归属用量/ }).click();
      await page.getByRole("dialog", { name: "主会话与未归属用量" }).waitFor();
      assert(
        reads.some((path) => path.includes("issue=_unassigned") && path.includes("details=1")),
      );
      assert.deepEqual(errors, []);
      console.log(`PASS interactions and values ${width}`);
    } finally {
      await context.close();
    }
  }
  assert.deepEqual(writes, []);
  await writeFile(
    join(output, "geometry.json"),
    JSON.stringify(
      {
        browser: browser.version(),
        source: "isolated built product; fictional API fixtures; emulated viewports, not a phone",
        reads,
        writes,
        measurements,
        failures,
      },
      null,
      2,
    ),
  );
  assert.deepEqual(failures, []);
  console.log(`PASS geometry; Chromium ${browser.version()}; evidence ${output}`);
} finally {
  await browser.close();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
