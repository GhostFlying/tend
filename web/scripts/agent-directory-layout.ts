/// <reference lib="dom" />
// Browser regression for the read-only directory. No real runner or model.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";
import type {
  AgentInventoryDefinition,
  AgentInventoryRuntime,
  RunnerAgentCatalog,
} from "../src/api/generated/api.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT ?? "/tmp/tend-agent-directory";
await mkdir(output, { recursive: true });
const hash = `sha256:${"a".repeat(64)}`;
const categories = [
  ["implementation", "复杂或存在不确定性的实现任务"],
  ["implementation-economy", "简单、确定、方案清楚的实现任务"],
  ["investigation", "源码调查、分析与复现"],
  ["formal-review", "独立正式评审与验收"],
].map(([id, description]) => ({
  category_id: id!,
  title: (
    {
      implementation: "Implementation",
      "implementation-economy": "Economy implementation",
      investigation: "Investigation",
      "formal-review": "Formal review",
    } as Record<string, string>
  )[id!]!,
  description: description!,
  revision: 1,
  state: "active",
}));
const runtimes: AgentInventoryRuntime[] = [
  {
    runtime_id: "codex",
    revision: 1,
    kind: "orca-worker",
    cli: "codex",
    declared_purposes: ["worker_run"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
  {
    runtime_id: "traex",
    revision: 1,
    kind: "orca-worker",
    cli: "traex",
    declared_purposes: ["worker_run"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
  {
    runtime_id: "native",
    revision: 1,
    kind: "appserver-worker",
    cli: "codex",
    declared_purposes: ["issue_manager"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
];
let secondHostOnline = true;
let failCatalog = false;
let nativeWorker = false;
const hosts = [
  {
    host_id: "host-a",
    online: true,
    status: "active",
    os: "linux",
    slots: 4,
    labels: [],
    repos: [],
  },
  {
    host_id: "host-b",
    online: true,
    status: "active",
    os: "linux",
    slots: 4,
    labels: [],
    repos: [],
  },
];
function catalog(host: string): RunnerAgentCatalog {
  const agents: AgentInventoryDefinition[] = [
    ["cedar", "Cedar", "implementation", "codex", "GPT-6.1-Sol", "high"],
    ["iris", "Iris", "investigation", "codex", "GPT-6.1-Sol", "medium"],
    ["maple", "Maple", "formal-review", "codex", "GPT-6.1-Sol", "xhigh"],
    ["birch", "Birch", "implementation-economy", "traex", "deepseek-v4.1-flash", "high"],
  ].map(([id, name, category, runtime, model, effort]) => ({
    identity: { execution_environment_id: host, local_id: id! },
    revision: 4,
    display_name:
      host === "host-a"
        ? name!
        : (
            { Cedar: "Pine", Iris: "Aster", Maple: "Elm", Birch: "Willow" } as Record<
              string,
              string
            >
          )[name!]!,
    runtime_binding_ref: { runtime_id: runtime!, revision: 1 },
    owner_runtime_binding_ref: { runtime_id: "native", revision: 1 },
    categories: [category!],
    purposes: ["worker_run"],
    requested_model: model!,
    requested_effort: effort!,
    enabled: true,
    registration: "staged",
    availability: {
      available: false,
      reason: id === "maple" ? "definition_disabled" : "runtime_not_verified",
    },
  }));
  const histories =
    host === "host-a"
      ? [2, 3].map((version) => {
          const historical: AgentInventoryDefinition = {
            ...agents[0]!,
            identity: { execution_environment_id: host, local_id: "elm" },
            display_name: `Elm v${version}`,
            revision: version,
            categories: [version === 2 ? "implementation" : "investigation"],
            requested_model: version === 2 ? "GPT-6.1-Sol" : "DeepSeek V4.1 Flash",
            runtime_binding_ref: { runtime_id: "codex", revision: version - 1 },
          };
          return {
            definition: historical,
            pin: {
              identity: historical.identity,
              agent_revision: version,
              runtime: historical.runtime_binding_ref,
              inventory_revision: version,
              snapshot_id: `historical-v${version}`,
              public_sha256: `sha256:${String(version).repeat(64)}`,
              profile: { model: historical.requested_model, effort: historical.requested_effort },
              worker: "orca" as const,
              backend_id: "orca",
            },
            runtimes: runtimes.map((runtime) =>
              runtime.runtime_id === "codex"
                ? { ...runtime, revision: version - 1, cli: version === 2 ? "codex" : "traex" }
                : runtime,
            ),
          };
        })
      : [];
  return {
    host_id: host,
    online: host === "host-a" || secondHostOnline,
    continuation_supported: true,
    last_heartbeat_at_ms: 1793586720000,
    current: {
      host_id: host,
      server_revision: 4,
      local_revision: 4,
      snapshot_id: "current",
      public_sha256: hash,
      request_id: "registration",
      state: "registered_staged",
      categories,
    },
    projection: {
      format_version: 2,
      execution_environment_id: host,
      source_kind: "local_file",
      runtimes: runtimes.map((runtime) =>
        nativeWorker && runtime.runtime_id === "codex"
          ? { ...runtime, kind: "appserver-worker" }
          : runtime,
      ),
      agents,
    },
    executors: agents.flatMap((a) =>
      ["", "issue_manager"].map((purpose) => ({
        available: a.enabled,
        pin: {
          identity: a.identity,
          agent_revision: 4,
          runtime: purpose ? a.owner_runtime_binding_ref! : a.runtime_binding_ref,
          inventory_revision: 4,
          snapshot_id: "current",
          public_sha256: hash,
          profile: { model: a.requested_model, effort: a.requested_effort },
          purpose,
          worker: purpose ? ("appserver" as const) : ("orca" as const),
          backend_id: purpose ? "" : "orca",
        },
      })),
    ),
    retained_definitions: histories,
    retained_executions: histories.map((history) => history.pin),
    continuations: histories.map((history, index) => ({
      pin: history.pin,
      available: index === 0,
    })),
  };
}
const hostedRuntime: AgentInventoryRuntime = {
  ...runtimes[2]!,
  runtime_id: "traex",
  kind: "hosted-manager",
  cli: "codex",
};
const hostedAgents: AgentInventoryDefinition[] = [
  ["consultant", "Sage", "consultation"],
  ["manager", "Laurel", "issue_manager"],
  ["planner", "Sequoia", "main"],
].map(([id, name, purpose]) => ({
  identity: { execution_environment_id: "hosted", local_id: id! },
  revision: 2,
  display_name: name!,
  runtime_binding_ref: { runtime_id: "traex", revision: 1 },
  categories: ["planning"],
  purposes: [purpose!],
  requested_model: "GPT-6.1-Sol",
  requested_effort: "high",
  enabled: true,
  registration: "staged",
  availability: { available: false, reason: "isolation_not_verified" },
}));
categories.unshift({
  category_id: "planning",
  title: "Planning",
  description: "规划与协调",
  revision: 1,
  state: "active",
});
let hostedCLI: string | undefined = "codex";
const streams = new Set<ServerResponse>();
const writes: string[] = [];
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://fixture.invalid"),
    p = u.pathname;
  const json = (value: unknown) => {
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify(value));
  };
  if (req.method !== "GET" && p !== "/api/v1/session") {
    writes.push(p);
    res.statusCode = 405;
    return json({});
  }
  if (p.endsWith("/events")) {
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/session")
    return json({
      token: "fixture",
      login: "owner",
      scope: "owner",
      client_id: "directory",
      deployment_id: "directory",
    });
  if (p === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], hosts, projects: [], queue: [], decisions: [] });
  if (p === "/api/v1/categories") return json({ items: categories, more: false });
  if (p === "/api/v1/hosted-agents")
    return json({
      state: "registered_staged",
      configuration_revision: 2,
      main_agent_id: "planner",
      projection: {
        format_version: 1,
        execution_environment_id: "hosted",
        source_kind: "inline",
        agents: hostedAgents,
        runtimes: [{ ...hostedRuntime, cli: hostedCLI }],
      },
    });
  if (/^\/api\/v1\/hosts\/[^/]+\/agents$/.test(p)) {
    if (failCatalog && p.includes("host-b")) {
      res.statusCode = 503;
      return json({ error: { code: "unavailable", message: "Temporary fixture failure" } });
    }
    return json(catalog(decodeURIComponent(p.split("/")[4]!)));
  }
  if (p === "/api/v1/messaging/messages") return json({ items: [], next: 0, more: false });
  if (p === "/api/v1/attention") return json({ items: [], more: false });
  if (p.startsWith("/api/"))
    return json({ items: [], more: false, revision: 1, enabled: false, available: false });
  try {
    const file = resolve(root, p.split("/").at(-1)?.includes(".") ? `.${p}` : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "content-type",
      p.endsWith(".js")
        ? "application/javascript"
        : p.endsWith(".css")
          ? "text/css"
          : p.endsWith(".json")
            ? "application/json"
            : p.endsWith(".png")
              ? "image/png"
              : "text/html",
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
const origin = `http://127.0.0.1:${address.port}`;
const browser = await chromium.launch();
try {
  for (const viewport of [
    { width: 1536, height: 1024 },
    { width: 1201, height: 900 },
    { width: 1024, height: 768 },
    { width: 390, height: 844 },
    { width: 320, height: 640 },
  ])
    for (const theme of ["light", "dark"] as const) {
      const context = await browser.newContext({
        viewport,
        colorScheme: theme,
        serviceWorkers: "block",
      });
      await context.addInitScript((value) => localStorage.setItem("tend.theme", value), theme);
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (e) => {
        if (e.type() === "error" && !(failCatalog && e.text().includes("503")))
          errors.push(e.text());
      });
      secondHostOnline = true;
      nativeWorker = false;
      hostedCLI = "codex";
      await page.goto(`${origin}/settings/agents?selected=hosted%2Fplanner`);
      await page.waitForURL((url) => url.pathname === "/agents");
      const directory = page.getByRole("region", { name: "Agents", exact: true });
      const mobileDetails = viewport.width <= 1399;
      const detail = () =>
        mobileDetails
          ? directory.locator('[class*="mobileDetails"]')
          : directory.getByRole("complementary");
      const backToList = async () => {
        if (mobileDetails)
          await directory.getByRole("button", { name: "返回列表", exact: true }).click();
      };
      await detail().getByRole("heading", { name: "Sequoia", exact: true }).waitFor();
      await detail().getByText("Codex · app-server", { exact: true }).waitFor();
      assert.equal(
        await detail().getByText("未上报", { exact: true }).filter({ visible: true }).count(),
        1,
      );
      if (mobileDetails) {
        await page.screenshot({
          path: join(output, `details-${viewport.width}-${theme}.png`),
          fullPage: false,
        });
        await backToList();
      }
      const names = directory.locator('[class*="identity"] strong').filter({ visible: true });
      await names.first().waitFor();
      assert.equal(await names.count(), 11);
      assert.equal(await names.first().innerText(), "Sequoia");
      const typeSizes = await directory.evaluate((root) => {
        const px = (element: Element) => parseFloat(getComputedStyle(element).fontSize);
        const brand = document.querySelector('[class*="brand"] b');
        return {
          name: px(root.querySelector('[class*="identity"] strong')!),
          title: px(root.querySelector("h1")!),
          brand: brand ? px(brand) : 16,
        };
      });
      assert(
        typeSizes.name < typeSizes.title && typeSizes.title <= typeSizes.brand,
        JSON.stringify(typeSizes),
      );
      assert.equal(
        await directory.getByRole("button", { name: /新增|编辑|保存|reload/ }).count(),
        0,
      );
      assert.equal(await page.getByText("hosted/CLI 未记录", { exact: true }).count(), 0);
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      await page.screenshot({
        path: join(output, `agents-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      if (mobileDetails) {
        await page.setViewportSize({
          width: viewport.width,
          height: Math.min(viewport.height, 512),
        });
        const willow = directory.getByRole("button", { name: /Willow/ });
        await willow.scrollIntoViewIfNeeded();
        const before = await directory.evaluate((element) => element.scrollTop);
        assert(before > 0);
        await willow.click();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        assert.equal(await directory.evaluate((element) => element.scrollTop), 0);
        failCatalog = true;
        await directory
          .getByRole("button", { name: "重新读取", exact: true })
          .filter({ visible: true })
          .click();
        await directory.getByRole("status").filter({ hasText: "部分目录暂时无法读取" }).waitFor();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        failCatalog = false;
        await directory
          .getByRole("button", { name: "重新读取", exact: true })
          .filter({ visible: true })
          .click();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        await directory
          .getByRole("status")
          .filter({ hasText: "部分目录暂时无法读取" })
          .waitFor({ state: "hidden" });
        await backToList();
        const after = await directory.evaluate((element) => element.scrollTop);
        assert(Math.abs(before - after) < 2, `list scroll changed from ${before} to ${after}`);
        assert.equal(await willow.evaluate((element) => document.activeElement === element), true);
        await page.setViewportSize(viewport);
        await directory.evaluate((element) => {
          element.scrollTop = 0;
        });
      }
      const filter = directory.getByRole("combobox", { name: "运行位置或方式筛选" });
      await filter.selectOption("location:host-b");
      assert.equal(await names.count(), 4);
      assert(await page.url().includes("location=host-b"));
      await page.reload();
      await names.first().waitFor();
      assert.equal(await names.count(), 4);
      await filter.selectOption("method:orca/traex");
      assert.equal(await names.count(), 2);
      await directory.getByRole("button", { name: /Birch birch|Birch.*Economy/ }).click();
      await detail().getByRole("heading", { name: "Birch", exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("TraeX · Orca · traex · v1", { exact: true }).waitFor();
      await detail().getByText("app-server", { exact: false }).first().waitFor();
      await backToList();
      await filter.selectOption("");
      await directory.getByRole("tab", { name: "Categories", exact: true }).click();
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).waitFor();
      assert.equal(await directory.getByRole("button", { name: /Elm v/ }).count(), 0);
      await page.screenshot({
        path: join(output, `categories-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).click();
      await detail().getByRole("heading", { name: "Cedar", exact: true }).waitFor();
      await backToList();
      await directory.getByRole("combobox", { name: "类别筛选", exact: true }).selectOption("");
      assert.equal(await directory.getByRole("button", { name: /Elm v2 elm/ }).isVisible(), false);
      await directory.getByText("历史定义（2）", { exact: true }).click();
      await directory.getByRole("button", { name: /Elm v2 elm|Elm v2.*Implementation/ }).click();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v2", { exact: true }).waitFor();
      await detail().getByText("Codex · Orca · codex · v1", { exact: true }).waitFor();
      await backToList();
      await directory.getByRole("button", { name: /Elm v3 elm|Elm v3.*Investigation/ }).click();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v3", { exact: true }).waitFor();
      await detail().getByText("TraeX · Orca · codex · v2", { exact: true }).waitFor();
      await page.reload();
      await detail().getByRole("heading", { name: "Elm v3", exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v3", { exact: true }).waitFor();
      await backToList();
      secondHostOnline = false;
      hostedCLI = undefined;
      await directory
        .getByRole("button", { name: "重新读取", exact: true })
        .filter({ visible: true })
        .click();
      await directory
        .getByRole("button", { name: /Pine/ })
        .getByText("离线", { exact: true })
        .waitFor();
      await directory.getByRole("button", { name: /Sequoia/ }).click();
      await detail().getByText("app-server", { exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("未声明", { exact: true }).waitFor();
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      await backToList();
      hostedCLI = "codex";
      nativeWorker = true;
      await directory
        .getByRole("button", { name: "重新读取", exact: true })
        .filter({ visible: true })
        .click();
      await filter
        .getByRole("option", { name: "Codex · app-server（服务端）", exact: true })
        .waitFor({ state: "attached" });
      await filter
        .getByRole("option", { name: "Codex · app-server（主机）", exact: true })
        .waitFor({ state: "attached" });
      await filter.selectOption("method:hosted/codex");
      assert.equal(await names.count(), 3);
      await filter.selectOption("method:app-server/codex");
      assert.equal(await names.count(), 6);
      assert.deepEqual(errors, []);
      assert.deepEqual(writes, []);
      console.log(
        `PASS directory ${viewport.width} ${theme}: typography, declared hosted CLI, unknown CLI, location/method filters, historical pins, offline refresh, read-only, no overflow or console errors`,
      );
      await context.close();
    }
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
